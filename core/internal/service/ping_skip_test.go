package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/armanlife/arman-vpn-client/core/internal/proxy"
	"github.com/armanlife/arman-vpn-client/core/internal/serverlist"
)

func pingCountry(code string) serverlist.Country {
	return serverlist.Country{Name: code, Code: code, Cities: []serverlist.City{{
		Name: code, Code: code, Servers: []serverlist.Server{{Hostname: code + ".example", Port: 443}},
	}}}
}

// A location that came out slow, or did not answer, is left out of the next
// measurements for a while, keeps its last figure on the list, and is
// measured again on another network.
func TestSlowLocationsAreSkippedForAWhile(t *testing.T) {
	network := "home"
	oldNetwork, oldPing := networkNow, pingLocations
	t.Cleanup(func() { networkNow, pingLocations = oldNetwork, oldPing })
	networkNow = func() (string, bool) { return network, true }

	figures := map[string]int64{"NL": 80, "US": 300, "JP": 0} // JP answers nothing
	var measured []string
	pingLocations = func(_ context.Context, targets []proxy.Candidate, _ string, _ func(proxy.LocationPing)) []proxy.LocationPing {
		measured = measured[:0]
		var out []proxy.LocationPing
		for _, c := range targets {
			measured = append(measured, c.CountryCode)
			p := proxy.LocationPing{Code: c.CountryCode, Name: c.CountryName}
			if ms := figures[c.CountryCode]; ms > 0 {
				p.PingMS = ms
			} else {
				p.Error = "timeout"
			}
			out = append(out, p)
		}
		return out
	}

	d := &Daemon{countries: []serverlist.Country{pingCountry("NL"), pingCountry("US"), pingCountry("JP")}}
	ctx := context.Background()

	if _, _ = d.measureLocations(ctx, "token", nil, false, PingProgress{}); len(measured) != 3 {
		t.Fatalf("first measurement covered %v; want all three", measured)
	}

	results, _ := d.measureLocations(ctx, "token", nil, false, PingProgress{})
	if len(measured) != 1 || measured[0] != "NL" {
		t.Fatalf("second measurement covered %v; want only the quick NL", measured)
	}
	if len(results) != 3 {
		t.Fatalf("the answer lists %d locations; want all three, the skipped with their last figure", len(results))
	}
	for _, r := range results {
		if r.Code == "US" && (r.PingMS != 300 || !r.Cached) {
			t.Fatalf("US: %d ms cached=%v; want its last figure, 300, marked cached", r.PingMS, r.Cached)
		}
		if r.Code == "NL" && r.Cached {
			t.Fatal("NL was measured now but is marked cached")
		}
	}

	// A person pressing the button gets every location measured.
	if _, _ = d.measureLocations(ctx, "token", nil, true, PingProgress{}); len(measured) != 3 {
		t.Fatalf("a fresh measurement covered %v; want all three", measured)
	}

	// The skip runs out.
	for code := range d.pingSkip {
		d.pingSkip[code] = time.Now().Add(-time.Second)
	}
	if _, _ = d.measureLocations(ctx, "token", nil, false, PingProgress{}); len(measured) != 3 {
		t.Fatalf("after the skip ran out, measured %v; want all three", measured)
	}

	// Another network measures everything again at once.
	_, _ = d.measureLocations(ctx, "token", nil, false, PingProgress{}) // US and JP skipped again
	network = "office"
	if _, _ = d.measureLocations(ctx, "token", nil, false, PingProgress{}); len(measured) != 3 {
		t.Fatalf("on another network, measured %v; want all three", measured)
	}
}

// The locations that answered fastest last time are measured first, and a
// follower hears the order and each location as it comes.
func TestMeasurementStartsWithLastFastestAndReportsProgress(t *testing.T) {
	oldNetwork, oldPing := networkNow, pingLocations
	t.Cleanup(func() { networkNow, pingLocations = oldNetwork, oldPing })
	networkNow = func() (string, bool) { return "home", true }
	pingLocations = func(_ context.Context, targets []proxy.Candidate, _ string, update func(proxy.LocationPing)) []proxy.LocationPing {
		var out []proxy.LocationPing
		for _, c := range targets {
			p := proxy.LocationPing{Code: c.CountryCode, PingMS: 50}
			update(p)
			out = append(out, p)
		}
		return out
	}
	d := &Daemon{
		countries: []serverlist.Country{pingCountry("NL"), pingCountry("US"), pingCountry("JP"), pingCountry("DE")},
		pings:     []proxy.LocationPing{{Code: "JP", PingMS: 40}, {Code: "US", PingMS: 90}, {Code: "NL", Error: "timeout"}},
	}
	var started []string
	var updated []string
	d.measureLocations(context.Background(), "token", nil, true, PingProgress{
		Start:  func(codes []string) { started = codes },
		Update: func(p proxy.LocationPing) { updated = append(updated, p.Code) },
	})
	if got := strings.Join(started, ","); got != "JP,US,NL,DE" {
		t.Fatalf("order = %s; want the last fastest first, then the server list's order", got)
	}
	if got := strings.Join(updated, ","); got != "JP,US,NL,DE" {
		t.Fatalf("updates = %s", got)
	}
}
