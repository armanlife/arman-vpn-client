package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/armanlife/arman-vpn-client/core/internal/proxy"
)

// The saved scan is answered as it is, empty before any.
func TestSavedEdges(t *testing.T) {
	d := &Daemon{paths: DataPaths{Edges: filepath.Join(t.TempDir(), edgesFileName)}}
	if got := d.edges(); got.Edges == nil || len(got.Edges) != 0 || !got.ScannedAt.IsZero() {
		t.Fatalf("before a scan: %+v", got)
	}
	saved := savedEdges{ScannedAt: time.Now().Round(time.Second), Edges: []proxy.EdgeResult{{IP: "192.0.2.141", MS: 120}, {IP: "192.0.2.50", MS: 180}}}
	raw, _ := json.Marshal(saved)
	if err := os.WriteFile(d.paths.Edges, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loadEdges(d.paths.Edges)
	defer proxy.SetSpareEdges(nil)
	got := d.edges()
	if len(got.Edges) != 2 || got.Edges[0].IP != "192.0.2.141" || !got.ScannedAt.Equal(saved.ScannedAt) {
		t.Fatalf("saved: %+v", got)
	}
}

// The background scan does not run while this device has spares of its own
// that were not all tried, nor again soon after one.
func TestBackgroundScanWaits(t *testing.T) {
	d := &Daemon{paths: DataPaths{Edges: filepath.Join(t.TempDir(), edgesFileName)}}
	raw, _ := json.Marshal(savedEdges{ScannedAt: time.Now(), Edges: []proxy.EdgeResult{{IP: "192.0.2.141", MS: 120}}})
	if err := os.WriteFile(d.paths.Edges, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// Spares known, not yet exhausted: nothing to do.
	d.spareEdgesNeeded(false)
	if !d.edgeScan.lastAt.IsZero() {
		t.Fatal("scanned with spares of its own")
	}
	// Exhausted, but a scan ran a moment ago: wait.
	d.edgeScan.lastAt = time.Now()
	d.spareEdgesNeeded(true)
	if d.edgeScan.running {
		t.Fatal("scanning again so soon")
	}
}
