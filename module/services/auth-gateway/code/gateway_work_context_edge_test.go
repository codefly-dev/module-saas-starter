package main

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
)

// THE EDGE RULE (issue #952): resolve the route, then judge the capability
// against THAT route's audience.
//
// Before this, the edge verified a presented Work Context before any routing and
// with no audience expectation at all — so a capability minted for one solution
// verified cleanly on another solution's route and was forwarded there, and the
// callee's own audience check was the only thing that could notice. The host now
// derives the expected audience from the route it resolved and refuses the
// mismatch at the edge.

// mintWorkContextAudience signs a token for an arbitrary audience, which is what
// an A-for-B test needs: the same key, the same window, a different consumer.
func mintWorkContextAudience(t *testing.T, kid string, priv ed25519.PrivateKey, audience string) workcontext.WorkContextToken {
	t.Helper()
	signer, err := workcontext.NewWorkContextSigner(workcontext.WorkContextSignerOptions{
		Issuer:     "saas-starter",
		KeyID:      kid,
		PrivateKey: priv,
	})
	require.NoError(t, err)
	token, _, err := signer.StartTask(workcontext.StartTaskInput{
		Audience:         audience,
		TenantID:         "tenant-1",
		OwnerPrincipalID: "user-1",
		TaskID:           "task-1",
		SessionID:        "session-1",
		AuthorityScopes: []*basev0.WorkScopeV1{
			{ResourceKind: "audit", Actions: []string{"read"}},
		},
	})
	require.NoError(t, err)
	return token
}

// edgeHarness wires a gateway with a working Work Context verifier, and hands back
// the accounts upstream so a test can assert the capability never reached it.
func edgeHarness(t *testing.T) (*Gateway, *fakeUpstream, ed25519.PrivateKey, ed25519.PrivateKey, string) {
	t.Helper()
	gw, apiFake, _, accessPriv := newGatewayHarness(t)
	pub, wcPriv := mustEd25519(t)
	const kid = "key-1"
	server := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{kid: pub}), nil)
	gw.workContext = newWorkContextVerifier(server.URL)
	return gw, apiFake, accessPriv, wcPriv, kid
}

// Rule 1 and rule 5 together: a capability minted for ONE solution is refused on
// ANOTHER solution's route, and refused as an invalid capability rather than as a
// missing route.
//
// The two halves matter separately. A 404 would say the route does not exist,
// which is false and sends an operator to the registry; the 401 says the
// capability is not valid here, which is what happened.
func TestGatewayEdge_ACapabilityForOneSolutionIsRefusedOnAnothers(t *testing.T) {
	gw, _, accessPriv, wcPriv, kid := edgeHarness(t)
	registerSolutionUpstream(t, gw, "example-go")
	fake := registerSolutionUpstream(t, gw, "other-solution")

	// Minted for example-go's binding, presented to other-solution's route.
	foreign := mintWorkContextAudience(t, kid, wcPriv,
		solutionRouteAudience("acme.test.example-go"))
	req := httptest.NewRequest(http.MethodGet, "/solutions/other-solution/data", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, accessPriv))
	req.Header.Set(workcontext.WorkContextHeaderName, foreign.Encoded())
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code,
		"A-for-B must be refused as an invalid capability, not answered as a missing route")
	require.NotEqual(t, http.StatusNotFound, w.Code)
	require.Nil(t, fake.lastHeaders, "the foreign capability must not reach the upstream")

	// And the SAME token on its own route is forwarded, so the refusal above is
	// about the audience and not about the token.
	own := registerSolutionUpstream(t, gw, "example-go")
	ok := httptest.NewRequest(http.MethodGet, "/solutions/example-go/data", nil)
	ok.Header.Set("authorization", "Bearer "+signValidToken(t, accessPriv))
	ok.Header.Set(workcontext.WorkContextHeaderName, foreign.Encoded())
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, ok)
	require.Equal(t, http.StatusOK, w.Code,
		"the same capability on its own binding's route must be accepted, or the test proves nothing about the audience")
	require.Equal(t, foreign.Encoded(), own.lastHeaders.Get(workcontext.WorkContextHeaderName))
}

// Rule 2: a path that resolves to NO route is answered as the unexposed path it
// is, and the verifier is never called.
//
// Asserted on the JWKS server's hit count, not on the status code: a 404 proves
// the answer, but only "the key set was never fetched" proves the ordering — that
// the route was resolved first and the token never judged.
func TestGatewayEdge_AnUnresolvedRouteNeverReachesTheVerifier(t *testing.T) {
	gw, _, accessPriv, wcPriv, kid := edgeHarness(t)
	var hits int64
	pub, _ := mustEd25519(t)
	server := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{kid: pub}), &hits)
	gw.workContext = newWorkContextVerifier(server.URL)

	token := mintWorkContextAudience(t, kid, wcPriv, "solution:anything")
	req := httptest.NewRequest(http.MethodGet, "/nothing/this/host/exposes", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, accessPriv))
	req.Header.Set(workcontext.WorkContextHeaderName, token.Encoded())
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Zero(t, hits, "the key set must not be fetched for a request that resolves to no route")
}

// Rule 4: a capability addressed to the GATEWAY ITSELF is refused.
//
// This gateway is a forwarding hop: it inspects and never consumes. A token
// naming the hop is one nobody will consume, and it is what a caller would mint
// to make the hop treat a capability as its own. Refused by name rather than
// forwarded for a callee to puzzle over.
func TestGatewayEdge_ACapabilityAddressedToTheGatewayIsRefused(t *testing.T) {
	gw, apiFake, accessPriv, wcPriv, kid := edgeHarness(t)

	token := mintWorkContextAudience(t, kid, wcPriv, gatewayAudience)
	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, accessPriv))
	req.Header.Set(workcontext.WorkContextHeaderName, token.Encoded())
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, apiFake.lastHeaders, "a capability addressed to the hop must not be forwarded")

	// Directly, so the refusal is pinned to the rule and not to a routing accident.
	err := gw.workContext.Verify(context.Background(), token, "")
	require.ErrorIs(t, err, workcontext.ErrWorkContextInvalid)
	require.Contains(t, err.Error(), "forwarding hop")
}

// Rule 3: a declared MODULE route expects the module capability audience, and a
// solution capability is refused there.
func TestGatewayEdge_AModuleRouteExpectsTheModuleAudience(t *testing.T) {
	gw, _, accessPriv, wcPriv, kid := edgeHarness(t)
	fake := declareModule(t, gw, "example-module")

	solutionToken := mintWorkContextAudience(t, kid, wcPriv, solutionRouteAudience("acme.test.example-go"))
	req := httptest.NewRequest(http.MethodGet, "/v1/example-module/things", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, accessPriv))
	req.Header.Set(workcontext.WorkContextHeaderName, solutionToken.Encoded())
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, fake.lastHeaders)

	moduleToken := mintWorkContextAudience(t, kid, wcPriv, moduleCapabilitiesAudience)
	ok := httptest.NewRequest(http.MethodGet, "/v1/example-module/things", nil)
	ok.Header.Set("authorization", "Bearer "+signValidToken(t, accessPriv))
	ok.Header.Set(workcontext.WorkContextHeaderName, moduleToken.Encoded())
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, ok)
	require.Equal(t, http.StatusOK, w.Code,
		"the module audience must be accepted on a module route, or the refusal above proves nothing")
}

// The solution route's expected audience is keyed on the BINDING, not the alias.
//
// An alias is deliberately reusable, so an audience keyed on it would let a
// replacement binding under the same route be addressed as its predecessor — the
// same reuse hole the artifact-approval authority and the boundary derivation both
// had to close. Asserted by value against the prefix, so a change to the shape
// moves this test rather than letting it pass against a stale spelling.
func TestGatewayEdge_TheSolutionAudienceIsKeyedOnTheBinding(t *testing.T) {
	require.Equal(t, "solution:acme.test.example", solutionRouteAudience("acme.test.example"))
	require.Equal(t, solutionAudiencePrefix+"b", solutionRouteAudience("b"))
	require.Empty(t, solutionRouteAudience(""),
		"no binding means no expectation; the surrounding refusals are what make that unreachable")
	// The two constants this gateway cannot import from accounts, pinned by value.
	require.Equal(t, "solution:", solutionAudiencePrefix)
	require.Equal(t, "module-capabilities", moduleCapabilitiesAudience)
}
