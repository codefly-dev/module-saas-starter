package adapters

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// trustedProxies is the set of CIDR ranges whose members are allowed to
// speak for a client via X-Forwarded-For. Empty means "trust no proxy":
// the direct transport peer is used verbatim and any XFF header is
// ignored — the safe default when the api is reached directly, since a
// client can set XFF to anything.
var trustedProxies []netip.Prefix

// WithTrustedProxies installs the parsed TRUSTED_PROXY_CIDRS allowlist that
// clientIP consults when attributing anonymous traffic to a source IP.
func WithTrustedProxies(cidrs []netip.Prefix) { trustedProxies = cidrs }

// ParseTrustedProxyCIDRs parses the comma-separated TRUSTED_PROXY_CIDRS
// value into prefixes, failing on any malformed entry so a typo can't
// silently widen who we trust for X-Forwarded-For.
func ParseTrustedProxyCIDRs(raw string) ([]netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS: invalid CIDR %q: %w", part, err)
		}
		out = append(out, prefix.Masked())
	}
	return out, nil
}

// clientIP resolves the client address a request should be attributed to.
// remoteAddr is the transport peer ("ip:port" or "ip"); xff is the raw
// X-Forwarded-For header. XFF is only consulted when the direct peer is
// itself a trusted proxy — otherwise a caller could forge the header to
// dodge the per-IP budget. Returns "" when no address can be parsed.
func clientIP(remoteAddr, xff string) string {
	peer := parsePeerAddr(remoteAddr)
	if !peer.IsValid() {
		return ""
	}
	if !isTrustedProxy(peer) {
		return peer.String()
	}
	// Walk X-Forwarded-For right-to-left (closest hop first) for the first
	// address outside the trusted set — that hop is the real client.
	hops := strings.Split(xff, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			continue
		}
		addr = addr.Unmap()
		if !isTrustedProxy(addr) {
			return addr.String()
		}
	}
	// Every hop was a trusted proxy (or the header was empty) — fall back
	// to the direct peer rather than inventing an address.
	return peer.String()
}

func isTrustedProxy(addr netip.Addr) bool {
	for _, prefix := range trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func parsePeerAddr(remoteAddr string) netip.Addr {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return netip.Addr{}
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		remoteAddr = host
	}
	addr, err := netip.ParseAddr(remoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}
