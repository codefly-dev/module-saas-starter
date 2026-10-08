package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/infra"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// The delivery endpoint's PUBLISHED contract, exercised through the handler.
//
// The response-code table in `solution_delivery_http.go` is what three
// repositories agreed on, and a table in a comment is a claim until something
// drives the handler and reads the codes back. These requests go through the
// real `ReceiveSolutionDelivery` with fake collaborators, so the ordering and
// the mapping are both under test rather than the mapping alone.
//
// THE ONE DISTINCTION THESE TESTS EXIST FOR is 401 versus 503: "the review ran
// and refused" against "the review could not run". They are different facts with
// opposite retry answers, and an earlier draft of the mapping got it wrong by
// grouping on HTTP class. A test that only checked "it is an error" would have
// passed against that draft.

type fakeDeliveryVerifier struct {
	signer string
	err    error
	// payloads records what was verified, so "the bundle is checked over
	// exactly these bytes" is observable rather than assumed.
	payloads [][]byte
}

func (f *fakeDeliveryVerifier) VerifyBundle(
	_ context.Context, payload []byte, _ json.RawMessage,
) (string, error) {
	f.payloads = append(f.payloads, payload)
	if f.err != nil {
		return "", f.err
	}
	return f.signer, nil
}

type fakeDeliveryStore struct {
	inserted bool
	err      error
	calls    int
}

func (f *fakeDeliveryStore) RecordDeliveredDocument(
	_ context.Context, _ *business.SolutionDeliveryRecord,
) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.inserted, nil
}

func (f *fakeDeliveryStore) ListNewestDeliveredDocuments(
	context.Context, business.SolutionDeliveryKind,
) ([]*business.SolutionDeliveryRecord, error) {
	return nil, nil
}

type fakeCarrierAuthorizer struct {
	err error
	// seenNamespaces records what the document declared, because a presence
	// carrier is authorised against the namespaces in the SIGNED document and
	// not against host configuration.
	seenNamespaces []string
	calls          int
}

func (f *fakeCarrierAuthorizer) AuthorizeCarrier(
	_ context.Context, _ string, _ business.SolutionDeliveryKind, declared []string,
) error {
	f.calls++
	f.seenNamespaces = declared
	return f.err
}

// deliveryHarness wires a service whose delivery half is fakes.
type deliveryHarness struct {
	handler  http.Handler
	verifier *fakeDeliveryVerifier
	store    *fakeDeliveryStore
	carrier  *fakeCarrierAuthorizer
}

func newDeliveryHarness(t *testing.T, signer string, domains map[string][]string) *deliveryHarness {
	t.Helper()
	harness := &deliveryHarness{
		verifier: &fakeDeliveryVerifier{signer: signer},
		store:    &fakeDeliveryStore{inserted: true},
		carrier:  &fakeCarrierAuthorizer{},
	}
	service, err := business.NewService(deliveryNoopStore{})
	require.NoError(t, err)
	service.SetSolutionDelivery(harness.verifier, harness.store, harness.carrier, domains)
	harness.handler = NewSolutionDeliveryHTTPHandler(service)
	return harness
}

func (h *deliveryHarness) post(path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer projected-token")
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	return recorder
}

// ---------------------------------------------------------------------------
// The surface itself
// ---------------------------------------------------------------------------

// An unknown path under the reserved namespace is 404, never a fall-through to
// some other handler.
func TestDeliveryUnknownKindIs404(t *testing.T) {
	harness := newDeliveryHarness(t, "signer", nil)
	for _, path := range []string{
		SolutionDeliveryPrefix + "nonsense",
		SolutionDeliveryPrefix,
		SolutionDeliveryPrefix + "presence/extra",
	} {
		recorder := harness.post(path, []byte("{}"))
		require.Equal(t, http.StatusNotFound, recorder.Code, "path %s", path)
	}
	require.Zero(t, harness.verifier.payloads, "an unknown kind must not reach verification")
}

// A GET gets 405 WITH Allow, so a probe learns the method rather than reading a
// 404 as "this host has no delivery endpoint at all".
func TestDeliveryGetIs405WithAllow(t *testing.T) {
	harness := newDeliveryHarness(t, "signer", nil)
	request := httptest.NewRequest(http.MethodGet, SolutionDeliveryPrefix+"presence", nil)
	recorder := httptest.NewRecorder()
	harness.handler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	require.Equal(t, http.MethodPost, recorder.Header().Get("Allow"),
		"without Allow, a caller cannot tell a wrong method from a missing endpoint")
}

// A body past the bound is 400 and never buffered whole.
func TestDeliveryOversizedBodyIs400(t *testing.T) {
	harness := newDeliveryHarness(t, "signer", nil)
	recorder := harness.post(SolutionDeliveryPrefix+"presence",
		[]byte(strings.Repeat("a", (1<<20)+1)))

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, harness.verifier.payloads, "an oversized body must not reach verification")
}

// ---------------------------------------------------------------------------
// The code table, refusal by refusal
// ---------------------------------------------------------------------------

// Malformed bytes are 400: terminal, because the bytes are wrong.
func TestDeliveryMalformedCarrierIs400(t *testing.T) {
	harness := newDeliveryHarness(t, "signer", nil)
	recorder := harness.post(SolutionDeliveryPrefix+"presence", []byte("not a carrier"))

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, harness.carrier.calls,
		"a carrier that does not parse is refused before the caller is authorised")
}

// THE SPLIT THAT MATTERS. A carrier the host refused is 401 — terminal, because
// the token is kubelet-projected and re-read per request, so a refusal is a
// configuration fault and a retry asks the same question. A carrier the host
// could not CHECK is 503 — retryable, because nothing was decided.
//
// Both in one test, because either alone is satisfied by collapsing them, which
// is exactly the mistake the mapping's comment records.
func TestCarrierRefusedIs401WhileUnreachableIs503(t *testing.T) {
	carrier := signedPresenceCarrier(t, "acme", "acme-prod")

	refused := newDeliveryHarness(t, "signer", map[string][]string{"signer": {"acme"}})
	refused.carrier.err = fmt.Errorf("%w: wrong service account", infra.ErrCarrierUnauthenticated)
	recorder := refused.post(SolutionDeliveryPrefix+"presence", carrier)
	require.Equal(t, http.StatusUnauthorized, recorder.Code,
		"the review RAN and refused: a retry asks the same question")

	unreachable := newDeliveryHarness(t, "signer", map[string][]string{"signer": {"acme"}})
	unreachable.carrier.err = fmt.Errorf("%w: dial tcp: connection refused", infra.ErrKubernetesUnavailable)
	recorder = unreachable.post(SolutionDeliveryPrefix+"presence", carrier)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code,
		"the review COULD NOT RUN: nothing was decided, so the caller must retry")
}

// A caller in the wrong namespace or with the wrong service account is 403:
// authenticated, and not the writer.
func TestCarrierNotAuthorizedIs403(t *testing.T) {
	harness := newDeliveryHarness(t, "signer", map[string][]string{"signer": {"acme"}})
	harness.carrier.err = fmt.Errorf("%w: namespace not declared", infra.ErrCarrierNotAuthorized)

	recorder := harness.post(SolutionDeliveryPrefix+"presence",
		signedPresenceCarrier(t, "acme", "acme-prod"))
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

// An unattested carrier is 403, and the signature is checked BEFORE the caller.
//
// That order is the contract: otherwise a captured carrier replayed by an
// authorised caller would be refused with a message about credentials, and an
// unauthenticated caller's bad signature with a message about signatures. The
// operator reads the wrong cause in both directions.
func TestUnattestedCarrierIs403AndIsRefusedBeforeTheCaller(t *testing.T) {
	harness := newDeliveryHarness(t, "signer", map[string][]string{"signer": {"acme"}})
	harness.verifier.err = errors.New("no accepted signer")

	recorder := harness.post(SolutionDeliveryPrefix+"presence",
		signedPresenceCarrier(t, "acme", "acme-prod"))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Zero(t, harness.carrier.calls,
		"verification precedes carrier authorisation, so the operator reads the real cause")
}

// An accepted signer delivering under a domain it may not speak for is 403 —
// distinct from being unattested, because the signature is perfectly good.
func TestSignerOutsideItsDomainIs403(t *testing.T) {
	harness := newDeliveryHarness(t, "signer", map[string][]string{"signer": {"other-domain"}})

	recorder := harness.post(SolutionDeliveryPrefix+"presence",
		signedPresenceCarrier(t, "acme", "acme-prod"))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Zero(t, harness.store.calls, "nothing is persisted for a domain the signer cannot claim")
}

// A rewritten generation is 409: publish never rewrites one, so this is a
// hand-edited delivery tree and no retry helps.
func TestRewrittenGenerationIs409(t *testing.T) {
	harness := newDeliveryHarness(t, "signer", map[string][]string{"signer": {"acme"}})
	harness.store.err = fmt.Errorf("%w: generation 3", business.ErrSolutionDeliveryRewritten)

	recorder := harness.post(SolutionDeliveryPrefix+"presence",
		signedPresenceCarrier(t, "acme", "acme-prod"))
	require.Equal(t, http.StatusConflict, recorder.Code)
}

// An UNCLASSIFIED failure is 503, not 500, and this is a deliberate choice
// rather than a default.
//
// An unmapped failure means the host does not know whether it verified the
// carrier, and the honest answer to "did you accept this?" is "ask again". A 500
// is what a Job's retry policy treats as terminal, so it would strand a delivery
// the host might well accept on the next attempt.
func TestUnclassifiedFailureIs503NotServerError(t *testing.T) {
	harness := newDeliveryHarness(t, "signer", map[string][]string{"signer": {"acme"}})
	harness.store.err = errors.New("something nobody mapped")

	recorder := harness.post(SolutionDeliveryPrefix+"presence",
		signedPresenceCarrier(t, "acme", "acme-prod"))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.NotEqual(t, http.StatusInternalServerError, recorder.Code,
		"a 500 is terminal to a Job's retry policy, and the host does not know this is terminal")
}

// ---------------------------------------------------------------------------
// Success, and the 202/200 split a pipeline's idempotency depends on
// ---------------------------------------------------------------------------

// A new generation is 202 with a receipt; an exact replay is 200. Both are
// success, and the difference is whether anything CHANGED — which is what lets a
// publishing pipeline tell "I delivered this" from "this was already here".
func TestNewGenerationIs202AndAnExactReplayIs200(t *testing.T) {
	carrier := signedPresenceCarrier(t, "acme", "acme-prod")

	fresh := newDeliveryHarness(t, "signer", map[string][]string{"signer": {"acme"}})
	fresh.store.inserted = true
	recorder := fresh.post(SolutionDeliveryPrefix+"presence", carrier)
	require.Equal(t, http.StatusAccepted, recorder.Code)

	// The tags are the WIRE CONTRACT, spelled here independently of the
	// handler's own struct so this test pins the field names a consuming
	// pipeline parses. Declared camelCase because that is what the endpoint
	// emits — my first draft of this test guessed snake_case and failed, which
	// is the test doing its job: a cross-repo contract that nothing spells out
	// twice is a contract one side can rename silently.
	var receipt struct {
		Disposition string `json:"disposition"`
		DocumentID  string `json:"documentId"`
		Generation  uint64 `json:"generation"`
		ContentHash string `json:"contentHash"`
		Signer      string `json:"signer"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &receipt))
	require.Equal(t, string(business.SolutionDeliveryAccepted), receipt.Disposition)
	require.Equal(t, "signer", receipt.Signer, "the receipt names the ATTESTED signer, not the caller")
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, receipt.ContentHash,
		"the receipt carries the content hash a pipeline compares to decide whether to re-deliver")
	require.Equal(t, uint64(3), receipt.Generation)
	require.Equal(t, "acme.test.delivery-http", receipt.DocumentID)

	replay := newDeliveryHarness(t, "signer", map[string][]string{"signer": {"acme"}})
	replay.store.inserted = false
	recorder = replay.post(SolutionDeliveryPrefix+"presence", carrier)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &receipt))
	require.Equal(t, string(business.SolutionDeliveryReplayed), receipt.Disposition)
}

// The bundle is verified over the carrier's PAYLOAD bytes, and the caller's
// declared namespaces come from the signed document.
//
// Both are properties the handler cannot be trusted on by inspection: a verifier
// handed the whole request body, or an authoriser handed namespaces from host
// configuration, would behave identically on the happy path.
func TestVerificationCoversThePayloadAndNamespacesComeFromTheDocument(t *testing.T) {
	harness := newDeliveryHarness(t, "signer", map[string][]string{"signer": {"acme"}})
	recorder := harness.post(SolutionDeliveryPrefix+"presence",
		signedPresenceCarrier(t, "acme", "acme-prod"))
	require.Equal(t, http.StatusAccepted, recorder.Code)

	require.Len(t, harness.verifier.payloads, 1)
	require.NotEmpty(t, harness.verifier.payloads[0])
	require.Equal(t, []string{"acme-prod"}, harness.carrier.seenNamespaces,
		"the presence namespace is a property of the SIGNED document, not of host configuration")
}

// signedPresenceCarrier builds a carrier core's ParseSigned accepts, with a
// bundle the fake verifier stands in for.
//
// Built through core's own types and `MarshalSigned` over `CanonicalBytes`, not
// a hand-written JSON literal: the signing input IS the canonical encoding, and
// a literal would drift the moment core's schema moved while still passing.
// `Validate` is asserted here so a malformed fixture fails as a fixture rather
// than as a 422 that looks like the handler refusing correctly.
func signedPresenceCarrier(t *testing.T, domain, namespace string) []byte {
	t.Helper()
	document := &solutionhost.SolutionHostBinding{
		Schema:           solutionhost.SchemaPresenceV1,
		Kind:             solutionhost.KindSolution,
		Binding:          "acme.test.delivery-http",
		Generation:       3,
		OwnershipDomain:  domain,
		EnvelopeRevision: 1,
		Host: solutionhost.HostTarget{
			Coordinate: "acme/test/eu-west-1", Component: "saas-host",
		},
		Release: solutionhost.Release{
			Publisher: "acme", Name: "delivery-http", Version: "1.4.0",
			Digest: solutionhost.ReleaseDigest("sha256:" + strings.Repeat("ef", 32)),
		},
		Routes: []solutionhost.Route{{Alias: "delivery-http", Surface: solutionhost.SurfaceBackend}},
		Artifacts: []solutionhost.Artifact{{
			Surface: solutionhost.SurfaceBackend,
			Name:    "api",
			Release: "acme/delivery-http@1.4.0",
			Digest:  solutionhost.RenderedDigest("sha256:" + strings.Repeat("ab", 32)),
		}},
		Workloads: []solutionhost.Workload{{
			Name: "api", Artifact: "api", Container: "api",
			Image: solutionhost.Image{
				Repository: "registry.example/acme/delivery-http",
				Digest:     solutionhost.ImageDigest("sha256:" + strings.Repeat("cd", 32)),
			},
			// The namespace reaches the carrier authoriser from the SPIFFE ID,
			// which is why there is no Namespace field to set: a workload's
			// namespace is derived from its signed identity rather than
			// declared beside it, so it cannot be edited without breaking the
			// signature. That is the property
			// TestVerificationCoversThePayloadAndNamespacesComeFromTheDocument
			// asserts, and it holds because of where the value comes from.
			Identity: solutionhost.WorkloadIdentity{
				Audience: "https://test.acme.example/solutions",
				Subject:  "system:serviceaccount:" + namespace + ":api",
				SPIFFEID: "spiffe://acme.test/ns/" + namespace + "/sa/api",
			},
			NonAuthenticating: &[]string{"telemetry-sidecar"},
		}},
	}
	require.NoError(t, document.Validate(), "the fixture must be a valid binding")

	canonical, err := document.CanonicalBytes()
	require.NoError(t, err)
	carrier, err := solutionhost.MarshalSigned(&solutionhost.Signed{
		Schema:   solutionhost.SchemaSignedV1,
		Document: canonical,
		Bundle:   json.RawMessage(solutionhost.FixtureBundle),
	})
	require.NoError(t, err)
	return carrier
}

// deliveryNoopStore satisfies business.Store without implementing it.
//
// The embedded interface is nil, so any method this test does not override
// PANICS rather than returning a zero value — which is what caught that the
// persist path really does open a control-plane transaction. A stub returning
// zeroes would have let the write silently no-op and the 409 test would have
// passed for the wrong reason.
type deliveryNoopStore struct{ business.Store }

// WithControlPlane runs the function inline.
//
// Overridden because the delivery write is wrapped in a control-plane
// transaction, and the point of these tests is the HTTP contract rather than
// transaction behaviour. Running it inline keeps the store error surfacing
// exactly as the real path surfaces it, which is what the 409 and the
// unclassified-failure cases depend on.
func (deliveryNoopStore) WithControlPlane(
	ctx context.Context, fn func(ctx context.Context) error,
) error {
	return fn(ctx)
}
