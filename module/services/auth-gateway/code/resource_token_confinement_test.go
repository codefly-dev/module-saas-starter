package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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
// token VERIFIES at all: the minter stamps it and its own VerifiedAccess checks
// it. Reading that parser requirement as an authorization grant is what made the
// escape.
//
// TWO host surfaces are admitted, each in its own company, and neither is
// exercised here: the Work Context mint (see
// TestResourceTokenAtTheWorkContextMintIsAdmittedConfined), and a declared module
// route reached together with this person's verified capability for that module
// (see gateway_module_confinement_test.go). Every request below presents NO
// capability, which is why the module path refuses here as well — what this test
// pins is that nothing about the token alone admits it anywhere.
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

// THE MINT, asserted at the decision point rather than through ServeHTTP.
//
// It is admitted, and admitted CONFINED — the resource travels with it so
// accounts can hold what it issues to the consumer the credential was delivered
// to. The runtime's MCP viewer path needs exactly this: it mints a viewer-owned
// capability for the module its solution declared it consumes, forwarding the
// person's bearer, because its own service credential does not stand in for the
// person.
//
// This test used to assert the OPPOSITE — that the mint refused a resource token
// — and it kept passing after the admission shipped, because `newTestExtAuthz`
// leaves `publicBase` empty and a confined credential is refused in that state
// for an unrelated reason: this process cannot say what any resource identifier
// IS, so it cannot compare one. A test that passes through the path it means to
// exercise being unreachable asserts nothing about it. The public base is
// therefore set here, to the value the production constructor resolves, and both
// states are asserted separately.
//
// The Connect route is not in the test harness's catalog (ServeHTTP answers 404
// before any credential is read), so this runs against ExtAuthz.Check, which is
// where the decision is made and where the real deployment reaches it.
func TestResourceTokenAtTheWorkContextMintIsAdmittedConfined(t *testing.T) {
	withPublicBase(t, testPublicBase)
	s, priv := newTestExtAuthz(t)
	s.publicBase = publicBaseURL()
	require.Equal(t, testPublicBase, s.publicBase,
		"the admission under test is unreachable without a configured public address")

	resource := solutionMCPResource(testPublicBase, "example")
	bound := validClaims(time.Now())
	bound.Audience = jwt.ClaimStrings{"saas-starter", resource}

	resp, err := s.Check(context.Background(), checkReq(
		workContextMintProcedure, map[string]string{
			"authorization": "Bearer " + signClaims(t, priv, bound),
		}))
	require.NoError(t, err)
	require.Nil(t, resp.GetDeniedResponse(),
		"the consumer's own mint is admitted; what it may ISSUE is bounded by accounts")
	require.Equal(t, resource, headerMap(resp)[resourceConfinementHeader],
		"and the resource travels with it, or the mint cannot tell it from an unbound session")

	// The control: an unbound session still mints, and carries an EMPTY
	// confinement rather than none, so accounts reads a value either way.
	ok, err := s.Check(context.Background(), checkReq(
		workContextMintProcedure, map[string]string{
			"authorization": "Bearer " + signClaims(t, priv, validClaims(time.Now())),
		}))
	require.NoError(t, err)
	require.NotNil(t, ok.GetOkResponse(), "an ordinary session still reaches the mint")
	require.Empty(t, headerMap(ok)[resourceConfinementHeader])
}

// Without a configured public address the mint refuses a confined credential,
// and says which of the two refusals it is. An operator fixes a configuration and
// a client re-authorizes; those are different actions, so they are different
// messages.
func TestResourceTokenAtTheMintIsRefusedWithNoConfiguredPublicAddress(t *testing.T) {
	s, priv := newTestExtAuthz(t)
	require.Empty(t, s.publicBase)

	bound := validClaims(time.Now())
	bound.Audience = jwt.ClaimStrings{
		"saas-starter", solutionMCPResource(testPublicBase, "example"),
	}

	resp, err := s.Check(context.Background(), checkReq(
		workContextMintProcedure, map[string]string{
			"authorization": "Bearer " + signClaims(t, priv, bound),
		}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse())
	require.Equal(t, int32(401), int32(resp.GetDeniedResponse().Status.Code))
	require.Contains(t, resp.GetDeniedResponse().GetBody(), "no configured public address")
}
