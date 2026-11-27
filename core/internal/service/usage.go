package service

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/armanlife/arman-vpn-client/core/internal/logx"
	"github.com/armanlife/arman-vpn-client/core/internal/proxy"
	"github.com/armanlife/arman-vpn-client/core/internal/securefile"
)

// Data usage: what the proxy's clients sent and received each day, through
// Mozilla and directly (routing rules). The engine's counters
// (proxy.Traffic) are sampled every few seconds and what they gained is
// added to the day of the sample, in local time. Every day is kept in
// usage.json until the usage is reset (a day is a few dozen bytes). Each
// account's own use is Mozilla's (the Accounts page), so none is kept here.

const (
	usageSampleEvery = 5 * time.Second
	// usageSaveEvery spaces the writes; a stopping engine and a stopping
	// program write at once.
	usageSaveEvery = time.Minute
)

// UsageBytes is what clients sent (up) and received (down).
type UsageBytes struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

func (b *UsageBytes) add(o UsageBytes) {
	b.Up += o.Up
	b.Down += o.Down
}

// UsageCounts is a use through Mozilla (VPN) and directly by the routing
// rules.
type UsageCounts struct {
	VPN    UsageBytes `json:"vpn"`
	Direct UsageBytes `json:"direct"`
}

func (c *UsageCounts) add(vpn, direct UsageBytes) {
	c.VPN.add(vpn)
	c.Direct.add(direct)
}

// UsageDay is one day's use.
type UsageDay struct {
	// Date is the local day, YYYY-MM-DD.
	Date string `json:"date"`
	UsageCounts
}

// usageFile is usage.json, and the answer of GET /api/usage with the days
// newest first.
type usageFile struct {
	// Days, oldest first in the file.
	Days []*UsageDay `json:"days"`
}

type usageRecorder struct {
	path string

	mu      sync.Mutex
	data    usageFile
	dirty   bool
	savedAt time.Time
	// The counters of the engine sampled last, at that sample; a new
	// engine's counters start from zero.
	routed *proxy.RoutedOpener
	last   proxy.Traffic
}

func newUsageRecorder(path string) *usageRecorder {
	u := &usageRecorder{path: path, savedAt: time.Now()}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		logx.Warn("reading the data usage failed path=%s err=%s", path, logx.Err(err))
	default:
		if err := json.Unmarshal(raw, &u.data); err != nil {
			logx.Warn("the data usage file is unreadable; starting it again path=%s err=%s", path, logx.Err(err))
			u.data = usageFile{}
		}
	}
	return u
}

// note adds what the engine's counters t gained since the last sample to
// the day of now and to the total.
func (u *usageRecorder) note(routed *proxy.RoutedOpener, t proxy.Traffic, now time.Time) {
	if u == nil || routed == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	last := u.last
	if routed != u.routed {
		last = proxy.Traffic{}
	}
	u.routed, u.last = routed, t
	vpn := UsageBytes{Up: max(0, t.Up-last.Up), Down: max(0, t.Down-last.Down)}
	direct := UsageBytes{Up: max(0, t.DirectUp-last.DirectUp), Down: max(0, t.DirectDown-last.DirectDown)}
	if vpn == (UsageBytes{}) && direct == (UsageBytes{}) {
		return
	}
	u.day(now).add(vpn, direct)
	u.dirty = true
}

// day returns the day of now, added when new.
func (u *usageRecorder) day(now time.Time) *UsageDay {
	date := now.Format(time.DateOnly)
	for i := len(u.data.Days) - 1; i >= 0; i-- {
		if u.data.Days[i].Date == date {
			return u.data.Days[i] // the last, or an earlier one after the clock went back
		}
	}
	day := &UsageDay{Date: date}
	u.data.Days = append(u.data.Days, day)
	sort.Slice(u.data.Days, func(i, j int) bool { return u.data.Days[i].Date < u.data.Days[j].Date })
	return day
}

// reset forgets every day and saves at once; counting goes on from zero.
func (u *usageRecorder) reset() {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.data = usageFile{}
	u.dirty = true
	u.mu.Unlock()
	u.save(true)
}

// save writes the usage when it changed, at most every usageSaveEvery
// unless now.
func (u *usageRecorder) save(now bool) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.dirty || (!now && time.Since(u.savedAt) < usageSaveEvery) {
		return
	}
	raw, err := json.Marshal(u.data)
	if err == nil {
		err = securefile.WritePrivate(u.path, raw)
	}
	u.savedAt = time.Now()
	if err != nil {
		logx.Warn("saving the data usage failed path=%s err=%s", u.path, logx.Err(err))
		return
	}
	u.dirty = false
}

// snapshot returns a copy, the days newest first.
func (u *usageRecorder) snapshot() usageFile {
	out := usageFile{Days: []*UsageDay{}}
	if u == nil {
		return out
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for i := len(u.data.Days) - 1; i >= 0; i-- {
		day := *u.data.Days[i]
		out.Days = append(out.Days, &day)
	}
	return out
}

// noteUsage samples the running engine's counters.
func (d *Daemon) noteUsage() {
	if d.usage == nil {
		return
	}
	// Under d.mu, so a sample of an engine never follows the last one the
	// supervisor took of it after a newer engine started.
	d.mu.Lock()
	defer d.mu.Unlock()
	if engine := d.engine; engine != nil && engine.routed != nil {
		d.usage.note(engine.routed, engine.routed.Traffic(), time.Now())
	}
}

// noteEngineUsage takes an engine's last sample as it stops, and saves.
func (d *Daemon) noteEngineUsage(engine *proxyEngine) {
	if d.usage == nil || engine == nil || engine.routed == nil {
		return
	}
	d.usage.note(engine.routed, engine.routed.Traffic(), time.Now())
	d.usage.save(true)
}

// recordUsage samples the usage until the program stops, then saves it.
func (d *Daemon) recordUsage() {
	ticker := time.NewTicker(usageSampleEvery)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			d.noteUsage()
			d.usage.save(true)
			return
		case <-ticker.C:
			d.noteUsage()
			d.usage.save(false)
		}
	}
}

// handleUsage answers the data usage, today's up to now: {days: [{date,
// vpn: {up, down}, direct: {up, down}}]}, newest first.
func (s *dashboardServer) handleUsage(w http.ResponseWriter, r *http.Request) {
	s.d.noteUsage()
	writeJSON(w, http.StatusOK, s.d.usage.snapshot())
}

// handleUsageReset forgets the data usage; counting starts from zero.
func (s *dashboardServer) handleUsageReset(w http.ResponseWriter, r *http.Request) {
	s.d.noteUsage()
	s.d.usage.reset()
	logx.Info("data usage reset")
	writeJSON(w, http.StatusOK, s.d.usage.snapshot())
}
