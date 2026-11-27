package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/armanlife/arman-vpn-client/core/internal/serverlist"
)

func pingTestCountries() []serverlist.Country {
	country := func(code, name, host string, locked bool) serverlist.Country {
		return serverlist.Country{Code: code, Name: name, Locked: locked, Cities: []serverlist.City{{Name: name, Servers: []serverlist.Server{{Hostname: host, Port: 2499}}}}}
	}
	return []serverlist.Country{
		country("US", "United States", "us.example", false),
		country("DE", "Germany", "de.example", false),
		country("JP", "Japan", "jp.example", true),
		country("FR", "France", "fr.example", false),
	}
}

func TestPingLocationsSortsByLatencyAndReportsFailures(t *testing.T) {
	latency := map[string]time.Duration{"us.example:2499": 180 * time.Millisecond, "de.example:2499": 40 * time.Millisecond, "jp.example:2499": 20 * time.Millisecond}
	meter := func(_ context.Context, target Candidate) (time.Duration, error) {
		if d, ok := latency[target.Addr]; ok {
			return d, nil
		}
		return 0, &proxyConnectHTTPError{statusCode: http.StatusForbidden}
	}
	results := PingLocations(context.Background(), PingTargets(pingTestCountries()), PingRun{Meter: meter})
	var order []string
	for _, r := range results {
		order = append(order, r.Code)
	}
	if got := strings.Join(order, ","); got != "JP,DE,US,FR" {
		t.Fatalf("order = %s", got)
	}
	if results[0].PingMS != 20 || results[3].Error != "not allowed for this account" {
		t.Fatalf("unexpected results %+v", results)
	}

	// JP is marked locked in the server list and is still the fastest.
	best, ok := FastestLocation(results)
	if !ok || best.Code != "JP" {
		t.Fatalf("fastest = %+v, %v", best, ok)
	}
}

func TestFastestLocationSkipsFailedLocations(t *testing.T) {
	results := []LocationPing{
		{Code: "JP", Error: "not allowed for this account"},
		{Code: "GB", Error: "timeout"},
		{Code: "DE", PingMS: 90},
	}
	if best, ok := FastestLocation(results); !ok || best.Code != "DE" {
		t.Fatalf("fastest = %+v, %v", best, ok)
	}
	if _, ok := FastestLocation(results[:2]); ok {
		t.Fatal("expected no pick when every location failed")
	}
}

func TestPingErrorText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&proxyConnectHTTPError{statusCode: 407}, "proxy pass rejected"},
		{&proxyConnectHTTPError{statusCode: 502}, "refused (HTTP 502)"},
		{&net.DNSError{Err: "no such host"}, "DNS lookup failed"},
		{context.DeadlineExceeded, "timeout"},
		{errors.New("boom"), "unreachable"},
	} {
		if got := pingErrorText(tc.err); got != tc.want {
			t.Errorf("pingErrorText(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// fakePingSession answers CONNECT after a delay, or refuses it.
type fakePingSession struct {
	delay   time.Duration
	refuse  bool
	opens   atomic.Int32
	closed  atomic.Bool
	targets []string
}

func (s *fakePingSession) OpenTunnel(authority string) (net.Conn, error) {
	s.opens.Add(1)
	s.targets = append(s.targets, authority)
	if s.refuse {
		return nil, &proxyConnectHTTPError{statusCode: http.StatusForbidden}
	}
	time.Sleep(s.delay)
	a, b := net.Pipe()
	_ = b.Close()
	return a, nil
}

func (s *fakePingSession) Close() error { s.closed.Store(true); return nil }

func TestTunnelMeterTimesConnectThroughEachLocation(t *testing.T) {
	sessions := map[string]*fakePingSession{
		"us.example": {delay: 30 * time.Millisecond},
		"jp.example": {refuse: true},
	}
	var tokens []string
	old := pingSessionDialer
	pingSessionDialer = func(u *url.URL, token string, _ time.Duration) (proxySession, error) {
		tokens = append(tokens, token)
		s, ok := sessions[u.Hostname()]
		if !ok {
			return nil, errors.New("dial failed")
		}
		return s, nil
	}
	defer func() { pingSessionDialer = old }()

	meter := TunnelMeter("pass-token")
	d, err := meter(context.Background(), Candidate{Addr: "us.example:2499"})
	if err != nil || d < 30*time.Millisecond {
		t.Fatalf("us: %v, %v", d, err)
	}
	us := sessions["us.example"]
	if us.opens.Load() != pingAttempts || !us.closed.Load() || us.targets[0] != PingTarget {
		t.Fatalf("us session: opens=%d closed=%v targets=%v", us.opens.Load(), us.closed.Load(), us.targets)
	}
	if _, err := meter(context.Background(), Candidate{Addr: "jp.example:2499"}); pingErrorText(err) != "not allowed for this account" {
		t.Fatalf("jp: %v", err)
	}
	if sessions["jp.example"].opens.Load() != 1 {
		t.Fatal("a refused CONNECT must not be retried")
	}
	if _, err := meter(context.Background(), Candidate{Addr: "fr.example:2499"}); err == nil {
		t.Fatal("expected a dial error")
	}
	for _, tok := range tokens {
		if tok != "pass-token" {
			t.Fatalf("session got token %q", tok)
		}
	}
}

func TestPingSubsetIsMergedIntoEarlierResults(t *testing.T) {
	targets := PingTargets(pingTestCountries())
	subset := FilterTargets(targets, []string{"de", " JP "})
	if len(subset) != 2 || subset[0].CountryCode != "DE" || subset[1].CountryCode != "JP" {
		t.Fatalf("subset = %+v", subset)
	}
	if len(FilterTargets(targets, nil)) != len(targets) {
		t.Fatal("no codes means every location")
	}
	old := []LocationPing{{Code: "US", PingMS: 90}, {Code: "DE", PingMS: 300}, {Code: "FR", Error: "timeout"}}
	merged := MergePings(old, []LocationPing{{Code: "DE", PingMS: 40}, {Code: "JP", Error: "not allowed for this account"}})
	var got []string
	for _, p := range merged {
		got = append(got, fmt.Sprintf("%s:%d", p.Code, p.PingMS))
	}
	if strings.Join(got, ",") != "DE:40,US:90,JP:0,FR:0" {
		t.Fatalf("merged = %v", got)
	}
}

// Every location is measured once, a location without an answer too, and
// each is reported as its result comes.
func TestPingLocationsMeasuresOnceAndReportsEachLocation(t *testing.T) {
	var tries sync.Map
	meter := func(_ context.Context, target Candidate) (time.Duration, error) {
		n, _ := tries.LoadOrStore(target.CountryCode, new(int))
		*n.(*int)++
		switch target.CountryCode {
		case "US":
			return 50 * time.Millisecond, nil
		case "JP":
			return 0, &proxyConnectHTTPError{statusCode: http.StatusForbidden}
		}
		return 0, context.DeadlineExceeded // DE and FR: no answer
	}
	var (
		mu      sync.Mutex
		updates []string
	)
	results := PingLocations(context.Background(), PingTargets(pingTestCountries()), PingRun{
		Meter: meter,
		OnUpdate: func(p LocationPing) {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, fmt.Sprintf("%s:%d:%s", p.Code, p.PingMS, p.Error))
		},
	})
	for _, code := range []string{"US", "DE", "JP", "FR"} {
		n, ok := tries.Load(code)
		if !ok || *n.(*int) != 1 {
			t.Fatalf("%s measured %v times, want once", code, n)
		}
	}
	var got []string
	for _, r := range results {
		got = append(got, fmt.Sprintf("%s:%d:%s", r.Code, r.PingMS, r.Error))
	}
	if want := "US:50:,FR:0:timeout,DE:0:timeout,JP:0:not allowed for this account"; strings.Join(got, ",") != want {
		t.Fatalf("results = %v, want %s", got, want)
	}
	sort.Strings(updates)
	if want := "DE:0:timeout,FR:0:timeout,JP:0:not allowed for this account,US:50:"; strings.Join(updates, ",") != want {
		t.Fatalf("updates = %v, want %s", updates, want)
	}
}

// The first locations given are the first measured.
func TestPingLocationsStartsInTheGivenOrder(t *testing.T) {
	targets := PingTargets(pingTestCountries())
	var (
		mu    sync.Mutex
		order []string
	)
	meter := func(_ context.Context, target Candidate) (time.Duration, error) {
		mu.Lock()
		order = append(order, target.CountryCode)
		mu.Unlock()
		return time.Millisecond, nil
	}
	old := pingParallelForTest(1)
	defer pingParallelForTest(old)
	PingLocations(context.Background(), targets, PingRun{Meter: meter})
	if got := strings.Join(order, ","); got != "US,DE,JP,FR" {
		t.Fatalf("order = %s", got)
	}
}

// pingParallelForTest sets pingParallel and returns the old value.
func pingParallelForTest(n int) int {
	old := pingParallel
	pingParallel = n
	return old
}
