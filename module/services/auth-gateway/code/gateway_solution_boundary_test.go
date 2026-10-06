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

// A caller that simply asserts the header gets it stripped, in both spellings
// an upstream could read one under. This is the whole cross-solution property:
// without the strip, any authenticated viewer could mint under any solution's
// boundary by typing its id.
func TestGateway_WorkContextMint_StripsACallerAssertedSolution(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	req := mintRequest(t, priv)
	req.Header.Set("x-codefly-solution-identity", "audit")
	req.Header.Set("x-codefly-solution-publisher", "solution:audit")
	req.Header.Set("Grpc-Metadata-X-Codefly-Solution-Id", "audit")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, apiFake.lastHeaders.Get("x-codefly-solution-identity"))
	require.Empty(t, apiFake.lastHeaders.Get("x-codefly-solution-publisher"))
	requireNoGRPCMetadataHeaders(t, apiFake.lastHeaders)
}

// A registration answer carries NO boundary, to either half, and neither does
// the registry snapshot. A solution never needs one — accounts derives and
// seals it from the credential the solution already presents — so echoing it
// would only widen who can see a value that is now stable for the life of the
// registration. The accounts half of this promise is
// TestSolutionRegistrationResponsesCarryNoRuntimeBoundary.
// The registry must never echo a runtime boundary, and this is the half of that
// guarantee that survives the cutover.
//
// It used to POST to `/solutions/_frontend` and `/solutions/_register` and check
// their answers too. Those endpoints are the runtime registration writer and are
// deleted, so asserting on their responses would assert on a 404. What still
// matters — and matters more, since the seed now outlives a withdrawal — is that
// the SNAPSHOT every consumer reads carries no boundary: the seed is the one
// thing that must never leave this host, and the snapshot is what leaves it.
func TestGateway_SolutionRegistry_SnapshotEchoesNoBoundary(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)

	snapshot := httptest.NewRequest(http.MethodGet, "/solutions/_registry", nil)
	snapshot.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, snapshot)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, strings.ToLower(w.Body.String()), "boundary")
}
