//go:build integration

package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Real login, key verification, gateway routing and Accounts handlers. A refused
// review of an absent target proves handler reachability without creating an
// installation or bypassing the separately qualified signed delivery fixture.
func TestIntegration_Gateway_CombinedInstallationAuthentication(t *testing.T) {
	base, close := newTestGateway(t)
	defer close()
	const org = "00000000-0000-7000-8000-0000000000c1"
	login := func(fixture string) string {
		code, body, _ := doRequest(t, http.MethodPost, base+"/v1/auth/authenticate", map[string]any{"provider": "email", "fixture": map[string]any{"token": fixture}}, "")
		require.Equal(t, http.StatusOK, code)
		return decodeLoginBody(t, body).AccessToken
	}
	admin, member := login("dev-admin"), login("dev-bob")
	for _, tc := range []struct {
		path      string
		body      map[string]any
		adminCode int
		reason    string
	}{
		{"/v1/solution-installations/review", map[string]any{"orgId": org, "targetId": "550e8400-e29b-41d4-a716-446655440000"}, http.StatusBadRequest, "reviewed solution target is not available"},
		{"/v1/solution-installations", map[string]any{"orgId": org, "review": map[string]any{}}, http.StatusForbidden, "installation review belongs to another actor or organization"},
		{"/saas.accounts.v1.InstallationService/ReviewSolutionInstall", map[string]any{"orgId": org, "targetId": "550e8400-e29b-41d4-a716-446655440000"}, http.StatusBadRequest, "reviewed solution target is not available"},
		{"/saas.accounts.v1.InstallationService/InstallReviewedSolution", map[string]any{"orgId": org, "review": map[string]any{}}, http.StatusForbidden, "installation review belongs to another actor or organization"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			for _, bearer := range []string{"", "invalid-session"} {
				code, _, _ := doRequest(t, http.MethodPost, base+tc.path, tc.body, bearer)
				require.Equal(t, http.StatusUnauthorized, code)
			}
			code, body, _ := doRequest(t, http.MethodPost, base+tc.path, tc.body, member)
			require.Equal(t, http.StatusForbidden, code, string(body))
			code, body, _ = doRequest(t, http.MethodPost, base+tc.path, tc.body, admin)
			require.Equal(t, tc.adminCode, code, string(body))
			require.Contains(t, string(body), tc.reason)
		})
	}
}
