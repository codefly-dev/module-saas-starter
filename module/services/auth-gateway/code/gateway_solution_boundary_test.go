package main

import (
	"context"
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

// accountsSolutionIdentityHeader and accountsSolutionPublisherHeader are
// ACCOUNTS' spellings, written here as literals ON PURPOSE. The point of this
// file is to check the gateway against what accounts reads, so it must not share
// a symbol with the gateway: an earlier version of this test set and asserted
// `x-codefly-solution-identity`, the same near-miss the strip list carried, so
// the test and the hole agreed and the suite was green while any authenticated
// caller could assert any solution's identity to accounts. A test that borrows
// the constant it is checking cannot catch a wrong constant.
//
// They are accounts' `connect_auth_interceptor.go` constants, lowercased, which
// is the form `http.Header.Del` and gRPC metadata both use.
const (
	accountsSolutionIdentityHeader  = "x-codefly-solution-id"
	accountsSolutionPublisherHeader = "x-codefly-solution-publisher"
)

// The gateway's strip constants must BE accounts' spellings. `http.Header.Del`
// canonicalises its argument, so a near-miss deletes a header nobody sends and
// leaves the real one in place, with no error anywhere.
func TestSolutionStripConstantsAreTheHeadersAccountsReads(t *testing.T) {
	require.Equal(t, accountsSolutionIdentityHeader, solutionIdentityHeader)
	require.Equal(t, accountsSolutionPublisherHeader, solutionPublisherHeader)
}

// A caller that simply asserts the header gets it stripped, in both spellings
// an upstream could read one under. This is the whole cross-solution property:
// without the strip, any authenticated viewer could mint under any solution's
// boundary by typing its id. Asserted on what the UPSTREAM received, because
// that is the only place the question is answered.
func TestGateway_WorkContextMint_StripsACallerAssertedSolution(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)

	req := mintRequest(t, priv)
	req.Header.Set(accountsSolutionIdentityHeader, "audit")
	req.Header.Set(accountsSolutionPublisherHeader, "solution:audit")
	// The canonical-cased spelling too: a caller controls the case on the wire,
	// and Del is case-insensitive, so both must go.
	req.Header.Set("X-Codefly-Solution-Id", "audit")
	req.Header.Set("Grpc-Metadata-X-Codefly-Solution-Id", "audit")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, apiFake.lastHeaders.Get(accountsSolutionIdentityHeader),
		"a caller-asserted solution identity reached the upstream: accounts trusts this header beside a valid gateway token, so it would mint under that solution's boundary")
	require.Empty(t, apiFake.lastHeaders.Get(accountsSolutionPublisherHeader))
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
	// A snapshot that has actually LOADED, reached through the declared path.
	// This test used to POST to `/solutions/_frontend` and `/solutions/_register`
	// to populate the registry; those are the runtime registration writer and
	// are deleted. `refresh` is the seam that remains — without it the handler
	// answers 503 "solution registry unavailable" and the boundary assertion
	// would pass because there is nothing to leak.
	gw, _, _, _ := newGatewayHarness(t)
	registry := solutionRegistryFake(t, gw)
	registry.seedDeclared("audit", "http://audit.svc")
	require.NoError(t, gw.solutions.refresh(context.Background()))

	req := httptest.NewRequest(http.MethodGet, "/solutions/_registry", nil)
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code,
		"the snapshot must actually be served, or the assertion below is vacuous")
	require.Contains(t, w.Body.String(), "audit",
		"the snapshot must carry the declared record, or there is nothing a boundary could leak through")
	require.NotContains(t, strings.ToLower(w.Body.String()), "boundary")
}
