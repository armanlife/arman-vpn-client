package service

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/armanlife/arman-vpn-client/core/internal/evasion"
	"github.com/armanlife/arman-vpn-client/core/internal/logx"
	"github.com/armanlife/arman-vpn-client/core/internal/proxy"
	"github.com/armanlife/arman-vpn-client/core/internal/resolver"
	"github.com/armanlife/arman-vpn-client/core/internal/securefile"
)

// The spare edges of this device: the addresses of Mozilla's recommended
// location that answered its last scan (proxy.ScanEdges), the fastest
// first, kept in edges.json and given to the proxy as its spare addresses.
// The scan runs by itself, in the background, when the recommended server's
// own address does not answer (spareEdgesNeeded); nothing asks for it.
// Before any scan the profile's are used (evasion.ServerAddresses); a build
// whose profile has no candidates does not scan.

// savedEdges is edges.json, and the answer of GET /api/edges.
type savedEdges struct {
	ScannedAt time.Time          `json:"scanned_at,omitzero"`
	Edges     []proxy.EdgeResult `json:"edges"`
}

// edgeScan lets one background scan run at a time, and spaces them.
type edgeScan struct {
	mu      sync.Mutex
	running bool
	lastAt  time.Time
}

// edgeScanSpacing is the least time between two background scans.
const edgeScanSpacing = 10 * time.Minute

// loadEdges gives the saved spare edges to the proxy.
func loadEdges(path string) {
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return
	case err != nil:
		logx.Debug("reading the spare edges failed path=%s err=%s", path, logx.Err(err))
		return
	}
	var saved savedEdges
	if json.Unmarshal(raw, &saved) != nil {
		return
	}
	proxy.SetSpareEdges(edgeIPs(saved.Edges))
}

func edgeIPs(edges []proxy.EdgeResult) []string {
	ips := make([]string, 0, len(edges))
	for _, e := range edges {
		ips = append(ips, e.IP)
	}
	return ips
}

// edges returns the saved scan; an empty one before any.
func (d *Daemon) edges() savedEdges {
	saved := savedEdges{Edges: []proxy.EdgeResult{}}
	if raw, err := os.ReadFile(d.paths.Edges); err == nil {
		_ = json.Unmarshal(raw, &saved)
		if saved.Edges == nil {
			saved.Edges = []proxy.EdgeResult{}
		}
	}
	return saved
}

// spareEdgesNeeded is proxy.OnSpareEdgesNeeded: the recommended server's
// own address did not answer. It scans in the background when this device
// knows no spare of its own yet, or when those failed too (exhausted); at
// most every edgeScanSpacing, one at a time.
func (d *Daemon) spareEdgesNeeded(exhausted bool) {
	if !exhausted && len(d.edges().Edges) > 0 {
		return
	}
	d.edgeScan.mu.Lock()
	if d.edgeScan.running || time.Since(d.edgeScan.lastAt) < edgeScanSpacing {
		d.edgeScan.mu.Unlock()
		return
	}
	d.edgeScan.running, d.edgeScan.lastAt = true, time.Now()
	d.edgeScan.mu.Unlock()
	defer func() {
		d.edgeScan.mu.Lock()
		d.edgeScan.running = false
		d.edgeScan.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.scanEdges(ctx); err != nil {
		logx.Debug("scanning for spare edges failed err=%s", logx.Err(err))
	}
}

// scanEdges tries a random sample of the profile's candidates for the
// recommended location's server until enough answered, and saves those as
// this device's spare edges. When none answered, the saved ones stay.
func (d *Daemon) scanEdges(ctx context.Context) error {
	addrs := evasion.ServerAddresses()
	if len(addrs.Candidates) == 0 || addrs.Sample <= 0 {
		return nil
	}
	d.mu.Lock()
	countries := d.countries
	d.mu.Unlock()
	if len(countries) == 0 {
		countries = d.refreshLocations(d.config())
	}
	var server string
	var others []string
	for _, c := range proxy.ConnectCandidates(countries) {
		u, err := proxy.NormalizeURL(c.Addr)
		if err != nil {
			continue
		}
		if c.IsRecommended() && server == "" {
			server = u.Host
		}
		others = append(others, u.Hostname())
	}
	if server == "" {
		return errors.New("no server list")
	}
	// Every server's own address is left out: a city's decides where the
	// traffic leaves, and the recommended server's is the one that failed.
	excluded := resolveAll(ctx, others)
	sample := proxy.EdgeSample(proxy.EdgeCandidates(addrs.Candidates, excluded), addrs.Sample)
	started := time.Now()
	answered := proxy.ScanEdges(ctx, server, sample, addrs.Want, nil)
	if len(answered) == 0 {
		logx.Warn("spare edge scan: no address answered tried=%d took=%s", len(sample), time.Since(started).Round(100*time.Millisecond))
		return nil
	}
	saved := savedEdges{ScannedAt: time.Now(), Edges: answered}
	raw, err := json.Marshal(saved)
	if err == nil {
		err = securefile.WritePrivate(d.paths.Edges, raw)
	}
	if err != nil {
		logx.Warn("saving the spare edges failed path=%s err=%s", d.paths.Edges, logx.Err(err))
	}
	proxy.SetSpareEdges(edgeIPs(answered))
	logx.Info("spare edge scan: the recommended server's own address did not answer; found=%d tried=%d fastest=%s ms=%d took=%s",
		len(answered), len(sample), answered[0].IP, answered[0].MS, time.Since(started).Round(100*time.Millisecond))
	return nil
}

// resolveAll returns the addresses the names resolve to, those it could
// find within a few seconds.
func resolveAll(ctx context.Context, names []string) []netip.Addr {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var (
		mu  sync.Mutex
		out []netip.Addr
		wg  sync.WaitGroup
	)
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.ToLower(name)
		if seen[name] {
			continue
		}
		seen[name] = true
		if a, err := netip.ParseAddr(name); err == nil {
			out = append(out, a)
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			addrs, err := resolver.LookupNetIPBoth(ctx, name)
			if err != nil {
				return
			}
			mu.Lock()
			out = append(out, addrs...)
			mu.Unlock()
		}(name)
	}
	wg.Wait()
	return out
}

// handleEdges answers the saved scan, for a look at what the background
// scan found: {scanned_at, edges: [{ip, ms}]}, the fastest first; edges is
// empty before any scan.
func (s *dashboardServer) handleEdges(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.d.edges())
}
