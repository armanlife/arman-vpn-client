package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/armanlife/arman-vpn-client/core/internal/proxy"
)

// The recorder adds what the counters gained to the day; a new engine
// starts from zero; and the days are kept across a restart of the program.
func TestUsageCountsEachDay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	u := newUsageRecorder(path)
	first, second := &proxy.RoutedOpener{}, &proxy.RoutedOpener{}
	day1 := time.Date(2026, 11, 24, 23, 59, 0, 0, time.Local)
	day2 := day1.Add(2 * time.Minute)

	u.note(first, proxy.Traffic{Up: 10, Down: 100, DirectUp: 1, DirectDown: 5}, day1)
	u.note(first, proxy.Traffic{Up: 30, Down: 300, DirectUp: 1, DirectDown: 5}, day1)
	// The next day; the engine's counters go on.
	u.note(first, proxy.Traffic{Up: 35, Down: 350, DirectUp: 2, DirectDown: 9}, day2)
	// A new engine: its counters start again from zero.
	u.note(second, proxy.Traffic{Up: 5, Down: 50}, day2)
	u.note(second, proxy.Traffic{Up: 5, Down: 50}, day2) // nothing new
	u.save(true)

	got := newUsageRecorder(path).snapshot()
	if len(got.Days) != 2 {
		t.Fatalf("days = %+v", got.Days)
	}
	today, yesterday := got.Days[0], got.Days[1]
	if today.Date != "2026-11-25" || yesterday.Date != "2026-11-24" {
		t.Fatalf("dates = %s, %s; want newest first", today.Date, yesterday.Date)
	}
	if yesterday.VPN != (UsageBytes{Up: 30, Down: 300}) || yesterday.Direct != (UsageBytes{Up: 1, Down: 5}) {
		t.Fatalf("yesterday = %+v", yesterday)
	}
	if today.VPN != (UsageBytes{Up: 10, Down: 100}) || today.Direct != (UsageBytes{Up: 1, Down: 4}) {
		t.Fatalf("today = %+v", today)
	}
}

// Every day is kept, however old.
func TestUsageKeepsEveryDay(t *testing.T) {
	u := newUsageRecorder(filepath.Join(t.TempDir(), "usage.json"))
	routed := &proxy.RoutedOpener{}
	start := time.Date(2025, 1, 1, 12, 0, 0, 0, time.Local)
	for i := range 400 {
		u.note(routed, proxy.Traffic{Down: int64(i + 1)}, start.AddDate(0, 0, i))
	}
	got := u.snapshot()
	if len(got.Days) != 400 || got.Days[399].Date != "2025-01-01" {
		t.Fatalf("kept %d days, the oldest %s", len(got.Days), got.Days[len(got.Days)-1].Date)
	}
}

// A reset forgets every day, also on disk, and counting goes on from zero
// without the bytes counted before it.
func TestUsageReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	u := newUsageRecorder(path)
	routed := &proxy.RoutedOpener{}
	now := time.Now()
	u.note(routed, proxy.Traffic{Down: 100, DirectUp: 5}, now)
	u.reset()
	if got := newUsageRecorder(path).snapshot(); len(got.Days) != 0 {
		t.Fatalf("days after the reset = %+v", got.Days)
	}
	u.note(routed, proxy.Traffic{Down: 130, DirectUp: 5}, now)
	got := u.snapshot()
	if len(got.Days) != 1 || got.Days[0].VPN != (UsageBytes{Down: 30}) || got.Days[0].Direct != (UsageBytes{}) {
		t.Fatalf("days = %+v, want only what came after the reset", got.Days)
	}
}
