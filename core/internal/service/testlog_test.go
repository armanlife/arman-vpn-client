package service

import (
	"testing"
	"time"
)

// The test the app asks for as the proxy connects, right after the
// proxy's own, is not logged again with the same answer; a later one, or
// one at another place, is.
func TestRepeatedTestPassIsLoggedOnce(t *testing.T) {
	d := &Daemon{}
	now := time.Now()
	for _, c := range []struct {
		place string
		after time.Duration
		want  bool
	}{
		{"IT, Milan, Fastly", 0, false},
		{"IT, Milan, Fastly", time.Second, true},
		{"DE, Frankfurt, Fastly", 2 * time.Second, false},
		{"DE, Frankfurt, Fastly", 2*time.Second + 2*time.Minute, false},
	} {
		if got := d.testPassRepeats(c.place, now.Add(c.after)); got != c.want {
			t.Fatalf("%s after %s: repeats = %v, want %v", c.place, c.after, got, c.want)
		}
	}
}
