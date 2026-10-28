package main

// THE TWO HEADERS THE CEILING IS RESOLVED FROM, and the two hops they travel on
// (register finding SA-F-MCPAUD, round 4).
//
// `x-codefly-resource` and `x-codefly-installation-id` are the whole of what
// accounts has to decide a confined mint with: which consumer the credential was
// delivered to, and which installation of it this organization consented to. A
// caller that could set either picks its own ceiling, and a hop that failed to
// stamp the second leaves accounts with no ceiling at all.
//
// The round-3 test covered one route — the declared module route — and that route
// stamps the installation itself, after the strip. So the caller's value is
// overwritten there whether or not the header is on the strip list at all, and a
// suite with only that test stayed green when the name was removed from
// `untrustedAuthHeaders` (which also drops it from Envoy's `HeadersToRemove`) and
// when the mint stopped stamping it. These cover the three places that test
// cannot reach: the mint route, an ordinary session, and the Envoy answer.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// mintRequestOf is the StartTask request the consumer's runtime sends: the
// viewer's bearer, the org and session the gateway projected for it, and the
// audience of the module its solution declared it consumes.
func mintRequestOf(caller confinedCaller, forged http.Header) *http.Request {
	body, _ := json.Marshal(map[string]any{
		"orgId":    caller.org,
		"taskId":   uuid.Must(uuid.NewV7()).String(),
		"audience": "documents",
		"authorityScopes": []map[string]any{
			{"resourceKind": "documents", "actions": []string{"read"}},
		},
	})
	req := httptest.NewRequest(http.MethodPost, workContextMintProcedure, bytes.NewReader(body))
	req.Header.Set("authorization", "Bearer "+caller.bearer)
	req.Header.Set("content-type", "application/json")
	for name, values := range forged {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	return req
}

// THE MINT, through the real handler: it is told which consumer and which
// installation, which is the ceiling accounts resolves. The module is not read
// through this gateway; a module call goes through the hop.
func TestCeilingHeaders_TheMintIsToldTheConsumerAndItsInstallation(t *testing.T) {
	gw, accounts, caller, _ := confinedMintHarness(t)

	mint := httptest.NewRecorder()
	gw.ServeHTTP(mint, mintRequestOf(caller, nil))

	require.Equal(t, http.StatusOK, mint.Code, mint.Body.String())
	require.Equal(t, solutionMCPResource(testPublicBase, "example"),
		accounts.lastHeaders.Get(resourceConfinementHeader),
		"the mint is told which consumer this credential was delivered to")
	require.Equal(t, "install-"+fakeSolutionTarget("example"),
		accounts.lastHeaders.Get(installationConfinementHeader),
		"and WHICH INSTALLATION of it admitted the request, which is the ceiling accounts resolves")
}

// confinedMintHarness wires a gateway with one declared solution whose
// resource the credential names, entitled to the caller's organization: the
// consumer a confined credential mints for.
func confinedMintHarness(t *testing.T) (gw *Gateway, accounts *fakeUpstream, caller confinedCaller, access ed25519.PrivateKey) {
	t.Helper()
	withPublicBase(t, testPublicBase)
	gw, accounts, _, access = newGatewayHarness(t)
	registerSolutionUpstream(t, gw, "example")
	gw.solutionEntitlements = &entitledTo{ids: []string{fakeSolutionTarget("example")}}
	caller = signConfinedCaller(t, access, solutionMCPResource(testPublicBase, "example"))
	return gw, accounts, caller, access
}

// A caller cannot pick the ceiling AT THE MINT — the route whose stamp accounts
// resolves the installation from. Both values are replaced, and the
// grpc-metadata spelling of either reaches nothing: accounts transcodes REST to
// gRPC, so a prefixed name is a second way to assert the same field.
func TestCeilingHeaders_ACallerCannotPickTheMintsCeiling(t *testing.T) {
	gw, accounts, caller, _ := confinedMintHarness(t)

	forged := http.Header{}
	forged.Set(resourceConfinementHeader, solutionMCPResource(testPublicBase, "a-broader-consumer"))
	forged.Set(installationConfinementHeader, "install-of-a-broader-consumer")
	forged.Set("Grpc-Metadata-"+installationConfinementHeader, "install-of-a-broader-consumer")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, mintRequestOf(caller, forged))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []string{solutionMCPResource(testPublicBase, "example")},
		accounts.lastHeaders.Values(resourceConfinementHeader),
		"one value, and the one this credential names")
	require.Equal(t, []string{"install-" + fakeSolutionTarget("example")},
		accounts.lastHeaders.Values(installationConfinementHeader),
		"one value, and the one the authority admitted")
	require.Empty(t, accounts.lastHeaders.Get("Grpc-Metadata-"+installationConfinementHeader))
}

// AN ORDINARY SESSION at the mint carries NO installation, forged or otherwise.
//
// Nothing resolves one for an unconfined request — no consumer was named, so no
// consent was looked up — and a value arriving anyway would hand accounts a
// ceiling for an installation this person never acted under. The resource header
// is restamped EMPTY rather than dropped, so accounts reads a value either way.
func TestCeilingHeaders_AnOrdinarySessionCannotForgeAnInstallationAtTheMint(t *testing.T) {
	gw, accounts, _, access := confinedMintHarness(t)

	forged := http.Header{}
	forged.Set(installationConfinementHeader, "install-of-somebody-else")
	ordinary := confinedCaller{
		bearer:  signValidToken(t, access),
		org:     uuid.Must(uuid.NewV7()).String(),
		session: uuid.Must(uuid.NewV7()).String(),
	}
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, mintRequestOf(ordinary, forged))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Empty(t, accounts.lastHeaders.Values(installationConfinementHeader),
		"an unconfined mint names no consumer, so no installation is resolved and none may be asserted")
	require.Empty(t, accounts.lastHeaders.Get(resourceConfinementHeader),
		"and the resource is stamped empty, not left to a caller")
}

// THE ENVOY ANSWER, which the HTTP path cannot speak for.
//
// A deployment driving ext_authz from Envoy acts on this response alone, so the
// two headers have to be in it: the resource restamped (replaced, including with
// the empty value) and the installation in the remove set, because ext_authz
// consults no entitlement authority and therefore never stamps one. A name
// missing from `untrustedAuthHeaders` disappears from BOTH the in-process strip
// and this set at once, which is why it is asserted here by value.
func TestCeilingHeaders_TheEnvoyAnswerReplacesOrRemovesThem(t *testing.T) {
	withPublicBase(t, testPublicBase)
	s, priv := newTestExtAuthz(t)
	s.publicBase = testPublicBase

	for _, path := range []string{workContextMintProcedure, "/v1/users"} {
		resp, err := s.Check(context.Background(), checkReq(path, map[string]string{
			"authorization":               "Bearer " + signValidToken(t, priv),
			resourceConfinementHeader:     solutionMCPResource(testPublicBase, "example"),
			installationConfinementHeader: "install-of-a-broader-consumer",
		}))
		require.NoError(t, err)
		require.NotNil(t, resp.GetOkResponse(), path)

		stamped := headerMap(resp)
		value, restamped := stamped[resourceConfinementHeader]
		require.True(t, restamped, "the resource header must be REPLACED on %s, not left as the caller sent it", path)
		require.Empty(t, value, "an unbound session is confined to nothing, by value")
		require.Contains(t, resp.GetOkResponse().GetHeadersToRemove(), installationConfinementHeader,
			"ext_authz stamps no installation, so a caller's must be removed on %s", path)
	}

	// And the pairing is unreachable in Envoy mode, so a confined bearer at a
	// module route is refused there rather than admitted unpaired.
	resp, err := s.Check(context.Background(), checkReq("/v1/documents/collection", map[string]string{
		"authorization": "Bearer " + signResourceToken(t, priv, solutionMCPResource(testPublicBase, "example")),
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse())
}
