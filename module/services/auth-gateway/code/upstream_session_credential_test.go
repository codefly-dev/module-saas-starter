package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The host access token IS the person's whole session: one host-wide audience,
// the person's full authority, valid at every other upstream and at the host's own
// API. Forwarding it verbatim meant a compromised module or solution pod — or one
// logged request header — yielded replayable full-authority sessions for every
// viewer who had used it.
//
// What an upstream receives instead is the identity ext_authz stamped, which names
// the person without carrying their authority.
func TestModuleUpstreamReceivesNoSessionBearer(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	fake := declareModule(t, gw, "documents")

	token := signValidToken(t, priv)
	request := httptest.NewRequest(http.MethodGet, "/v1/documents/entries", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Cookie", "codefly_rt=a-refresh-token; codefly_session=1")
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, fake.lastHeaders.Get("Authorization"),
		"a module upstream must not receive the person's session credential")
	require.Empty(t, fake.lastHeaders.Get("Cookie"),
		"nor the cookie the gateway has already resolved identity from")
	require.NotEmpty(t, fake.lastHeaders.Get("x-user-id"),
		"it still learns who the person is")
	require.NotEmpty(t, fake.lastHeaders.Get("x-org-id"))
}

// The public solution surface — the module-federation manifest, remote entry and
// chunks — is fetched by the browser's module loader. It carries no bearer when the
// loader is working as designed, but a page that had one attached forwarded it to
// an unauthenticated path, which is the verifier's reproducer.
func TestPublicSolutionPathForwardsNoCredential(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	fake := registerSolutionUpstream(t, gw, "audit")

	request := httptest.NewRequest(http.MethodGet, "/solutions/audit/assets/remoteEntry.js", nil)
	request.Header.Set("Authorization", "Bearer a-host-session-token")
	request.Header.Set("Cookie", "codefly_rt=a-refresh-token")
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, fake.lastHeaders.Get("Authorization"))
	require.Empty(t, fake.lastHeaders.Get("Cookie"))
}

// The host's own API is the one upstream where the session IS the credential, so
// the strip must not reach a catalog route. Without this control the test above
// would pass on a gateway that forwarded nothing to anybody.
func TestCatalogRouteStillCarriesTheSessionToAccounts(t *testing.T) {
	gw, api, _, priv := newGatewayHarness(t)

	token := signValidToken(t, priv)
	request := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "Bearer "+token, api.lastHeaders.Get("Authorization"))
}
