package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/armanlife/arman-vpn-client/core/internal/evasion"
)

// testAddresses is a profile with other addresses of the servers, from the
// documentation range: three spares, a search of .42 to .254 (32 at most,
// keeping 8), a last resort, and two servers with other addresses.
type testAddresses struct{}

func (testAddresses) Name() string                        { return "test" }
func (testAddresses) Authorize(string, string) url.Values { return nil }
func (testAddresses) Spares() []netip.Addr                { return addrs("192.0.2.141", "192.0.2.200", "192.0.2.50") }
func (testAddresses) LastResort() []netip.Addr            { return addrs("192.0.2.1", "192.0.2.7") }
func (testAddresses) SpareCandidates() ([]netip.Addr, int, int) {
	var out []netip.Addr
	for a := netip.MustParseAddr("192.0.2.42"); a.Compare(netip.MustParseAddr("192.0.2.254")) <= 0; a = a.Next() {
		out = append(out, a)
	}
	return out, 32, 8
}
func (testAddresses) OtherAddresses(host string) []netip.Addr {
	switch host {
	case "city1.m1.fastly-masque.net":
		return addrs("192.0.2.4")
	case "city2.m1.fastly-masque.net":
		return addrs("192.0.2.5", "192.0.2.13")
	}
	return nil
}

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

// resetEdges forgets the edges and the recommended servers, and puts the
// test profile in place, for this test and after it.
func resetEdges(t *testing.T) {
	t.Helper()
	evasion.Register(testAddresses{})
	t.Cleanup(func() { evasion.Register(nil) })
	reset := func() {
		edge.Store(nil)
		spareEdges.Store(nil)
		ForgetEdgeFailures()
		recommendedMu.Lock()
		recommendedHosts = map[string]bool{}
		recommendedMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

const (
	recommendedServer = "p.m1.fastly-masque.net:2499"
	cityServer        = "muc139.m1.fastly-masque.net:2499"
)

func noteServers() {
	noteRecommended(&url.URL{Host: recommendedServer}, true)
	noteRecommended(&url.URL{Host: cityServer}, false)
}

// The custom edge is used for the recommended location's servers only: an
// IP as it is, a range inside it (never its first or last); a chosen
// city's server keeps its own address, since the address decides the exit.
func TestCustomEdgeIsForTheRecommendedLocation(t *testing.T) {
	resetEdges(t)
	noteServers()
	if err := SetEdge("192.0.2.141"); err != nil {
		t.Fatal(err)
	}
	if got, _ := edgeAddress(recommendedServer); got != "192.0.2.141:2499" {
		t.Fatalf("recommended: %s", got)
	}
	if got, ok := edgeAddress(cityServer); ok || got != cityServer {
		t.Fatalf("a city's server: %s", got)
	}
	if err := SetEdge("192.0.2.0/24"); err != nil {
		t.Fatal(err)
	}
	r := netip.MustParsePrefix("192.0.2.0/24")
	for i := 0; i < 500; i++ {
		a, _ := customEdge("p.m1.fastly-masque.net")
		if !r.Contains(a) || a == netip.MustParseAddr("192.0.2.0") || a == netip.MustParseAddr("192.0.2.255") {
			t.Fatalf("range gave %s", a)
		}
	}
	if err := SetEdge("not an ip"); err == nil {
		t.Fatal("a wrong value is accepted")
	}
	if err := SetEdge(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := edgeAddress(recommendedServer); ok {
		t.Fatal("off, yet an edge")
	}
}

// The spare edges are tried when the own address fails; the one that
// answered goes first the next time, and the own address answering again
// puts it back first. A city's server has none.
func TestSpareEdges(t *testing.T) {
	resetEdges(t)
	noteServers()
	SetSpareEdges([]string{"192.0.2.141", "192.0.2.200", "not an ip"})
	if got := spareEdgesFor("muc139.m1.fastly-masque.net"); got != nil {
		t.Fatalf("a city's server has spares: %v", got)
	}

	down := map[string]bool{"": true, "192.0.2.141:2499": true}
	var mu sync.Mutex
	var tried []string
	attempt := func(_ context.Context, at string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		tried = append(tried, at)
		if down[at] {
			return "", errors.New("timeout")
		}
		return at, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := withSpares(ctx, recommendedServer, attempt, nil)
	if err != nil || got != "192.0.2.200:2499" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !reflect.DeepEqual(tried, []string{"", "192.0.2.141:2499", "192.0.2.200:2499"}) {
		t.Fatalf("tried %v", tried)
	}

	// The spare that answered goes first now.
	tried = nil
	if got, _ := withSpares(ctx, recommendedServer, attempt, nil); got != "192.0.2.200:2499" || len(tried) != 1 {
		t.Fatalf("second: got %q after %v", got, tried)
	}

	// The own address goes after the spares for a while: the spare is
	// down, the next spare answers before the own address is tried.
	down = map[string]bool{"": true, "192.0.2.200:2499": true}
	tried = nil
	if got, _ := withSpares(ctx, recommendedServer, attempt, nil); got != "192.0.2.141:2499" || tried[0] != "192.0.2.200:2499" {
		t.Fatalf("third: got %q after %v", got, tried)
	}

	// On another network the own address is first again, and answering
	// puts it back first.
	ForgetEdgeFailures()
	down = map[string]bool{}
	tried = nil
	if got, _ := withSpares(ctx, recommendedServer, attempt, nil); got != "" || len(tried) != 1 {
		t.Fatalf("fourth: got %q after %v", got, tried)
	}
	if _, ok := workingSpare("p.m1.fastly-masque.net"); ok {
		t.Fatal("the spare is still first")
	}

	// A city's server: its own address alone.
	tried = nil
	down = map[string]bool{"": true}
	if _, err := withSpares(ctx, cityServer, attempt, nil); err == nil || len(tried) != 1 {
		t.Fatalf("a city's server tried %v", tried)
	}

	// With a custom edge, no spares.
	_ = SetEdge("192.0.2.141")
	if got := spareEdgesFor("p.m1.fastly-masque.net"); got != nil {
		t.Fatalf("spares beside a custom edge: %v", got)
	}
}

// An address that hangs does not hold the others up: the next one starts
// after spareStagger beside it, the first to answer wins, and what the
// others bring later is discarded.
func TestSpareEdgesRace(t *testing.T) {
	resetEdges(t)
	stagger := spareStagger
	spareStagger = 100 * time.Millisecond
	t.Cleanup(func() { spareStagger = stagger })
	noteServers()
	SetSpareEdges([]string{"192.0.2.141"})
	release := make(chan struct{})
	discarded := make(chan string, 4)
	attempt := func(ctx context.Context, at string) (string, error) {
		if at == "" {
			<-release // the own address answers, but late
			return "own", nil
		}
		return at, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	began := time.Now()
	got, err := withSpares(ctx, recommendedServer, attempt, func(s string) { discarded <- s })
	if err != nil || got != "192.0.2.141:2499" {
		t.Fatalf("got %q, %v", got, err)
	}
	if d := time.Since(began); d < spareStagger || d > 5*spareStagger {
		t.Fatalf("the spare started after %s", d)
	}
	close(release)
	select {
	case s := <-discarded:
		if s != "own" {
			t.Fatalf("discarded %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the late answer was not discarded")
	}
}

// The recommended location tries a few of its spares, then the United
// States.
func TestLastResortEdges(t *testing.T) {
	resetEdges(t)
	noteServers()
	SetSpareEdges([]string{"192.0.2.141", "192.0.2.200", "192.0.2.60", "192.0.2.55"})
	var names []string
	for _, a := range spareEdgesFor("p.m1.fastly-masque.net") {
		names = append(names, a.String())
	}
	want := []string{"192.0.2.141", "192.0.2.200", "192.0.2.60", "192.0.2.1", "192.0.2.7"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("spares %v", names)
	}
}

// Without spares of its own, the profile's, then its last resort; the
// built-in profile has none, so a server keeps its own address alone.
func TestProfileSpareEdges(t *testing.T) {
	resetEdges(t)
	noteServers()
	SetSpareEdges(nil)
	got := spareEdgesFor("p.m1.fastly-masque.net")
	if len(got) != 5 || got[0].String() != "192.0.2.141" || got[4].String() != "192.0.2.7" {
		t.Fatalf("the profile's: %v", got)
	}
	evasion.Register(nil)
	if got := spareEdgesFor("p.m1.fastly-masque.net"); got != nil {
		t.Fatalf("the built-in profile has spares: %v", got)
	}
	if got := spareEdgesFor("city1.m1.fastly-masque.net"); got != nil {
		t.Fatalf("the built-in profile has other addresses: %v", got)
	}
}

// A scan tries the candidates but the ones left out.
func TestEdgeCandidates(t *testing.T) {
	got := EdgeCandidates(addrs("10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"), addrs("10.0.0.3"))
	var names []string
	for _, a := range got {
		names = append(names, a.String())
	}
	if !reflect.DeepEqual(names, []string{"10.0.0.1", "10.0.0.2", "10.0.0.4"}) {
		t.Fatalf("candidates %v", names)
	}
}

// A city with more than one address has the others as spares; the one that
// answered is remembered for that server alone, and no scan is asked for.
func TestCitySpareEdges(t *testing.T) {
	resetEdges(t)
	noteServers()
	scans := make(chan bool, 4)
	OnSpareEdgesNeeded(func(exhausted bool) { scans <- exhausted })
	t.Cleanup(func() { OnSpareEdgesNeeded(nil) })
	us := "city1.m1.fastly-masque.net:2499"
	if got := spareEdgesFor("city1.m1.fastly-masque.net"); len(got) != 1 || got[0].String() != "192.0.2.4" {
		t.Fatalf("the first city's spares %v", got)
	}
	if got := spareEdgesFor("city2.m1.fastly-masque.net"); len(got) != 2 {
		t.Fatalf("the second city's spares %v", got)
	}
	attempt := func(_ context.Context, at string) (string, error) {
		if at == "" {
			return "", errors.New("timeout")
		}
		return at, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if got, err := withSpares(ctx, us, attempt, nil); err != nil || got != "192.0.2.4:2499" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, ok := workingSpare("city1.m1.fastly-masque.net"); !ok {
		t.Fatal("not remembered")
	}
	if got := spareEdgesFor("p.m1.fastly-masque.net"); got[0].String() == "192.0.2.4" {
		t.Fatal("a city's address offered to the recommended location")
	}
	select {
	case <-scans:
		t.Fatal("a city's failure asked for a scan")
	case <-time.After(50 * time.Millisecond):
	}
}

// The scan connects to each address with the server's name and checks its
// certificate: the address that serves it answers, with a time; one where
// nothing listens does not; every result is reported.
func TestScanEdges(t *testing.T) {
	fixture := httptest.NewUnstartedServer(nil)
	fixture.StartTLS()
	t.Cleanup(fixture.Close)
	pool := x509.NewCertPool()
	pool.AddCert(fixture.Certificate())
	previous := upstreamRoots.Load()
	upstreamRoots.Store(pool)
	t.Cleanup(func() { upstreamRoots.Store(previous) })
	config := fixture.TLS.Clone()
	config.NextProtos = []string{alpnH2}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = conn.(*tls.Conn).Handshake()
				_ = conn.Close()
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	// A free port on the second address: nothing listens there.
	candidates := []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.2")}
	var reported []EdgeResult
	answered := ScanEdges(context.Background(), net.JoinHostPort("example.com", port), candidates, 0, func(r EdgeResult) {
		reported = append(reported, r)
	})
	if len(answered) != 1 || answered[0].IP != "127.0.0.1" || answered[0].MS <= 0 {
		t.Fatalf("answered %+v", answered)
	}
	if len(reported) != 2 {
		t.Fatalf("reported %+v", reported)
	}

	// Enough answered: the scan stops and keeps that many.
	many := []netip.Addr{}
	for i := 0; i < 40; i++ {
		many = append(many, netip.MustParseAddr("127.0.0.1"))
	}
	if got := ScanEdges(context.Background(), net.JoinHostPort("example.com", port), many, 3, nil); len(got) != 3 {
		t.Fatalf("want 3, kept %d", len(got))
	}
}

// A sample is that many different addresses of the candidates, at random.
func TestEdgeSample(t *testing.T) {
	all, n, _ := testAddresses{}.SpareCandidates()
	sample := EdgeSample(all, n)
	seen := map[netip.Addr]bool{}
	for _, a := range sample {
		if !slices.Contains(all, a) || seen[a] {
			t.Fatalf("sample %v", sample)
		}
		seen[a] = true
	}
	if len(sample) != n || len(EdgeSample(all[:5], n)) != 5 {
		t.Fatalf("sizes %d", len(sample))
	}
}
