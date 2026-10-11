package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// A resource-bound token is confined to the solution its resource names, on EVERY
// path (SA-F-MCPAUD, the escape half).
//
// The confinement used to run only under `/solutions/<id>/`, so a token the host
// delivered to one solution's runtime was admitted — as a full session, with the
// person's whole authority — at this host's own API, at another declared module's
// routes, and at the Work Context surface that mints further capabilities. A
// compromised runtime could therefore act as every viewer who had used it,
// anywhere, which is the opposite of what a resource indicator is for.
//
// The second audience such a token carries is this host's, and it is there so the
// token VERIFIES at all: the minter stamps it and its own VerifyAccess checks it.
// Reading that parser requirement as an authorization grant is what made the
// escape. Nothing legitimate needs it either way — a runtime calls this host with
// the module credential it was provisioned, which `/modules/_work-context` takes
// instead of a person's bearer.
func TestResourceTokenIsConfinedToItsOwnSolution(t *testing.T) {
	for _, path := range []string{
		"/v1/users",                  // this host's own API
		"/v1/documents/entries",      // another declared module's routes
		"/solutions/other/v1/things", // another solution's data
		"/solutions/other/mcp",       // another solution's tool endpoint
	} {
		t.Run(path, func(t *testing.T) {
			withPublicBase(t, testPublicBase)
			gw, api, _, priv := newGatewayHarness(t)
			registerSolutionUpstream(t, gw, "example")
			registerSolutionUpstream(t, gw, "other")
			declareModule(t, gw, "documents")

			// Issued for `example`, presented elsewhere.
			token := signResourceToken(t, priv, testPublicBase+"/api/solutions/example/proxy/mcp")
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer "+token)
			recorder := httptest.NewRecorder()
			gw.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusUnauthorized, recorder.Code,
				"a token issued for one resource must not be admitted at %s", path)
			require.Nil(t, api.lastHeaders,
				"and the upstream must not be reached at all")
		})
	}
}

// The control: the same token at the resource it DOES name is admitted. Without
// it the test above would pass on a gateway that refused every resource token.
func TestResourceTokenIsAdmittedAtItsOwnSolution(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	token := signResourceToken(t, priv, testPublicBase+"/api/solutions/example/proxy/mcp")
	request := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotNil(t, example.lastHeaders)
}

// An ordinary host session is NOT confined: it names no resource, and it is the
// credential every surface except a tool endpoint accepts. This is the other
// control — confinement must narrow resource tokens, not every token.
func TestAnUnboundSessionIsNotConfined(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, api, _, priv := newGatewayHarness(t)

	request := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	request.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotNil(t, api.lastHeaders)
}

// An API key names no resource and cannot be bound to one, so it is refused at a
// tool endpoint — which it reached by never being asked: the API-key branch
// returns before the resource rules, which live in the JWT path.
func TestAnAPIKeyIsRefusedAtAToolEndpoint(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, _ := newGatewayHarness(t)
	example := registerSolutionUpstream(t, gw, "example")

	request := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	request.Header.Set("Authorization", "Bearer cfly_sk_anything")
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	require.Contains(t, recorder.Body.String(), "names no resource")
	require.Nil(t, example.lastHeaders, "and the runtime is not reached")
}
