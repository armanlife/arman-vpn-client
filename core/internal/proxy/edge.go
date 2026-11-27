package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/armanlife/arman-vpn-client/core/internal/evasion"
	"github.com/armanlife/arman-vpn-client/core/internal/logx"
	"github.com/armanlife/arman-vpn-client/core/internal/resolver"
)

// Edges: other addresses of Mozilla's servers, for a network that slows or
// drops the one a server's name resolves to. The address can decide where
// the traffic leaves, so which addresses a server may use is the build's
// profile's to say (evasion.ServerAddresses); the built-in profile knows
// none, and every server keeps its own. Both ways keep the server's name as
// the TLS name and the certificate check, and its port:
//   - the custom edge (network.edge), recommended location only: an IP, or
//     a range from which each connection picks one, used in place of the
//     server's own address;
//   - spare edges: when the server's own address does not answer, a few
//     spare addresses are tried beside it (withSpares): for the
//     recommended location the ones the device's own search found on its
//     network (ScanEdges, the fastest first), else the profile's, and last
//     the profile's last resort; for another server the profile's other
//     addresses of it. The one that answered is tried first the next time.

// spareHostSuffix is the domain of the servers the spare edges are for:
// they are Fastly's addresses, so they serve only names hosted there.
const spareHostSuffix = ".fastly-masque.net"

var (
	edge       atomic.Pointer[netip.Prefix]
	spareEdges atomic.Pointer[[]netip.Addr]
	// workingSpares is the spare address that answered last, by server name.
	workingSpares sync.Map
	recommendedMu sync.Mutex
	// recommendedHosts are the names of the recommended location's servers
	// (NewController notes them).
	recommendedHosts = map[string]bool{}
	// onSpareNeeded runs, in its own goroutine, when a recommended server's
	// own address did not answer; exhausted when its spares did not either.
	onSpareNeeded atomic.Pointer[func(exhausted bool)]
)

// OnSpareEdgesNeeded sets what runs when a recommended server's own address
// did not answer (exhausted: nor any spare): the service scans for spares
// of this network then, in the background.
func OnSpareEdgesNeeded(f func(exhausted bool)) {
	if f == nil {
		onSpareNeeded.Store(nil)
		return
	}
	onSpareNeeded.Store(&f)
}

func spareNeeded(exhausted bool) {
	if f := onSpareNeeded.Load(); f != nil {
		go (*f)(exhausted)
	}
}

// ParseEdge reads a network.edge value: empty (none), an IP, or a range.
func ParseEdge(spec string) (netip.Prefix, bool, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return netip.Prefix{}, false, nil
	}
	if strings.Contains(spec, "/") {
		p, err := netip.ParsePrefix(spec)
		if err != nil {
			return netip.Prefix{}, false, fmt.Errorf("%q is not an IP or a range such as 192.0.2.0/24", spec)
		}
		return p.Masked(), true, nil
	}
	a, err := netip.ParseAddr(spec)
	if err != nil {
		return netip.Prefix{}, false, fmt.Errorf("%q is not an IP or a range such as 192.0.2.0/24", spec)
	}
	return netip.PrefixFrom(a, a.BitLen()), true, nil
}

// SetEdge sets network.edge; empty turns it off.
func SetEdge(spec string) error {
	p, ok, err := ParseEdge(spec)
	if err != nil {
		return err
	}
	if !ok {
		edge.Store(nil)
		return nil
	}
	edge.Store(&p)
	logx.Info("connections to Mozilla's recommended location go to the custom edge=%s", p)
	return nil
}

// SetSpareEdges sets the recommended location's spare addresses, the ones
// a search found; none (or none valid) keeps the profile's.
func SetSpareEdges(ips []string) {
	var addrs []netip.Addr
	for _, s := range ips {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
			addrs = append(addrs, a)
		}
	}
	// A new set starts without the one that answered last.
	for _, host := range recommendedList() {
		workingSpares.Delete(host)
	}
	if len(addrs) == 0 {
		spareEdges.Store(nil)
		return
	}
	spareEdges.Store(&addrs)
}

func spares() []netip.Addr {
	if p := spareEdges.Load(); p != nil {
		return *p
	}
	return evasion.ServerAddresses().Spares
}

// noteRecommended records that u is a server of the recommended location.
func noteRecommended(u *url.URL, recommended bool) {
	if u == nil || !recommended {
		return
	}
	recommendedMu.Lock()
	recommendedHosts[strings.ToLower(u.Hostname())] = true
	recommendedMu.Unlock()
}

func recommendedList() []string {
	recommendedMu.Lock()
	defer recommendedMu.Unlock()
	out := make([]string, 0, len(recommendedHosts))
	for h := range recommendedHosts {
		out = append(out, h)
	}
	return out
}

// workingSpare is the spare address of host that answered last.
func workingSpare(host string) (netip.Addr, bool) {
	v, ok := workingSpares.Load(strings.ToLower(host))
	if !ok {
		return netip.Addr{}, false
	}
	return v.(netip.Addr), true
}

func isRecommendedHost(host string) bool {
	recommendedMu.Lock()
	defer recommendedMu.Unlock()
	return recommendedHosts[strings.ToLower(host)]
}

// customEdge returns the custom edge's address for a server of host, a
// random one of a range; false without one, or for another location.
func customEdge(host string) (netip.Addr, bool) {
	p := edge.Load()
	if p == nil || !isRecommendedHost(host) {
		return netip.Addr{}, false
	}
	return pickIn(*p), true
}

// spareEdgesFor returns the spare addresses to try for host after its own,
// the one that answered last first: the recommended location's, or a
// city's other addresses. None for another server, for the recommended
// location with a custom edge, or through the user's upstream proxy.
func spareEdgesFor(host string) []netip.Addr {
	host = strings.ToLower(host)
	if !strings.HasSuffix(host, spareHostSuffix) || resolver.UpstreamProxy() != "" {
		return nil
	}
	var list []netip.Addr
	if isRecommendedHost(host) {
		if edge.Load() != nil {
			return nil
		}
		list = append(list, spares()...)
		if len(list) > sparesPerRace {
			list = list[:sparesPerRace]
		}
		for _, a := range evasion.ServerAddresses().LastResort {
			if !containsAddr(list, a) {
				list = append(list, a)
			}
		}
	} else {
		list = evasion.ServerAddresses().Others(host)
	}
	if len(list) == 0 {
		return nil
	}
	out := make([]netip.Addr, 0, len(list))
	w, ok := workingSpare(host)
	if ok {
		out = append(out, w)
	}
	for _, a := range list {
		if !ok || a != w {
			out = append(out, a)
		}
	}
	return out
}

// pickIn returns a random address of p, avoiding the first and last of an
// IPv4 range of more than two (its network and broadcast addresses).
func pickIn(p netip.Prefix) netip.Addr {
	base := p.Addr()
	hostBits := base.BitLen() - p.Bits()
	if hostBits == 0 {
		return base
	}
	if base.Is4() {
		size := uint32(1) << hostBits
		offset := rand.Uint32N(size)
		if size > 2 {
			offset = 1 + rand.Uint32N(size-2)
		}
		b := base.As4()
		binary.BigEndian.PutUint32(b[:], binary.BigEndian.Uint32(b[:])+offset)
		return netip.AddrFrom4(b)
	}
	// IPv6: random bits in the last 64 at most.
	if hostBits > 64 {
		hostBits = 64
	}
	b := base.As16()
	var low uint64
	if hostBits == 64 {
		low = rand.Uint64()
	} else {
		low = binary.BigEndian.Uint64(b[8:]) | (rand.Uint64() & (1<<hostBits - 1))
	}
	binary.BigEndian.PutUint64(b[8:], low)
	return netip.AddrFrom16(b)
}

// edgeAddress returns address (host:port) with the custom edge's IP in
// place of the host, and whether there is one for it.
func edgeAddress(address string) (string, bool) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address, false
	}
	a, ok := customEdge(host)
	if !ok {
		return address, false
	}
	return net.JoinHostPort(a.String(), port), true
}

// spareStagger is how long an address is given alone before the next one
// starts beside it; one that fails sooner starts the next at once. A TLS
// handshake on a slow network takes a second or more, and every address
// started is another handshake on that network. A connection has about
// 10 seconds for the race (firefoxContext), so the last of the six
// addresses (sparesPerRace, own, two of the last resort) still has 2 or more.
var spareStagger = 1500 * time.Millisecond

// sparesPerRace is how many of the recommended location's found spares one
// connection tries: when they all fail, a new scan finds others.
const sparesPerRace = 3

// ownAvoid is how long a server's own address that did not answer goes
// after its spares that did, on the same network.
const ownAvoid = 5 * time.Minute

// ownFailed is when each server's own address last did not answer, by name.
var ownFailed sync.Map

// ForgetEdgeFailures forgets which own and spare addresses answered: on
// another network, the own ones may answer again.
func ForgetEdgeFailures() {
	ownFailed.Clear()
	workingSpares.Clear()
}

func ownFailedRecently(host string) bool {
	v, ok := ownFailed.Load(strings.ToLower(host))
	return ok && time.Since(v.(time.Time)) < ownAvoid
}

// withSpares runs attempt on the server's own address (at == "") and, when
// it does not answer, on each spare address too, the next one starting
// every spareStagger or as soon as one fails, all within ctx: the first
// that succeeds wins, and discard closes what the others still bring. A
// spare that answered before goes first; an own address that did not
// answer within ownAvoid goes after the found spares, before the last
// resort. The error is the own address's.
func withSpares[T any](ctx context.Context, proxyHost string, attempt func(ctx context.Context, at string) (T, error), discard func(T)) (T, error) {
	host, port, err := net.SplitHostPort(proxyHost)
	list := []netip.Addr(nil)
	if err == nil {
		list = spareEdgesFor(host)
	}
	if len(list) == 0 {
		return attempt(ctx, "")
	}
	recommended := isRecommendedHost(host)
	key := strings.ToLower(host)
	spareAt := make([]string, len(list))
	for i, a := range list {
		spareAt[i] = net.JoinHostPort(a.String(), port)
	}
	var order []string
	switch w, ok := workingSpare(host); {
	case ownFailedRecently(host):
		// The own address did not answer a moment ago: after the spares
		// found here, before the last resort.
		k := len(list)
		lastResort := evasion.ServerAddresses().LastResort
		for i, a := range list {
			if i > 0 && slices.Contains(lastResort, a) {
				k = i
				break
			}
		}
		order = append(append(append(order, spareAt[:k]...), ""), spareAt[k:]...)
	case ok && list[0] == w:
		// The last spare that answered goes first; the own address after it.
		order = append(append(order, spareAt[0], ""), spareAt[1:]...)
	default:
		order = append([]string{""}, spareAt...)
	}

	type outcome struct {
		at     string
		result T
		err    error
	}
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan outcome, len(order))
	var zero T
	var firstErr, ownErr error
	started, finished := 0, 0
	start := func() {
		at := order[started]
		started++
		go func() {
			result, err := attempt(raceCtx, at)
			done <- outcome{at, result, err}
		}()
	}
	start()
	timer := time.NewTimer(spareStagger)
	defer timer.Stop()
	for finished < started {
		select {
		case <-timer.C:
			if started < len(order) {
				start()
				timer.Reset(spareStagger)
			}
			continue
		case o := <-done:
			finished++
			if o.err == nil {
				cancel()
				// The others that still succeed are closed.
				go func(left int) {
					for ; left > 0; left-- {
						if late := <-done; late.err == nil && discard != nil {
							discard(late.result)
						}
					}
				}(started - finished)
				if o.at == "" {
					ownFailed.Delete(key)
					if _, ok := workingSpares.LoadAndDelete(key); ok {
						logx.Info("Mozilla's server answers at its own address again proxy=%s", proxyHost)
					}
				} else if a := edgeAddrOf(o.at); a.IsValid() {
					// Said once when the spares take over; which spare
					// answers first changes from one connection to the next.
					switch w, ok := workingSpare(host); {
					case !ok:
						logx.Info("Mozilla's server does not answer at its own address; using a spare one proxy=%s edge=%s", proxyHost, a)
					case w != a:
						logx.Debug("Mozilla's server answered at another spare address proxy=%s edge=%s", proxyHost, a)
					}
					workingSpares.Store(key, a)
				}
				return o.result, nil
			}
			if firstErr == nil {
				firstErr = o.err
			}
			if o.at == "" {
				ownErr = o.err
				if raceCtx.Err() == nil {
					ownFailed.Store(key, time.Now())
				}
				if recommended {
					spareNeeded(false)
				}
			} else if w, ok := workingSpare(host); ok && w == edgeAddrOf(o.at) {
				workingSpares.Delete(key)
			}
			if started < len(order) && ctx.Err() == nil {
				start()
				timer.Reset(spareStagger)
			}
		}
	}
	if ctx.Err() == nil && recommended {
		spareNeeded(true)
	}
	if ownErr != nil {
		return zero, ownErr
	}
	return zero, firstErr
}

// edgeAddrOf is the IP of a host:port address; invalid when there is none.
func edgeAddrOf(address string) netip.Addr {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return netip.Addr{}
	}
	a, _ := netip.ParseAddr(host)
	return a
}
