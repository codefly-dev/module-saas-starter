package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tenant-isolation attack battery, perimeter half. Each test replays one attack
// from the tenant-isolation audit against the gateway and asserts the secure
// outcome; each was red before the fix it names.

// TestAttack_PerimeterStripsGrpcMetadataIdentityHeaders: grpc-gateway turns any
// `Grpc-Metadata-<name>` header into `<name>` metadata, so on a REST route this
// spelling of an identity header is the identity header. The gateway strips
// X-User-Id and restamps it, forwards `Grpc-Metadata-X-User-Id` untouched, and
// stamps the gateway token that makes accounts trust whatever identity arrives.
// Accounts then reads whichever x-user-id value is ordered first — the caller's
// in roughly one request in nine.
func TestAttack_PerimeterStripsGrpcMetadataIdentityHeaders(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	for _, header := range canonicalUpstreamAuthHeaders {
		req.Header.Set("Grpc-Metadata-"+header, "attacker-chosen")
	}
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	for name := range apiFake.lastHeaders {
		require.Falsef(t, strings.HasPrefix(strings.ToLower(name), "grpc-metadata-"),
			"caller-supplied %s reached the accounts upstream", name)
	}
}

// TestAttack_TheBearerLessArmStripsGrpcMetadataIdentityHeaders: the arm runs no
// ext_authz and stamps no identity, so a grpc-metadata spelling of one is the
// only identity an upstream could read. It never arrives.
func TestAttack_TheBearerLessArmStripsGrpcMetadataIdentityHeaders(t *testing.T) {
	gw, module, _, kid, wcPriv := operationHarness(t)
	req := onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal)))
	req.Header.Set("Grpc-Metadata-X-User-Id", "attacker-chosen")
	req.Header.Set("Grpc-Metadata-X-Org-Id", "attacker-chosen")

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	for name := range module.lastHeaders {
		require.Falsef(t, strings.HasPrefix(strings.ToLower(name), "grpc-metadata-"),
			"caller-supplied %s reached the module upstream", name)
	}
}

// TestAttack_AnyAuthorizationSchemeLeavesTheArm: the arm is selected only with no
// Authorization header at all. A Basic credential is not "no bearer"; it takes
// the person path, which accepts no such scheme.
func TestAttack_AnyAuthorizationSchemeLeavesTheArm(t *testing.T) {
	gw, module, installations, kid, wcPriv := operationHarness(t)
	req := onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal)))
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Zero(t, module.calls)
	require.Empty(t, admissionOf(t, gw).calls, "the arm was not selected")
	require.Empty(t, installations.calls)
}

// TestAttack_TheBearerLessArmForwardsNoProxyCredential: a Proxy-Authorization
// the caller sent is not a credential this gateway reads, and never reaches the
// module.
func TestAttack_TheBearerLessArmForwardsNoProxyCredential(t *testing.T) {
	gw, module, _, kid, wcPriv := operationHarness(t)
	req := onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal)))
	req.Header.Set("Proxy-Authorization", "Basic cHJveHk6c2VjcmV0")

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Empty(t, module.lastHeaders.Values("Proxy-Authorization"))
}

// TestAttack_TheBearerLessArmIsMeteredOnItsSignedTenant: with no X-Org-Id the
// generic key would be the waypoint's address, one bucket for every tenant on a
// cell. The arm's budget is the tenant the capability is signed for, in its own
// namespace, read after the strip: a caller's X-Org-Id and its source address
// open no new bucket. Exhausting it refuses before accounts is asked anything.
func TestAttack_TheBearerLessArmIsMeteredOnItsSignedTenant(t *testing.T) {
	gw, module, installations, kid, wcPriv := operationHarness(t)
	backend := &memoryBackend{}
	gw.rateLimiter = &RateLimiter{backend: backend, limit: 2, burst: 1, stop: make(chan struct{}), proxies: newProxyTrust("")}
	capability := operationCapability(t, kid, wcPriv, "documents", publisherSeal)
	logs := captureLog(t)

	serve := func(i int) int {
		req := onWorkloadEndpoint(publishRequest(capability))
		req.Header.Set("X-Org-Id", fmt.Sprintf("caller-org-%d", i))
		req.RemoteAddr = fmt.Sprintf("198.51.100.%d:1234", i+1)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		return w.Code
	}
	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusOK, serve(i), "request %d is within limit plus burst", i)
	}
	require.Equal(t, http.StatusTooManyRequests, serve(3))
	require.Equal(t, 3, module.calls)
	require.Len(t, admissionOf(t, gw).calls, 3, "a limited request never reaches the issuer")
	require.Len(t, installations.calls, 3, "nor the installation read")

	var keys []string
	backend.windows.Range(func(key, _ any) bool {
		keys = append(keys, key.(string))
		return true
	})
	require.Len(t, keys, 1, "one bucket, whatever X-Org-Id and source address the caller sent")
	require.True(t, strings.HasPrefix(keys[0], "rl:workload-operation:"+operationTenant+":"), keys[0])
	require.Contains(t, logs.String(), "reason=workload_operation_limited_or_origin")
}

// TestAttack_TheBearerLessArmFailsClosedWithoutItsLimiter: the arm's budget is a
// security budget. A limiter backend that cannot answer refuses the request; it
// does not wave it through to the issuer or the installation read.
func TestAttack_TheBearerLessArmFailsClosedWithoutItsLimiter(t *testing.T) {
	gw, module, installations, kid, wcPriv := operationHarness(t)
	gw.rateLimiter = &RateLimiter{backend: failingRateLimitBackend{}, limit: 100, burst: 20, stop: make(chan struct{}), proxies: newProxyTrust("")}

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal))))

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "rate limit service unavailable")
	require.Zero(t, module.calls)
	require.Empty(t, admissionOf(t, gw).calls)
	require.Empty(t, installations.calls)
}
