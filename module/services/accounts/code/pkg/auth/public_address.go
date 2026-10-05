package auth

import (
	"net"
	"net/netip"
)

// Whether an address is on the public internet.
//
// The invariant: a destination this host may be asked to fetch from reaches
// only the public internet. This classifier is the single authority for that
// question, and it enumerates the IANA special-purpose address registries for
// both families.
//
// It does not compose Go's net.IP predicates, because they answer a different
// question. net.IP.IsGlobalUnicast is not a public-routability test — it
// answers "is this a unicast address outside the loopback, link-local and
// multicast sets" — and IsPrivate covers the three RFC 1918 ranges. Their
// conjunction therefore classes a number of special-purpose registry ranges as
// public, so a predicate built from them would not mean what this one must
// mean. The registries are the specification of "not the public internet", so
// they are what this reads.
//
// It is deliberately a denylist of non-public space rather than an allowlist of
// public space: ranges unassigned today become public assignments tomorrow, and
// a deployment must not need a release to reach a newly assigned host.

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
	// The dummy prefix. Inside 100::/8 but NOT inside the discard-only /64
	// above, so that /64 alone does not cover it.
	"100:0:0:1::/64",
	// IETF protocol assignments, as one block rather than per sub-block. It
	// covers Teredo (2001::/32), benchmarking (2001:2::/48), both ORCHID
	// blocks, the anycast singletons AND the unassigned remainder; enumerating
	// the sub-blocks individually would not cover that remainder, which is why
	// the whole /23 is listed. 2001:db8::/32 is OUTSIDE this /23 and stays
	// listed separately; ordinary global unicast such as 2001:4860::/32 is
	// outside it too, so this does not over-refuse.
	"2001::/23",
	"2001:db8::/32", // documentation
	"2002::/16",     // 6to4
	"3fff::/20",     // documentation (RFC 9637)
	"5f00::/16",     // segment routing
	"fc00::/7",      // unique local
	"fe80::/10",     // link-local unicast
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
	// A zone makes this a SCOPED address, reachable only on the interface it
	// names. A scope identifier is meaningless for a destination on the public
	// internet, so its presence is itself disqualifying and such an address is
	// refused here, before the registries are consulted at all.
	//
	// Two reasons it is refused rather than stripped. netip.Prefix.Contains
	// reports false for every zoned address, so a zoned address compared
	// against the prefixes below would match no entry in either registry; and
	// stripping the zone would classify an address the dialer is then not going
	// to use. Refusing is the only answer that stays true of the destination.
	if address.Zone() != "" {
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
