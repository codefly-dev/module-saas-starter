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

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	"accounts/pkg/infra"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// The delivery endpoint's PUBLISHED contract, exercised through the handler:
// `POST /platform/_delivery/manifest`, one signed module manifest. These
// requests go through the real ReceiveSolutionDelivery — core's verification,
// admission and alias rules — with a fake inbox and carrier authorizer, so the
// ordering and the code mapping are both under test.
//
// The codes are what the delivery Job and a native run read: 200 for accepted
// and replayed, 400 invalid, 401/403 carrier or signer, 409 stale, rewritten or
// withdrawn, 422 refused, 503 retry.

const (
	deliveryTestKey        = "release-key"
	deliveryTestDomain     = "acme"
	deliveryTestCoordinate = "acme-test"
)

type fakeDeliveryStore struct {
	inserted bool
	err      error
	calls    int
	payloads [][]byte
	newest   []*business.SolutionDeliveryRecord
}

func (f *fakeDeliveryStore) RecordDeliveredDocument(_ context.Context, record *business.SolutionDeliveryRecord) (bool, error) {
	f.calls++
	f.payloads = append(f.payloads, record.Payload)
	if f.err != nil {
		return false, f.err
	}
	return f.inserted, nil
}

func (f *fakeDeliveryStore) ListNewestDeliveredDocuments(context.Context) ([]*business.SolutionDeliveryRecord, error) {
	return f.newest, nil
}

func (f *fakeDeliveryStore) ReadDeliveredDocument(context.Context, string, uint64) (*business.SolutionDeliveryRecord, error) {
	return nil, nil
}

// fakeCarrierAuthorizer records the namespace the host asked the carrier to be
// the delivery account of.
type fakeCarrierAuthorizer struct {
	err       error
	namespace string
	calls     int
}

func (f *fakeCarrierAuthorizer) AuthorizeCarrier(_ context.Context, _ string, namespace string) error {
	f.calls++
	f.namespace = namespace
	return f.err
}

// deliveryNoopStore satisfies business.Store with only the reads delivery
// makes: the applied records and an inline control-plane transaction. Any other
// method panics, because the embedded interface is nil.
type deliveryNoopStore struct {
	business.Store
	records []*business.SolutionHostBindingRecord
}

func (deliveryNoopStore) WithControlPlane(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func (s deliveryNoopStore) ListSolutionHostBindings(context.Context) ([]*business.SolutionHostBindingRecord, error) {
	return s.records, nil
}

// deliveryHarness uses real signatures with a fake inbox and carrier authorizer.
type deliveryHarness struct {
	handler http.Handler
	store   *fakeDeliveryStore
	carrier *fakeCarrierAuthorizer
}

func newDeliveryHarness(t *testing.T, pins map[string][]string, applied ...*solutionhost.ModuleManifest) *deliveryHarness {
	t.Helper()
	if pins == nil {
		pins = map[string][]string{deliveryTestKey: {deliveryTestDomain}}
	}
	harness := &deliveryHarness{store: &fakeDeliveryStore{inserted: true}, carrier: &fakeCarrierAuthorizer{}}
	var records []*business.SolutionHostBindingRecord
	for _, manifest := range applied {
		records = append(records, appliedRecord(t, manifest))
	}
	service, err := business.NewService(deliveryNoopStore{records: records})
	require.NoError(t, err)
	service.SetSolutionDelivery(deliverytest.Verifier(t, pins), harness.store, harness.carrier)
	service.SetSolutionDeliveryCoordinate(deliveryTestCoordinate)
	rule, err := business.NewModuleNamespaceRule("platform-acme-staging-saas", "saas")
	require.NoError(t, err)
	service.SetModuleNamespaceRule(rule)
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

func (h *deliveryHarness) deliver(t *testing.T, manifest *solutionhost.ModuleManifest) *httptest.ResponseRecorder {
	t.Helper()
	return h.post(SolutionDeliveryManifestPath, signedManifest(t, manifest, deliveryTestKey))
}

// testModuleManifest is a valid live manifest for module "reports" on the
// test host.
func testModuleManifest(t *testing.T, generation uint64) *solutionhost.ModuleManifest {
	t.Helper()
	manifest := &solutionhost.ModuleManifest{
		Schema: solutionhost.SchemaModuleManifestV1, Binding: "acme.test.reports", Alias: "reports",
		Kind: solutionhost.KindModule, Generation: generation,
		Host:      solutionhost.HostTarget{Coordinate: deliveryTestCoordinate, Domain: deliveryTestDomain},
		Release:   solutionhost.Release{Publisher: "acme", Name: "reports", Version: "1.0.0"},
		Workloads: []solutionhost.Workload{{Service: "reports", Container: "reports"}},
		Endpoints: []solutionhost.Endpoint{{Name: "rest", Service: "reports", API: "rest", Address: "http://reports:8080"}},
	}
	require.NoError(t, manifest.Validate())
	return manifest
}

func withdrawal(t *testing.T, manifest *solutionhost.ModuleManifest, generation uint64) *solutionhost.ModuleManifest {
	t.Helper()
	removed := *manifest
	removed.Generation, removed.Removed = generation, true
	removed.Workloads, removed.Endpoints, removed.Authority, removed.Frontend = nil, nil, nil, nil
	require.NoError(t, removed.Validate())
	return &removed
}

// signedManifest is the carrier delivery ships: the canonical payload and a
// DSSE envelope by keyID.
func signedManifest(t *testing.T, manifest *solutionhost.ModuleManifest, keyID string) []byte {
	t.Helper()
	payload, err := solutionhost.SignedPayload(manifest)
	require.NoError(t, err)
	return deliverytest.Carrier(t, payload, keyID)
}

// appliedRecord is the host's record of having applied manifest.
func appliedRecord(t *testing.T, manifest *solutionhost.ModuleManifest) *business.SolutionHostBindingRecord {
	t.Helper()
	digest, err := manifest.Digest()
	require.NoError(t, err)
	encoded, err := solutionhost.Marshal(manifest)
	require.NoError(t, err)
	return &business.SolutionHostBindingRecord{
		BindingID: manifest.Binding, HostCoordinate: manifest.Host.Coordinate,
		Applied: &business.SolutionHostBindingApplied{
			SolutionHostBindingGeneration: business.SolutionHostBindingGeneration{Generation: manifest.Generation, Digest: digest, Document: string(encoded)},
			Removed:                       manifest.Removed, SolutionID: manifest.Alias,
			Release: manifest.Release.Identity(), Domain: manifest.Host.Domain,
		},
	}
}

type deliveryReceipt struct {
	Disposition string `json:"disposition"`
	Binding     string `json:"binding"`
	Generation  uint64 `json:"generation"`
	Digest      string `json:"digest"`
	Signer      string `json:"signer"`
	Error       string `json:"error"`
}

func receiptOf(t *testing.T, recorder *httptest.ResponseRecorder) deliveryReceipt {
	t.Helper()
	var receipt deliveryReceipt
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &receipt), recorder.Body.String())
	return receipt
}

// The surface: one write path, POST, bounded.
func TestDeliveryHasOneManifestPath(t *testing.T) {
	harness := newDeliveryHarness(t, nil)
	for _, path := range []string{
		SolutionDeliveryPrefix + "presence", SolutionDeliveryPrefix + "authority", SolutionDeliveryPrefix + "endpoints",
		SolutionDeliveryPrefix, SolutionDeliveryManifestPath + "/extra",
	} {
		require.Equal(t, http.StatusNotFound, harness.post(path, []byte("{}")).Code, "path %s", path)
	}
	request := httptest.NewRequest(http.MethodGet, SolutionDeliveryManifestPath, nil)
	recorder := httptest.NewRecorder()
	harness.handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	require.Equal(t, http.MethodPost, recorder.Header().Get("Allow"))
	require.Equal(t, http.StatusBadRequest, harness.post(SolutionDeliveryManifestPath, []byte(strings.Repeat("a", (1<<20)+1))).Code)
	require.Zero(t, harness.store.calls)
}

// A new generation is 200 "accepted" and an exact replay 200 "replayed": the
// delivery Job and a native run read any other code as the host's verdict.
func TestANewGenerationIsAcceptedAndAReplayIsReplayed(t *testing.T) {
	manifest := testModuleManifest(t, 3)
	digest, err := manifest.Digest()
	require.NoError(t, err)

	fresh := newDeliveryHarness(t, nil)
	recorder := fresh.deliver(t, manifest)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	receipt := receiptOf(t, recorder)
	require.Equal(t, deliveryReceipt{
		Disposition: "accepted", Binding: manifest.Binding, Generation: 3, Digest: digest, Signer: deliveryTestKey,
	}, receipt)
	require.Equal(t, "platform-acme-staging-reports", fresh.carrier.namespace,
		"the carrier is the delivery account of the module's namespace, by the cell's rule")

	replay := newDeliveryHarness(t, nil, manifest)
	replay.store.inserted = false
	recorder = replay.deliver(t, manifest)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "replayed", receiptOf(t, recorder).Disposition)
}

// A withdrawal is delivered from where its binding was delivered from: the
// previously applied manifest's module, whatever alias the withdrawal names.
func TestAWithdrawalIsCarriedFromTheAppliedModulesNamespace(t *testing.T) {
	applied := testModuleManifest(t, 3)
	harness := newDeliveryHarness(t, nil, applied)
	removal := withdrawal(t, applied, 4)
	removal.Alias = "renamed"
	recorder := harness.deliver(t, removal)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "platform-acme-staging-reports", harness.carrier.namespace)
}

// Each refusal, by its code, with the reason in the body, and nothing stored.
func TestEveryRefusalHasItsCodeAndReason(t *testing.T) {
	applied := testModuleManifest(t, 3)
	rewritten := testModuleManifest(t, 3)
	rewritten.Release.Version = "9.9.9"
	elsewhere := testModuleManifest(t, 4)
	elsewhere.Host.Coordinate = "another-host"
	reserved := testModuleManifest(t, 1)
	reserved.Binding, reserved.Alias = "acme.test.codefly", "codefly"
	tombstone := withdrawal(t, applied, 4)
	invalid, err := solutionhost.SignedPayload(testModuleManifest(t, 1))
	require.NoError(t, err)
	invalidPayload := strings.Replace(string(invalid), `"generation":1`, `"generation":0`, 1)

	for name, tc := range map[string]struct {
		applied []*solutionhost.ModuleManifest
		pins    map[string][]string
		body    func(t *testing.T) []byte
		carrier error
		code    int
		reason  string
	}{
		"not a carrier": {body: func(*testing.T) []byte { return []byte("not a carrier") }, code: 400},
		"an invalid manifest, genuinely signed": {
			body: func(t *testing.T) []byte {
				envelope := deliverytest.Envelope(t, []byte(invalidPayload), deliveryTestKey)
				carrier, err := solutionhost.MarshalSigned(&solutionhost.Signed{Document: []byte(invalidPayload), Envelope: envelope})
				require.NoError(t, err)
				return carrier
			},
			code: 400, reason: "invalid",
		},
		"a signer no key pins": {
			pins: map[string][]string{"another-key": {deliveryTestDomain}},
			body: func(t *testing.T) []byte { return signedManifest(t, testModuleManifest(t, 1), deliveryTestKey) },
			code: 401,
		},
		"a signer pinned for another domain": {
			pins: map[string][]string{deliveryTestKey: {"other-domain"}},
			body: func(t *testing.T) []byte { return signedManifest(t, testModuleManifest(t, 1), deliveryTestKey) },
			code: 403,
		},
		"a carrier the api server refuses": {
			carrier: fmt.Errorf("%w: token expired", infra.ErrCarrierUnauthenticated), code: 401,
			body: func(t *testing.T) []byte { return signedManifest(t, testModuleManifest(t, 1), deliveryTestKey) },
		},
		"a carrier that is not the module's delivery account": {
			carrier: fmt.Errorf("%w: platform-acme-staging-billing/delivery", infra.ErrCarrierNotAuthorized), code: 403,
			body: func(t *testing.T) []byte { return signedManifest(t, testModuleManifest(t, 1), deliveryTestKey) },
		},
		"an api server that cannot answer": {
			carrier: fmt.Errorf("%w: connection refused", infra.ErrKubernetesUnavailable), code: 503,
			body: func(t *testing.T) []byte { return signedManifest(t, testModuleManifest(t, 1), deliveryTestKey) },
		},
		"a stale generation": {
			applied: []*solutionhost.ModuleManifest{applied}, code: 409, reason: "older",
			body: func(t *testing.T) []byte { return signedManifest(t, testModuleManifest(t, 2), deliveryTestKey) },
		},
		"a rewritten generation": {
			applied: []*solutionhost.ModuleManifest{applied}, code: 409, reason: "rewritten",
			body: func(t *testing.T) []byte { return signedManifest(t, rewritten, deliveryTestKey) },
		},
		"a withdrawn binding brought back": {
			applied: []*solutionhost.ModuleManifest{tombstone}, code: 409, reason: "withdrawn",
			body: func(t *testing.T) []byte { return signedManifest(t, testModuleManifest(t, 5), deliveryTestKey) },
		},
		"another host's manifest": {
			applied: []*solutionhost.ModuleManifest{applied}, code: 422, reason: "another-host",
			body: func(t *testing.T) []byte { return signedManifest(t, elsewhere, deliveryTestKey) },
		},
		"a reserved route alias": {
			code: 422, reason: "codefly",
			body: func(t *testing.T) []byte { return signedManifest(t, reserved, deliveryTestKey) },
		},
	} {
		t.Run(name, func(t *testing.T) {
			harness := newDeliveryHarness(t, tc.pins, tc.applied...)
			harness.carrier.err = tc.carrier
			recorder := harness.post(SolutionDeliveryManifestPath, tc.body(t))
			require.Equal(t, tc.code, recorder.Code, recorder.Body.String())
			receipt := receiptOf(t, recorder)
			require.NotEmpty(t, receipt.Error, "every refusal names its reason")
			if tc.reason != "" {
				require.Contains(t, receipt.Error, tc.reason)
			}
			require.Zero(t, harness.store.calls, "a refused manifest is never stored")
		})
	}
}

// The signature is checked before the caller, so an operator reads the real
// cause: a forged carrier is a signing problem, not a credential one.
func TestVerificationPrecedesTheCarrier(t *testing.T) {
	harness := newDeliveryHarness(t, map[string][]string{"another-key": {deliveryTestDomain}})
	harness.carrier.err = infra.ErrCarrierUnauthenticated
	recorder := harness.deliver(t, testModuleManifest(t, 1))
	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	require.Zero(t, harness.carrier.calls)
}

// A generation already in the inbox with other bytes is a rewrite even before
// it is applied, and an unclassified failure is a retry that names its cause.
func TestTheInboxDecidesUnappliedRewritesAndUnclassifiedFailuresAreRetries(t *testing.T) {
	harness := newDeliveryHarness(t, nil)
	harness.store.err = fmt.Errorf("%w: generation 3", business.ErrSolutionDeliveryConflict)
	require.Equal(t, http.StatusConflict, harness.deliver(t, testModuleManifest(t, 3)).Code)

	harness = newDeliveryHarness(t, nil)
	harness.store.err = errors.New("something nobody mapped")
	recorder := harness.deliver(t, testModuleManifest(t, 3))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, receiptOf(t, recorder).Error, "something nobody mapped")
}

// An alias moves between bindings once the leaving binding's withdrawal is
// delivered: the inbox's newest generation releases it.
func TestADeliveredWithdrawalReleasesItsAlias(t *testing.T) {
	leaving := testModuleManifest(t, 3)
	claimant := testModuleManifest(t, 1)
	claimant.Binding = "acme.test.reports-next"

	held := newDeliveryHarness(t, nil, leaving)
	recorder := held.deliver(t, claimant)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, "the alias is still held")

	released := newDeliveryHarness(t, nil, leaving)
	payload, err := solutionhost.SignedPayload(withdrawal(t, leaving, 4))
	require.NoError(t, err)
	released.store.newest = []*business.SolutionDeliveryRecord{{Binding: leaving.Binding, Generation: 4, Payload: payload}}
	recorder = released.deliver(t, claimant)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}
