package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"sync/atomic"
	"time"

	utls "github.com/refraction-networking/utls"

	"github.com/armanlife/arman-vpn-client/core/internal/firefoxtls"
	"github.com/armanlife/arman-vpn-client/core/internal/hellosplit"
	"github.com/armanlife/arman-vpn-client/core/internal/logx"
	"github.com/armanlife/arman-vpn-client/core/internal/resolver"
)

var (
	// upstreamTLSSessions and upstreamFirefoxSessions let a new connection to a
	// server resume TLS, which saves the time of a whole handshake when the
	// network cuts connections and the program reconnects.
	upstreamTLSSessions     = tls.NewLRUClientSessionCache(32)
	upstreamFirefoxSessions = utls.NewLRUClientSessionCache(32)

	// upstreamGoTLS is set once Firefox's hello failed where Go's passed: a
	// network or server that takes only Go's gets it without another failure
	// on each new connection, until the program restarts.
	upstreamGoTLS atomic.Bool

	// upstreamHandshakeFailures counts the TLS handshakes with Mozilla's
	// servers that failed both ways, so a slow connection says it in one
	// line (TakeUpstreamHandshakeFailures) instead of one per server.
	upstreamHandshakeFailures atomic.Int64
)

// TakeUpstreamHandshakeFailures returns how many TLS handshakes with
// Mozilla's servers failed since the last call, and starts again from zero.
func TakeUpstreamHandshakeFailures() int64 { return upstreamHandshakeFailures.Swap(0) }

// firefoxHandshakeShare is how much of the time left the Firefox hello may
// use, leaving the rest to Go's for a network that drops it silently.
const firefoxHandshakeShare = 2

// The protocols a connection to Mozilla speaks over TLS (ALPN).
const (
	alpnH2 = "h2"
	alpnH1 = "http/1.1"
)

// negotiated checks the protocol TLS agreed on: HTTP/2 must be named; for
// HTTP/1.1 a server that names none speaks it too.
func negotiated(proto, got string) error {
	switch {
	case got == proto, proto == alpnH1 && got == "":
		return nil
	case proto == alpnH1:
		return errProxyHTTP1Unavailable
	}
	return errProxyHTTP2Unavailable
}

// firefoxHandshake completes TLS on raw with Firefox's hello, for HTTP/2.
func firefoxHandshake(ctx context.Context, raw net.Conn, name string) (net.Conn, error) {
	return firefoxHandshakeFor(alpnH2)(ctx, raw, name)
}

// firefoxHandshakeFor is firefoxHandshake for proto: Firefox offers both
// protocols; HTTP/1.1 alone, offered alone, makes the server take it.
func firefoxHandshakeFor(proto string) func(context.Context, net.Conn, string) (net.Conn, error) {
	offer := []string{alpnH2, alpnH1}
	if proto == alpnH1 {
		offer = []string{alpnH1}
	}
	return func(ctx context.Context, raw net.Conn, name string) (net.Conn, error) {
		conn, err := firefoxtls.Handshake(ctx, raw, name, upstreamRoots.Load(), upstreamFirefoxSessions, offer...)
		if err != nil {
			return nil, err
		}
		if err := negotiated(proto, conn.ConnectionState().NegotiatedProtocol); err != nil {
			return nil, err
		}
		return conn, nil
	}
}

// goHandshake completes TLS on raw with Go's own hello, for HTTP/2.
func goHandshake(ctx context.Context, raw net.Conn, name string) (net.Conn, error) {
	return goHandshakeFor(alpnH2)(ctx, raw, name)
}

// goHandshakeFor is goHandshake for proto.
func goHandshakeFor(proto string) func(context.Context, net.Conn, string) (net.Conn, error) {
	return func(ctx context.Context, raw net.Conn, name string) (net.Conn, error) {
		conn := tls.Client(raw, &tls.Config{
			RootCAs:            upstreamRoots.Load(),
			ServerName:         name,
			MinVersion:         tls.VersionTLS12,
			NextProtos:         []string{proto},
			ClientSessionCache: upstreamTLSSessions,
		})
		if err := conn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		if err := negotiated(proto, conn.ConnectionState().NegotiatedProtocol); err != nil {
			return nil, err
		}
		return conn, nil
	}
}

// firefoxContext limits ctx to the share of its time the Firefox hello may use.
func firefoxContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Until(deadline)/firefoxHandshakeShare)
}

// upstreamHandshakeError marks a failure of TLS itself, which another hello may
// get past; a failure to connect is not one.
type upstreamHandshakeError struct{ err error }

func (e *upstreamHandshakeError) Error() string { return e.err.Error() }
func (e *upstreamHandshakeError) Unwrap() error { return e.err }

// upstreamHandshake connects to proxyHost and completes TLS with shake; a
// server that does not answer at its own address is tried at its spare ones
// (withSpares).
func upstreamHandshake(ctx context.Context, proxyHost, name string, shake func(context.Context, net.Conn, string) (net.Conn, error)) (net.Conn, *watchedConn, error) {
	type connected struct {
		conn net.Conn
		raw  *watchedConn
	}
	c, err := withSpares(ctx, proxyHost, func(ctx context.Context, at string) (connected, error) {
		conn, raw, err := handshakeAt(ctx, proxyHost, at, name, shake)
		return connected{conn, raw}, err
	}, func(c connected) { _ = c.conn.Close() })
	return c.conn, c.raw, err
}

// handshakeAt connects to proxyHost, at the address at when one is given,
// and completes TLS with shake for name.
func handshakeAt(ctx context.Context, proxyHost, at, name string, shake func(context.Context, net.Conn, string) (net.Conn, error)) (net.Conn, *watchedConn, error) {
	var dialed net.Conn
	var err error
	if at == "" {
		dialed, err = dialUpstream(ctx, proxyHost)
	} else {
		dialed, err = resolver.Dial(ctx, "tcp", at)
	}
	if err != nil {
		return nil, nil, err
	}
	raw := newWatchedConn(hellosplit.Wrap(dialed))
	conn, err := shake(ctx, raw, name)
	if err != nil {
		_ = raw.Close()
		if errors.Is(err, errProxyHTTP2Unavailable) || errors.Is(err, errProxyHTTP1Unavailable) {
			return nil, nil, err
		}
		return nil, nil, &upstreamHandshakeError{err}
	}
	return conn, raw, nil
}

// dialUpstreamTLS connects to proxyHost and completes TLS with proxyURL's name,
// for HTTP/2. It looks like the Firefox its User-Agent says it is; a handshake
// that fails that way is tried again with Go's hello.
func dialUpstreamTLS(ctx context.Context, proxyURL *url.URL, proxyHost string) (net.Conn, *watchedConn, error) {
	return dialUpstreamTLSFor(ctx, proxyURL, proxyHost, alpnH2)
}

// dialUpstreamTLSFor is dialUpstreamTLS for proto (alpnH2 or alpnH1).
func dialUpstreamTLSFor(ctx context.Context, proxyURL *url.URL, proxyHost, proto string) (net.Conn, *watchedConn, error) {
	name := proxyURL.Hostname()
	goShake := goHandshakeFor(proto)
	if upstreamGoTLS.Load() {
		conn, raw, err := upstreamHandshake(ctx, proxyHost, name, goShake)
		noteHandshakeFailure(ctx, err)
		return conn, raw, err
	}
	firstCtx, cancel := firefoxContext(ctx)
	defer cancel()
	conn, raw, err := upstreamHandshake(firstCtx, proxyHost, name, firefoxHandshakeFor(proto))
	var failed *upstreamHandshakeError
	if err == nil || ctx.Err() != nil || !errors.As(err, &failed) {
		return conn, raw, err
	}
	// Each server's failure is for a verbose log: on a network that drops
	// the handshakes, every server tried would add a line. What counts is
	// whether Go's hello then passes, said once.
	logx.Debug("upstream TLS with Firefox's hello failed; trying Go's proxy=%s err=%s", proxyHost, logx.Err(err))
	conn, raw, err = upstreamHandshake(ctx, proxyHost, name, goShake)
	if err == nil && upstreamGoTLS.CompareAndSwap(false, true) {
		logx.Info("this network answers Go's TLS hello but not Firefox's; using Go's until the program restarts proxy=%s", proxyHost)
	}
	noteHandshakeFailure(ctx, err)
	return conn, raw, err
}

// noteHandshakeFailure counts a failed handshake that was not given up by
// the caller.
func noteHandshakeFailure(ctx context.Context, err error) {
	var failed *upstreamHandshakeError
	if err != nil && ctx.Err() == nil && errors.As(err, &failed) {
		upstreamHandshakeFailures.Add(1)
	}
}
