package adapters

import (
	"net/netip"
	"testing"
)

func mustCIDRs(t *testing.T, raw string) []netip.Prefix {
	t.Helper()
	prefixes, err := ParseTrustedProxyCIDRs(raw)
	if err != nil {
		t.Fatalf("ParseTrustedProxyCIDRs(%q): %v", raw, err)
	}
	return prefixes
}

func TestParseTrustedProxyCIDRs_RejectsMalformed(t *testing.T) {
	if _, err := ParseTrustedProxyCIDRs("10.0.0.0/8, not-a-cidr"); err == nil {
		t.Fatal("expected error for malformed CIDR entry")
	}
	if got, err := ParseTrustedProxyCIDRs("  "); err != nil || got != nil {
		t.Fatalf("blank input: got %v, %v; want nil, nil", got, err)
	}
}

func TestClientIP_NoTrustedProxiesIgnoresXFF(t *testing.T) {
	WithTrustedProxies(nil)
	t.Cleanup(func() { WithTrustedProxies(nil) })

	// Direct client — XFF is attacker-controlled and must be ignored.
	if got := clientIP("203.0.113.7:44321", "1.2.3.4"); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want 203.0.113.7", got)
	}
}

func TestClientIP_TrustedPeerHonorsXFF(t *testing.T) {
	WithTrustedProxies(mustCIDRs(t, "10.0.0.0/8"))
	t.Cleanup(func() { WithTrustedProxies(nil) })

	// Peer is the trusted proxy; the real client is the last XFF hop.
	if got := clientIP("10.1.2.3:9000", "203.0.113.9"); got != "203.0.113.9" {
		t.Errorf("clientIP = %q, want 203.0.113.9", got)
	}
}

func TestClientIP_SkipsTrailingTrustedHops(t *testing.T) {
	WithTrustedProxies(mustCIDRs(t, "10.0.0.0/8"))
	t.Cleanup(func() { WithTrustedProxies(nil) })

	// Two trusted proxies in front; walk right-to-left to the first
	// untrusted address.
	if got := clientIP("10.0.0.1:80", "203.0.113.5, 10.9.9.9, 10.0.0.1"); got != "203.0.113.5" {
		t.Errorf("clientIP = %q, want 203.0.113.5", got)
	}
}

func TestClientIP_UntrustedPeerIgnoresXFF(t *testing.T) {
	WithTrustedProxies(mustCIDRs(t, "10.0.0.0/8"))
	t.Cleanup(func() { WithTrustedProxies(nil) })

	// Peer is not a trusted proxy, so a spoofed XFF is disregarded.
	if got := clientIP("203.0.113.7:1234", "10.0.0.1"); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want 203.0.113.7", got)
	}
}

func TestClientIP_UnparseablePeer(t *testing.T) {
	WithTrustedProxies(nil)
	if got := clientIP("garbage", ""); got != "" {
		t.Errorf("clientIP = %q, want empty", got)
	}
}
