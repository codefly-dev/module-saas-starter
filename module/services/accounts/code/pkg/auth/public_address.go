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
// both families. Every guard on a tenant-directed egress path answers through
// it: callable sources (datasource/apisource), the crawler and object-store
// connectors, outbound webhooks and the Slack notifier that shares their client
// (business.WebhookEndpointPolicy), and Client ID Metadata Document fetches.
// Each of those guards' tests holds the vectors in auth/egresstest.
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
// Inside the space IANA allocates for global unicast it is deliberately a
// denylist of non-public space rather than an allowlist of public space: ranges
// unassigned today become public assignments tomorrow, and a deployment must
// not need a release to reach a newly assigned host. For IPv6 that space is
// 2000::/3 (the IANA IPv6 Address Space registry), and an IPv6 address outside
// it is refused before the registries are read. Everything else there is
// multicast, link-local, unique local or "Reserved by IETF" — space no public
// route reaches, yet Go calls it global unicast. That includes the deprecated
// IPv4-compatible form ::a.b.c.d (RFC 4291 §2.5.5.1), which embeds an IPv4
// address exactly as NAT64 does, and deprecated site-local fec0::/10 (RFC 3879),
// neither of which the special-purpose registry lists. IPv4 has no unallocated
// remainder of that kind: its reserved block, 240.0.0.0/4, is in the registry.
//
// The IPv6 transition prefixes that embed an IPv4 address — NAT64
// (64:ff9b::/96, 64:ff9b:1::/48), 6to4 (2002::/16) and Teredo (2001::/32,
// inside 2001::/23) — are refused outright, not decoded and judged by the IPv4
// address they carry. Decoding cannot be complete: RFC 6052 lets an operator
// pick any network-specific NAT64 prefix, which is indistinguishable from
// ordinary global unicast, and the local-use /48 admits any RFC 6052 layout.
// Even a correctly decoded public address is reached through a translator or
// relay that sits on the host's own network, so the first hop is not the
// public internet. 6to4 is deprecated (RFC 7526) and Teredo obfuscates the
// address it carries. Every public IPv4 destination stays reachable directly by
// its own address, so refusing costs one known case: a host on an IPv6-only
// network with DNS64 cannot reach an IPv4-only provider through a synthesized
// NAT64 address.

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

// globalUnicastIPv6 is the only IPv6 block IANA has allocated for global
// unicast. The registry entries outside it (::/128, NAT64, fc00::/7 and the
// rest) stay listed above so the inventory still reads as the registry, and so
// either check alone refuses them.
var globalUnicastIPv6 = netip.MustParsePrefix("2000::/3")

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
	if address.Is6() && !globalUnicastIPv6.Contains(address) {
		return false
	}
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
