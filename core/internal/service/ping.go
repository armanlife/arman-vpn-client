package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/armanlife/arman-vpn-client/core/internal/logx"
	"github.com/armanlife/arman-vpn-client/core/internal/proxy"
)

const (
	// pingReuseAge is how long a measurement is reused when the "fastest"
	// location is chosen at an engine start.
	pingReuseAge = 30 * time.Minute
)

// errPingNeedsProxy reports a latency measurement without a running proxy.
var errPingNeedsProxy = errors.New("start the proxy first: latency is measured through the tunnel with the active account's proxy pass")

// PingProgress follows a measurement while it runs (the streamed
// POST /api/locations/ping). The zero value follows nothing.
type PingProgress struct {
	// Start names the locations about to be measured, in the order they
	// are taken; the others keep their last figure.
	Start func(codes []string)
	// Update hears each location as its result comes.
	Update func(proxy.LocationPing)
}

// PingAll measures the current server list through the running proxy's
// pass and keeps the result for the dashboard and the terminal screen.
// codes limits the measurement to those country codes; empty measures
// every location. Without a running proxy (the phone app turned it off)
// it measures with a pass of its own (offPass).
//
// fresh measures every location, as a person asking for it expects; without
// it a location that was slow lately keeps its last figure (slowPingSkip).
func (d *Daemon) PingAll(ctx context.Context, codes []string, fresh bool, progress PingProgress) ([]proxy.LocationPing, time.Time, error) {
	engine := d.currentEngine()
	if engine == nil {
		token, err := d.offPass(ctx)
		if err != nil {
			return nil, time.Time{}, err
		}
		results, at := d.measureLocations(ctx, token, codes, fresh, progress)
		return results, at, nil
	}
	// A real-delay test wakes a sleeping proxy and keeps it awake.
	controller, release, err := d.useController(engine, wakeForPing)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer release()
	token := controller.CurrentToken()
	if token == "" {
		return nil, time.Time{}, errPingNeedsProxy
	}
	results, at := d.measureLocations(ctx, token, codes, fresh, progress)
	return results, at, nil
}

// offPassMargin is how long before its expiry a kept pass is replaced.
const offPassMargin = 2 * time.Minute

// offPass returns a pass for measurements while the proxy is off: the kept
// one while it is valid, else a new one from the account pool.
func (d *Daemon) offPass(ctx context.Context) (string, error) {
	d.pingMu.Lock()
	defer d.pingMu.Unlock()
	if p := d.pingPass; p != nil && time.Until(p.ExpiresAt()) > offPassMargin {
		return p.RawToken, nil
	}
	if d.mgr.Len() == 0 {
		return "", errNoAccounts
	}
	ctx, cancel := context.WithTimeout(ctx, proxy.PassRenewTimeout)
	defer cancel()
	_, pass, err := d.pool.AcquirePass(ctx, d.config().Network.GuardianEndpoint)
	if err != nil {
		return "", err
	}
	d.pingPass = pass
	return pass.RawToken, nil
}

// pingLocations measures targets through the tunnel with token and reports
// each to update (may be nil); a test measures without a tunnel.
var pingLocations = func(ctx context.Context, targets []proxy.Candidate, token string, update func(proxy.LocationPing)) []proxy.LocationPing {
	return proxy.PingLocations(ctx, targets, proxy.PingRun{Meter: proxy.TunnelMeter(token), OnUpdate: update})
}

const (
	// A location whose delay came out above slowPingMS, or that did not
	// answer, is left out of the measurements for slowPingSkip. It is no
	// candidate for the fastest, and testing it again costs the most time,
	// waiting for its timeout. Its last figure stays on the list. The skip
	// is short and belongs to one network: on another one, all are tested.
	slowPingMS   = 210
	slowPingSkip = 10 * time.Minute
)

// measureLocations measures the given locations (all when codes is
// empty) and merges the results into the kept measurement, so testing one
// tab keeps the other locations' earlier values. The locations that
// answered fastest last time are taken first, so a list that shows results
// as they come fills from the top.
func (d *Daemon) measureLocations(ctx context.Context, token string, codes []string, fresh bool, progress PingProgress) ([]proxy.LocationPing, time.Time) {
	d.pingMu.Lock()
	defer d.pingMu.Unlock()
	d.mu.Lock()
	countries := d.countries
	d.mu.Unlock()
	if len(countries) == 0 {
		countries = d.refreshLocations(d.config())
	}
	targets := proxy.FilterTargets(proxy.PingTargets(countries), codes)
	now := time.Now()
	if network, _ := networkNow(); network != d.pingSkipNetwork {
		d.pingSkip, d.pingSkipNetwork = nil, network
	}
	measure := make([]proxy.Candidate, 0, len(targets))
	for _, target := range targets {
		if until, ok := d.pingSkip[strings.ToUpper(target.CountryCode)]; !fresh && ok && now.Before(until) {
			continue
		}
		measure = append(measure, target)
	}
	d.mu.Lock()
	lastMS := make(map[string]int64, len(d.pings))
	for _, p := range d.pings {
		if p.OK() {
			lastMS[strings.ToUpper(p.Code)] = p.PingMS
		}
	}
	d.mu.Unlock()
	sort.SliceStable(measure, func(i, j int) bool {
		a, aok := lastMS[strings.ToUpper(measure[i].CountryCode)]
		b, bok := lastMS[strings.ToUpper(measure[j].CountryCode)]
		if aok != bok {
			return aok
		}
		return aok && a < b
	})
	logx.Debug("testing the real delay through the tunnel locations=%d skipped_slow=%d target=%s", len(measure), len(targets)-len(measure), proxy.PingTarget)
	if progress.Start != nil {
		order := make([]string, len(measure))
		for i, target := range measure {
			order[i] = strings.ToUpper(target.CountryCode)
		}
		progress.Start(order)
	}
	results := pingLocations(ctx, measure, token, progress.Update)
	now = time.Now()
	for _, r := range results {
		code := strings.ToUpper(r.Code)
		if !r.OK() || r.PingMS > slowPingMS {
			if d.pingSkip == nil {
				d.pingSkip = make(map[string]time.Time)
			}
			d.pingSkip[code] = now.Add(slowPingSkip)
		} else {
			delete(d.pingSkip, code)
		}
	}
	d.mu.Lock()
	merged := proxy.MergePings(d.pings, results)
	d.pings, d.pingedAt = merged, now
	d.mu.Unlock()
	if len(measure) < len(targets) {
		// The answer covers every location asked for: the skipped ones with
		// their last figure, so the list does not lose them.
		asked := make(map[string]bool, len(targets))
		for _, target := range targets {
			asked[strings.ToUpper(target.CountryCode)] = true
		}
		measured := make(map[string]bool, len(results))
		for _, r := range results {
			measured[strings.ToUpper(r.Code)] = true
		}
		results = results[:0:0]
		for _, p := range merged {
			code := strings.ToUpper(p.Code)
			if asked[code] {
				p.Cached = !measured[code]
				results = append(results, p)
			}
		}
	}
	reachable := 0
	for _, r := range results {
		if r.OK() {
			reachable++
		}
	}
	if reachable > 0 {
		logx.Info("location latency measured locations=%d reachable=%d fastest=%s ping_ms=%d", len(results), reachable, results[0].Code, results[0].PingMS)
	} else {
		logx.Warn("location latency measured; no location answered locations=%d", len(results))
	}
	return results, now
}

// lastPings returns the most recent measurement, if any.
func (d *Daemon) lastPings() ([]proxy.LocationPing, time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]proxy.LocationPing(nil), d.pings...), d.pingedAt
}

// resolveFastestCountry returns the country code to use for
// proxy.country = "fastest": from a recent measurement, or measured now
// with token. It returns "" (the recommended location) when nothing could
// be measured.
func (d *Daemon) resolveFastestCountry(ctx context.Context, token string) string {
	results, at := d.lastPings()
	if time.Since(at) > pingReuseAge {
		results, _ = d.measureLocations(ctx, token, nil, false, PingProgress{})
	}
	best, ok := proxy.FastestLocation(results)
	if !ok {
		logx.Warn("no location answered the latency check; using the recommended location")
		return ""
	}
	logx.Info("fastest location selected country_code=%s ping_ms=%d", best.Code, best.PingMS)
	return best.Code
}
