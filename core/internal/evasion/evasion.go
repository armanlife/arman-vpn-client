// Package evasion holds the client's connection profile: the knobs the
// transport layer consults when a network refuses ordinary traffic. The
// built-in profile is the default and is what this repository builds and
// ships from source. A build may register a different profile at startup
// (Register) when it is put together with its own tuning, which keeps
// profile choices out of the transport code and out of user settings.
package evasion

import (
	"net/netip"
	"net/url"
)

// Profile is the active connection profile. It is deliberately narrow, and
// its methods say what is needed rather than how it is produced.
type Profile interface {
	// Name identifies the active profile. The daemon logs it at startup so
	// a build plainly reports which profile it is running.
	Name() string

	// Authorize returns the query parameters that let a shared endpoint
	// accept one request for method and target, or nil when this build has
	// nothing to offer. A build assembled from this repository alone
	// returns nil, so it simply does not use shared endpoints.
	Authorize(method, target string) url.Values
}

// builtin is the profile compiled from this repository.
type builtin struct{}

func (builtin) Name() string { return "base" }

func (builtin) Authorize(string, string) url.Values { return nil }

var active Profile = builtin{}

// Register installs a profile in place of the built-in one, or restores the
// built-in one when given nil. It is called from an init during startup,
// before any connection is made.
func Register(p Profile) {
	if p == nil {
		active = builtin{}
		return
	}
	active = p
}

// Active returns the profile in force.
func Active() Profile { return active }

// Name is the active profile's identifier.
func Name() string { return active.Name() }

// Authorize asks the active profile to authorize one request for a shared
// endpoint. A nil result means this build does not use them.
func Authorize(method, target string) url.Values { return active.Authorize(method, target) }

// GuardianAlternateAddress returns an optional TCP address for Guardian.
func GuardianAlternateAddress() string {
	if profile, ok := active.(interface{ GuardianAlternateAddress() string }); ok {
		return profile.GuardianAlternateAddress()
	}
	return ""
}

// Addresses are other addresses at which the servers this build connects to
// may answer, tried when a server's own address does not. The built-in
// profile knows none, so a server is reached only at the addresses its name
// resolves to.
type Addresses struct {
	// Spares are tried for the recommended location before a search found
	// spares of its own.
	Spares []netip.Addr
	// Candidates are what a search for the recommended location's spares
	// picks from: Sample of them at most, keeping the first Want that
	// answer. None means no search.
	Candidates   []netip.Addr
	Sample, Want int
	// LastResort are tried for the recommended location after its spares.
	LastResort []netip.Addr
	// Others are a server's other addresses, by its name.
	Others func(host string) []netip.Addr
}

// ServerAddresses returns what the active profile knows of the servers'
// other addresses.
func ServerAddresses() Addresses {
	var a Addresses
	if p, ok := active.(interface{ Spares() []netip.Addr }); ok {
		a.Spares = p.Spares()
	}
	if p, ok := active.(interface {
		SpareCandidates() ([]netip.Addr, int, int)
	}); ok {
		a.Candidates, a.Sample, a.Want = p.SpareCandidates()
	}
	if p, ok := active.(interface{ LastResort() []netip.Addr }); ok {
		a.LastResort = p.LastResort()
	}
	if p, ok := active.(interface {
		OtherAddresses(host string) []netip.Addr
	}); ok {
		a.Others = p.OtherAddresses
	} else {
		a.Others = func(string) []netip.Addr { return nil }
	}
	return a
}
