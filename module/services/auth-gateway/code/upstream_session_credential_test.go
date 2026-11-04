package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The cookie never reaches a declared upstream, whatever the bearer does.
//
// These were one decision and the broader one carried the narrower silently: the
// tool path skipped the whole removal, so a resource token presented with a
// session cookie forwarded the cookie too. They are two decisions now, and this
// is the one with no consumer contract to break — this perimeter refuses a cookie
// AS a credential by name, so an upstream can authenticate nothing with it, while
// the cookie carries the longest-lived value a browser holds.
func TestDeclaredUpstreamNeverReceivesTheCallerCookie(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"a solution data route", "/solutions/audit/v1/audit/logs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw, _, _, priv := newGatewayHarness(t)
			fake := registerSolutionUpstream(t, gw, "audit")

			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
			request.Header.Set("Cookie", "codefly_rt=a-refresh-token; codefly_session=1")
			recorder := httptest.NewRecorder()
			gw.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Empty(t, fake.lastHeaders.Get("Cookie"),
				"the cookie the gateway has already resolved identity from must not travel")
			require.NotEmpty(t, fake.lastHeaders.Get("x-user-id"),
				"the upstream still learns who the person is")
		})
	}
}

// The MCP tool path is the one the cookie leaked through, so it is asserted in
// its own right: a resource-bound token is forwarded there, and the cookie beside
// it is not.
func TestToolPathForwardsTheResourceTokenWithoutTheCookie(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	token := signResourceToken(t, priv, testPublicBase+"/api/solutions/example/proxy/mcp")
	request := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Cookie", "codefly_rt=a-refresh-token")
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "Bearer "+token, example.lastHeaders.Get("Authorization"),
		"a resource-bound token is what the runtime authenticates the caller by")
	require.Empty(t, example.lastHeaders.Get("Cookie"),
		"the cookie is withheld on this path too, which it was not")
}

// The anonymous module-federation surface is the one place the whole session
// credential is withheld, because it is the one place no ext_authz runs: a bearer
// arriving here was never authenticated for this upstream and the loader fetches
// these bytes with no credential at all.
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
// neither removal may reach a catalog route. Without this control the assertions
// above would pass on a gateway that forwarded nothing to anybody.
func TestCatalogRouteStillCarriesTheSessionToAccounts(t *testing.T) {
	gw, api, _, priv := newGatewayHarness(t)

	token := signValidToken(t, priv)
	request := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Cookie", "codefly_session=1")
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "Bearer "+token, api.lastHeaders.Get("Authorization"))
	require.Equal(t, "codefly_session=1", api.lastHeaders.Get("Cookie"))
}
