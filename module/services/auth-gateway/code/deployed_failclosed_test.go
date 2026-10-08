package main

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// SP-GW-09: anonymous and authentication-factor budgets key on the originating
// client, and a cell with no trusted-proxy range refuses to start. A deployed
// gateway sits behind an ingress and a frontend hop, so with no trusted range the
// client address is the frontend's own pod and every caller shares one bucket.
func TestDeployedRefusesEmptyProxyTrust(t *testing.T) {
	for _, raw := range []string{"", "   ", ",", " , "} {
		err := requireProxyTrust(raw, false)
		require.Error(t, err, "an empty range %q must refuse a deployed boot", raw)
		require.Contains(t, err.Error(), "TRUSTED_PROXY_CIDRS",
			"the refusal must name the key an operator has to set")
	}
}

// Local development has no ingress hop and the peer IS the client.
func TestLocalRuntimeAcceptsAnEmptyProxyTrust(t *testing.T) {
	require.NoError(t, requireProxyTrust("", true))
}

// A configured range still has to parse, and a configured range admits the boot.
func TestProxyTrustBootChecksAdmitAndRefuseTheRightThings(t *testing.T) {
	require.NoError(t, requireProxyTrust("10.0.0.0/8,192.0.2.10", false))
	require.Error(t, requireProxyTrust("10.0.0.0/8,not-an-address", false))
	require.Error(t, requireProxyTrust("not-an-address", true),
		"a malformed range is a configuration error in local development too")
}

// The collapse itself, as a property rather than as a boot check: with a trusted
// hop configured, two clients arriving through the same frontend pod key on
// themselves. The auditor's reproducer showed both keying on the pod.
func TestTrustedProxyRangeKeysDistinctClientsDistinctly(t *testing.T) {
	trust := newProxyTrust("10.0.0.0/8")

	first := httptest.NewRequest("POST", "/v1/auth/mfa/complete", nil)
	first.RemoteAddr = "10.4.0.7:44001" // the frontend pod
	first.Header.Set("X-Forwarded-For", "198.51.100.44")

	second := httptest.NewRequest("POST", "/v1/auth/mfa/complete", nil)
	second.RemoteAddr = "10.4.0.7:44002" // the same frontend pod
	second.Header.Set("X-Forwarded-For", "203.0.113.10")

	require.Equal(t, "198.51.100.44", trust.clientIP(first))
	require.Equal(t, "203.0.113.10", trust.clientIP(second))
	require.NotEqual(t, trust.clientIP(first), trust.clientIP(second),
		"two clients behind one hop must not share one budget")

	// And with no trusted hop they do collapse, which is what the boot refusal
	// above exists to prevent — asserted so the refusal's reason is not merely
	// claimed.
	untrusting := newProxyTrust("")
	require.Equal(t, untrusting.clientIP(first), untrusting.clientIP(second))
	require.Equal(t, "10.4.0.7", untrusting.clientIP(first))
}

// A revoker with no store answers "not revoked" to every question, so a signed-out
// session, a killed device and an ended impersonation window all keep
// authenticating until their tokens expire — and nothing on the request path can
// tell that from an empty revocation set, so the gateway reports healthy.
func TestDeployedRefusesAMissingRevocationStore(t *testing.T) {
	_, err := newRevoker("", defaultRevocationCacheTTL, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "revocation")

	revoker, err := newRevoker("", defaultRevocationCacheTTL, true)
	require.NoError(t, err, "local development has no cache service")
	require.IsType(t, noopRevoker{}, revoker)
}

// A per-replica bucket is not the budget it reports: with N replicas the effective
// limit is N times the configured one, and which replica a caller lands on decides
// their share. For an authentication-factor budget that is the difference between
// a bounded number of attempts and a bounded number per replica.
func TestDeployedRefusesAMissingRateLimitStore(t *testing.T) {
	t.Setenv("REDIS_URL", "")

	_, err := NewRateLimiter(1000)
	require.Error(t, err)
	require.Contains(t, err.Error(), "shared store")

	limiter, err := NewRateLimiter(1000, WithInProcessBackendAllowed(true))
	require.NoError(t, err, "local development has no cache service")
	t.Cleanup(limiter.Stop)
}
