package main

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The host assigns a solution's runtime boundary (issue #1015).
//
// Accounts seals a registered solution's boundary into every Work Context it
// mints for that solution, and it learns which solution is asking from exactly
// one place: the header this gateway stamps after verifying the solution's own
// signed, solution-bound registration credential. These tests pin that the
// header can come from nowhere else.

const mintPath = "/saas.accounts.v1.WorkContextService/StartTask"

func mintRequest(t *testing.T, priv ed25519.PrivateKey) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, mintPath, strings.NewReader("{}"))
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set("content-type", "application/json")
	return req
}

// A verified credential on the mint becomes the trusted solution identity, and
// only on the mint: the same credential on any other route stamps nothing, so
// the surface it widens is one procedure.
func TestGateway_WorkContextMint_StampsTheVerifiedSolution(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	req := mintRequest(t, priv)
	req.Header.Set(solutionRegistrationHeader,
		signSolutionRegistration(t, "audit", "solution:audit"))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "audit", apiFake.lastHeaders.Get(solutionIdentityHeader))
	// The publisher travels with the id, from the same claims: accounts checks
	// it against the publisher that owns the registration.
	require.Equal(t, "solution:audit", apiFake.lastHeaders.Get(solutionPublisherHeader))
	// The credential itself never reaches accounts: it proves the identity here
	// and the header is what accounts reads.
	require.Empty(t, apiFake.lastHeaders.Get(solutionRegistrationHeader))

	other := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	other.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	other.Header.Set(solutionRegistrationHeader,
		signSolutionRegistration(t, "audit", "solution:audit"))
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, other)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, apiFake.lastHeaders.Get(solutionIdentityHeader),
		"the credential means nothing outside the mint procedure")
}

// A caller that simply asserts the header gets it stripped, in both spellings
// an upstream could read one under. This is the whole cross-solution property:
// without the strip, any authenticated viewer could mint under any solution's
// boundary by typing its id.
func TestGateway_WorkContextMint_StripsACallerAssertedSolution(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	req := mintRequest(t, priv)
	req.Header.Set(solutionIdentityHeader, "audit")
	req.Header.Set(solutionPublisherHeader, "solution:audit")
	req.Header.Set("Grpc-Metadata-X-Codefly-Solution-Id", "audit")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, apiFake.lastHeaders.Get(solutionIdentityHeader))
	require.Empty(t, apiFake.lastHeaders.Get(solutionPublisherHeader))
	requireNoGRPCMetadataHeaders(t, apiFake.lastHeaders)
}

// A caller holding one solution's credential cannot stamp another's: the id is
// the credential's own claim, so the header says what was proved and the
// request body has no say at all.
func TestGateway_WorkContextMint_CredentialDecidesTheSolution(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	req := mintRequest(t, priv)
	req.Header.Set(solutionRegistrationHeader,
		signSolutionRegistration(t, "example-b", "solution:example-b"))
	req.Header.Set(solutionIdentityHeader, "example-a")
	req.Header.Set(solutionPublisherHeader, "solution:example-a")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "example-b", apiFake.lastHeaders.Get(solutionIdentityHeader))
	require.Equal(t, "solution:example-b", apiFake.lastHeaders.Get(solutionPublisherHeader))
}

// A presented credential that does not verify is refused rather than treated as
// an ordinary mint. Falling through would answer a forged or expired credential
// with a capability under a different boundary, which is the one outcome worth
// failing loudly on.
func TestGateway_WorkContextMint_RefusesAnUnverifiableCredential(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	for name, credential := range map[string]string{
		"not a token":      "not-a-token",
		"wrong audience":   signValidToken(t, priv),
		"empty solution":   signSolutionRegistration(t, "", "solution:audit"),
		"invalid solution": signSolutionRegistration(t, "Not A Solution", "solution:audit"),
		"no publisher":     signSolutionRegistration(t, "audit", ""),
	} {
		t.Run(name, func(t *testing.T) {
			req := mintRequest(t, priv)
			req.Header.Set(solutionRegistrationHeader, credential)
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)

			require.Equal(t, http.StatusUnauthorized, w.Code)
			require.Empty(t, apiFake.lastHeaders.Get(solutionIdentityHeader))
		})
	}
}

// A mint with no credential is an ordinary mint and is untouched — every mint
// the host's own pages and every composed module make takes this path.
func TestGateway_WorkContextMint_WithoutACredentialIsUnchanged(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, mintRequest(t, priv))

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, apiFake.lastHeaders.Get(solutionIdentityHeader))
}

// A registration answer carries NO boundary, to either half, and neither does
// the registry snapshot. A solution never needs one — accounts derives and
// seals it from the credential the solution already presents — so echoing it
// would only widen who can see a value that is now stable for the life of the
// registration. The accounts half of this promise is
// TestSolutionRegistrationResponsesCarryNoRuntimeBoundary.
func TestGateway_SolutionRegistration_EchoesNoBoundary(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	srv := httptest.NewServer(&fakeUpstream{body: "solution-response"})
	t.Cleanup(srv.Close)

	for name, call := range map[string][2]string{
		"the frontend half": {"/solutions/_frontend", `{"id":"audit","manifest":"{\"id\":\"audit\"}"}`},
		"the backend half":  {"/solutions/_register", `{"id":"audit","upstream":"` + srv.URL + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			answer := postSolutionRegistration(t, gw, call[0], call[1], http.StatusOK)
			require.NotContains(t, answer, "runtimeBoundary")
		})
	}

	snapshot := httptest.NewRequest(http.MethodGet, "/solutions/_registry", nil)
	snapshot.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, snapshot)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, strings.ToLower(w.Body.String()), "boundary")
}
