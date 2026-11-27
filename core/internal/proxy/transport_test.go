package proxy

import (
	"errors"
	"fmt"
	"net"
	"reflect"
	"testing"

	"golang.org/x/net/http2"
)

// The user's transport first, then HTTP/2 and HTTP/1.1; HTTP/3 only when
// chosen.
func TestTransportOrder(t *testing.T) {
	for preferred, want := range map[string][]string{
		"h1": {"h1", "h2"},
		"h2": {"h2", "h1"},
		"h3": {"h3", "h2", "h1"},
		"":   {"h2", "h1"},
	} {
		if got := TransportOrder(preferred); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: %v", preferred, got)
		}
	}
}

// A transport that does not connect gives way to the next; HTTP/2 gives
// way to HTTP/1.1 only for a failure of HTTP/2 itself, since both take the
// same path; a failure that is not the server's (a refused pass) changes
// nothing.
func TestTransportAdvance(t *testing.T) {
	timeout := &net.OpError{Op: "dial", Net: "tcp4", Err: errors.New("i/o timeout")}
	h1 := newTransportState(TransportOrder("h1"))
	if !h1.advance(timeout) || h1.current() != "h2" {
		t.Fatalf("HTTP/1.1 stays: %s", h1.current())
	}
	if h1.advance(errProxyHTTP2Unavailable) {
		t.Fatal("past the last transport")
	}
	if got := h1.fromCurrent(); !reflect.DeepEqual(got, []string{"h2", "h1"}) {
		t.Fatalf("next wake %v", got)
	}
	h1.reset()
	if h1.current() != "h1" {
		t.Fatal("not back to the user's")
	}

	h2 := newTransportState(TransportOrder("h2"))
	for _, err := range []error{timeout, &upstreamHandshakeError{errors.New("reset")}, fmt.Errorf("resolving x failed: %w", errors.New("timeout")), errors.New("proxy CONNECT timed out after 20s")} {
		if h2.advance(err) {
			t.Fatalf("HTTP/2 left for HTTP/1.1: %v", err)
		}
	}
	if h2.advance(&proxyConnectHTTPError{statusCode: 403}) {
		t.Fatal("moved on a refusal")
	}
	if !h2.advance(errProxyHTTP2Unavailable) || h2.current() != "h1" {
		t.Fatal("HTTP/2 not negotiated, yet no HTTP/1.1")
	}
	if h2 := newTransportState(TransportOrder("h2")); !h2.advance(fmt.Errorf("session: %w", http2.ConnectionError(http2.ErrCodeProtocol))) {
		t.Fatal("a broken HTTP/2 connection, yet no HTTP/1.1")
	}

	h3 := newTransportState(TransportOrder("h3"))
	if !h3.advance(timeout) || h3.current() != "h2" {
		t.Fatal("HTTP/3 stays")
	}
}

// Once connected, transportStrikes failures in a row move on; a tunnel that
// opens between them starts the count again.
func TestTransportStrikes(t *testing.T) {
	timeout := &net.OpError{Op: "dial", Net: "tcp4", Err: errors.New("i/o timeout")}
	s := newTransportState(TransportOrder("h1"))
	if s.strike(timeout) {
		t.Fatal("moved after one failure")
	}
	s.worked()
	if s.strike(timeout) {
		t.Fatal("the count did not start again")
	}
	if !s.strike(timeout) || s.current() != "h2" {
		t.Fatal("did not move after two in a row")
	}
	if s.strike(timeout) || s.strike(timeout) {
		t.Fatal("moved past the last")
	}
}
