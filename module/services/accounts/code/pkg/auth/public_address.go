package auth

import (
	"net"
	"net/netip"
)

// Whether an address is on the public internet.
//
// Go's net.IP.IsGlobalUnicast is NOT a public-routability test: it answers
// "is this a unicast address outside the loopback/link-local/multicast set",
// which leaves shared, benchmarking, documentation and reserved space looking
// public. Combining it with IsPrivate still admits 100.64.0.0/10 (carrier NAT,
// routable inside many networks), 198.18.0.0/15 (benchmarking), 240.0.0.0/4
// (reserved), the documentation ranges, and the 6to4 relay anycast block.
//
// A guard built on those two predicates therefore does not mean what the
// comment above it says, and for a URL an unauthenticated caller chooses — a
// Client ID Metadata Document's client_id — the gap is an SSRF reach into
// whatever a deployment routes in that space.
//
// The classifier below enumerates the IANA special-purpose registries for both
// families instead. It is deliberately a denylist of non-public space rather
// than an allowlist of public space: the unassigned ranges that exist today
// become public assignments tomorrow, and a deployment must not need a release
// to reach a newly assigned host.

// specialUseIPv4 is the IANA IPv4 Special-Purpose Address Registry, minus the
// ranges net.IP already answers for (loopback, link-local, multicast). Each
// entry is non-public destination space.
var specialUseIPv4 = []string{
	"0.0.0.0/8",          // "this network"
	"10.0.0.0/8",         // private
	"100.64.0.0/10",      // shared address space (carrier NAT)
	"172.16.0.0/12",      // private
	"192.0.0.0/24",       // IETF protocol assignments
	"192.0.2.0/24",       // documentation (TEST-NET-1)
	"192.31.196.0/24",    // AS112
	"192.52.193.0/24",    // AMT
	"192.88.99.0/24",     // 6to4 relay anycast, deprecated
	"192.168.0.0/16",     // private
	"192.175.48.0/24",    // AS112 direct delegation
	"198.18.0.0/15",      // benchmarking
	"198.51.100.0/24",    // documentation (TEST-NET-2)
	"203.0.113.0/24",     // documentation (TEST-NET-3)
	"240.0.0.0/4",        // reserved
	"255.255.255.255/32", // limited broadcast
}

// specialUseIPv6 is the IANA IPv6 Special-Purpose Address Registry, same
// basis. fc00::/7 (unique local) is listed explicitly rather than left to
// net.IP.IsPrivate, so the set reads as one inventory.
var specialUseIPv6 = []string{
	"::/128",         // unspecified
	"::1/128",        // loopback
	"64:ff9b::/96",   // NAT64
	"64:ff9b:1::/48", // local-use NAT64
	"100::/64",       // discard-only
	"2001::/32",      // Teredo
	"2001:1::1/128",  // port control protocol anycast
	"2001:1::2/128",  // TURN anycast
	"2001:2::/48",    // benchmarking
	"2001:db8::/32",  // documentation
	"2001:10::/28",   // deprecated ORCHID
	"2001:20::/28",   // ORCHIDv2
	"2002::/16",      // 6to4
	"fc00::/7",       // unique local
	"fe80::/10",      // link-local unicast
	"5f00::/16",      // segment routing
}

// specialUsePrefixes is both registries, parsed once.
var specialUsePrefixes = parseSpecialUsePrefixes()

func parseSpecialUsePrefixes() []netip.Prefix {
	raw := make([]string, 0, len(specialUseIPv4)+len(specialUseIPv6))
	raw = append(raw, specialUseIPv4...)
	raw = append(raw, specialUseIPv6...)
	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, candidate := range raw {
		// MustParsePrefix is right here: these are constants in this file, so a
		// bad one is a defect that must fail at startup rather than quietly
		// shrink the guard.
		prefixes = append(prefixes, netip.MustParsePrefix(candidate))
	}
	return prefixes
}

// IsPublicDestination reports whether address is a unicast address on the
// public internet, and so a destination this host may be asked to fetch from.
//
// It is applied to the CONCRETE address a connection is about to reach, after
// DNS resolution, which is what closes the rebinding gap a hostname check
// leaves open: a name that resolved public once may resolve to 10.0.0.1 on the
// next lookup, and only the dial knows which it got.
func IsPublicDestination(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	// A v4-mapped v6 address (::ffff:10.0.0.1) is the v4 address it wraps, and
	// must be classified as such — comparing it against the v6 registry alone
	// would find no match and admit it.
	address = address.Unmap()
	if address.IsLoopback() || address.IsMulticast() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsInterfaceLocalMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range specialUsePrefixes {
		// Prefixes of the other family never contain this address, so one pass
		// over both registries is correct without branching on family.
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

// IsPublicDestinationDialAddress reports whether a dialer's "host:port" names
// a public destination. A address it cannot parse is refused: a guard that
// cannot tell what it is about to reach must not admit it.
func IsPublicDestinationDialAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	parsed, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return IsPublicDestination(parsed)
}
