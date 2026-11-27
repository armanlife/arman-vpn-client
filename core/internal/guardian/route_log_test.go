package guardian

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/armanlife/arman-vpn-client/core/internal/logx"
)

// The same route as the last time is not told again at Info; another is.
func TestRouteReachedIsToldWhenItChanges(t *testing.T) {
	var out bytes.Buffer
	logx.SetOutput(&out)
	t.Cleanup(func() { logx.SetOutput(io.Discard) })
	lastRouteReached.Store("")
	logRouteReached("direct", "vendor", "direct=200")
	logRouteReached("direct", "vendor", "direct=200")
	logRouteReached("alternate", "vendor", "direct=timeout alternate=200")
	logRouteReached("direct", "vendor", "direct=200")
	if n := strings.Count(out.String(), "INFO  Guardian reached"); n != 3 {
		t.Fatalf("%d lines at Info:\n%s", n, out.String())
	}
}
