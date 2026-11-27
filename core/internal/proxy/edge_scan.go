package proxy

import (
	"context"
	"math/rand/v2"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/armanlife/arman-vpn-client/core/internal/logx"
)

// The edge scan: which of the profile's candidate addresses answer for
// Mozilla's recommended server on this network, and how fast. Networks
// differ (the operator, the city, the kind of line), so each device finds
// its own, by itself: the service scans in the background when the server's
// own address fails (OnSpareEdgesNeeded), a random sample of the
// candidates, until enough answered (evasion.Addresses: Sample, Want). The
// addresses that answered become its spare edges (SetSpareEdges). An
// address is tried the way a connection to Mozilla is made (the server's
// name as the TLS name, its certificate checked, the ClientHello as the
// settings send it); nothing is sent through it, so no account is used.

const (
	// edgeScanWorkers addresses are tried at once.
	edgeScanWorkers = 16
	// EdgeScanTimeout bounds one address's connection and handshake.
	EdgeScanTimeout = 3 * time.Second
)

// EdgeResult is one address's answer.
type EdgeResult struct {
	IP string `json:"ip"`
	// MS is the time to connect and complete TLS, when it answered.
	MS    int64  `json:"ms,omitempty"`
	Error string `json:"error,omitempty"`
}

// EdgeSample returns up to n of candidates, picked at random.
func EdgeSample(candidates []netip.Addr, n int) []netip.Addr {
	out := append([]netip.Addr(nil), candidates...)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// EdgeCandidates returns the candidates a scan tries: all but excluded
// (the servers' own addresses, in case one is among them).
func EdgeCandidates(candidates, excluded []netip.Addr) []netip.Addr {
	skip := make(map[netip.Addr]bool, len(excluded))
	for _, a := range excluded {
		skip[a] = true
	}
	var out []netip.Addr
	for _, a := range candidates {
		if !skip[a] {
			out = append(out, a)
		}
	}
	return out
}

// ScanEdges tries the addresses of candidates for server (host:port), in
// order, edgeScanWorkers at a time, each within EdgeScanTimeout, until want
// answered (0: all of them). onResult, when set, gets each result as it
// comes, one at a time. It returns the addresses that answered, the fastest
// first.
func ScanEdges(ctx context.Context, server string, candidates []netip.Addr, want int, onResult func(EdgeResult)) []EdgeResult {
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		return nil
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	var (
		mu       sync.Mutex
		answered []EdgeResult
		wg       sync.WaitGroup
	)
	jobs := make(chan netip.Addr)
	shake := goHandshakeFor(alpnH2)
	for i := 0; i < edgeScanWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range jobs {
				result := EdgeResult{IP: a.String()}
				tryCtx, cancel := context.WithTimeout(ctx, EdgeScanTimeout)
				started := time.Now()
				conn, _, err := handshakeAt(tryCtx, server, net.JoinHostPort(a.String(), port), host, shake)
				cancel()
				if err == nil {
					result.MS = max(time.Since(started).Milliseconds(), 1)
					_ = conn.Close()
				} else {
					result.Error = logx.Err(err)
				}
				mu.Lock()
				if err == nil && (want == 0 || len(answered) < want) {
					answered = append(answered, result)
					if want > 0 && len(answered) == want {
						stop() // enough: the rest are not needed
					}
				}
				if onResult != nil && (err == nil || ctx.Err() == nil) {
					onResult(result)
				}
				mu.Unlock()
			}
		}()
	}
	for _, a := range candidates {
		select {
		case jobs <- a:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	sort.Slice(answered, func(i, j int) bool { return answered[i].MS < answered[j].MS })
	return answered
}
