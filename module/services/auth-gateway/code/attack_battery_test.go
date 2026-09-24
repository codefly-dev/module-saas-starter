package main

import (
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
