package proxy

import (
	"errors"
	"sync"

	"golang.org/x/net/http2"

	"github.com/armanlife/arman-vpn-client/core/internal/logx"
)

// The transport to Mozilla's server: HTTP/2 (one TLS connection over TCP
// for every tunnel), HTTP/3 (QUIC, over UDP), or HTTP/1.1 (a TLS connection
// per tunnel). The user's choice (proxy.transport) is tried first; when it
// does not connect, or keeps failing once connected, the others are used
// instead, for that controller and the engine's later wakes, until the
// network changes. What the user wants is to connect; the setting itself
// stays as it is. HTTP/3 is tried only when chosen: a network that blocks
// much, as in Iran, rarely lets QUIC through, so trying it would only add
// a wait. HTTP/2 gives way to HTTP/1.1 only when HTTP/2 itself fails (the
// server does not take it, or its connection breaks the protocol): the two
// take the same path (TCP and TLS), so an address that does not answer or
// a slow network fails HTTP/1.1 too, and worse, since it needs a
// connection per tunnel where HTTP/2 needs one. From HTTP/1.1 and HTTP/3,
// any failure moves on.

const (
	TransportH2 = "h2"
	TransportH3 = "h3"
	TransportH1 = "h1"
)

// transportStrikes is how many failures in a row of a connected transport,
// counted where the server failover counts them (failOver), move to the
// next transport: fewer than serverFailoverStrikes, so another transport is
// tried before another server.
const transportStrikes = 2

// TransportOrder returns the transports to try: preferred first, then
// HTTP/2 and HTTP/1.1.
func TransportOrder(preferred string) []string {
	switch preferred {
	case TransportH2, TransportH3, TransportH1:
	default:
		preferred = TransportH2
	}
	order := []string{preferred}
	for _, t := range []string{TransportH2, TransportH1} {
		if t != preferred {
			order = append(order, t)
		}
	}
	return order
}

// TransportName is the transport's name for people.
func TransportName(t string) string {
	switch t {
	case TransportH3:
		return "HTTP/3"
	case TransportH1:
		return "HTTP/1.1"
	}
	return "HTTP/2"
}

// transportState is a controller's transport: the one in use, of the
// order to try, and the failures in a row of it. A nil one is HTTP/2 alone.
type transportState struct {
	mu      sync.Mutex
	order   []string
	at      int
	strikes int
}

func newTransportState(order []string) *transportState {
	if len(order) == 0 {
		order = TransportOrder(TransportH2)
	}
	return &transportState{order: append([]string(nil), order...)}
}

func (t *transportState) current() string {
	if t == nil {
		return TransportH2
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.order[t.at]
}

// fromCurrent is the order starting at the transport in use, for a later
// wake to start where this one ended.
func (t *transportState) fromCurrent() []string {
	if t == nil {
		return []string{TransportH2}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := append([]string(nil), t.order[t.at:]...)
	return append(out, t.order[:t.at]...)
}

// advance moves to the next transport after err, a failure that is the
// server's or the network path's (serverFault); false at the last one.
func (t *transportState) advance(err error) bool {
	if t == nil || !serverFault(err) {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.advanceLocked(err)
}

func (t *transportState) advanceLocked(err error) bool {
	if t.at+1 >= len(t.order) {
		return false
	}
	if t.order[t.at] == TransportH2 && t.order[t.at+1] == TransportH1 && !http2Failure(err) {
		return false
	}
	from := t.order[t.at]
	t.at++
	t.strikes = 0
	logx.Warn("%s did not connect; using %s instead err=%s", TransportName(from), TransportName(t.order[t.at]), logx.Err(err))
	return true
}

// strike counts err against a connected transport and moves to the next
// after transportStrikes in a row; it reports whether it moved.
func (t *transportState) strike(err error) bool {
	if t == nil || !serverFault(err) {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.at+1 >= len(t.order) {
		return false
	}
	t.strikes++
	if t.strikes < transportStrikes {
		return false
	}
	return t.advanceLocked(err)
}

// worked clears the failures in a row: a tunnel opened.
func (t *transportState) worked() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.strikes = 0
	t.mu.Unlock()
}

// reset goes back to the first transport, the user's choice.
func (t *transportState) reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.at, t.strikes = 0, 0
	t.mu.Unlock()
}

// http2Failure reports whether err is HTTP/2's own: the server did not
// take it, or the connection broke the protocol.
func http2Failure(err error) bool {
	var conn http2.ConnectionError
	var goAway http2.GoAwayError
	return errors.Is(err, errProxyHTTP2Unavailable) || errors.As(err, &conn) || errors.As(err, &goAway)
}
