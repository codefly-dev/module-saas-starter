package adapters

import (
	"context"
	"encoding/json"
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

// `POST /platform/_delivery/envelope`, one release-signed module envelope,
// through the handler and the real ReceiveModuleEnvelopeDelivery: the route,
// the receipt and the status taxonomy it shares with the manifest.

// envelopeTable is the module envelope table in memory.
type envelopeTable struct {
	records []*business.ModuleEnvelopeRecord
}

func (e *envelopeTable) ModuleInstallations(context.Context) ([]business.ModuleInstallationConsent, error) {
	return nil, nil
}

func (e *envelopeTable) LatestModuleEnvelopeRecord(context.Context) (*business.ModuleEnvelopeRecord, error) {
	if len(e.records) == 0 {
		return nil, nil
	}
	return e.records[len(e.records)-1], nil
}

func (e *envelopeTable) LatestDeliveredModuleEnvelopeRecord(context.Context) (*business.ModuleEnvelopeRecord, error) {
	for i := len(e.records) - 1; i >= 0; i-- {
		if e.records[i].WrittenBy == "" {
			return e.records[i], nil
		}
	}
	return nil, nil
}

func (e *envelopeTable) LatestModuleEnvelopeRevision(context.Context) (uint64, error) {
	if len(e.records) == 0 {
		return 0, nil
	}
	return e.records[len(e.records)-1].Revision, nil
}

func (e *envelopeTable) AppendModuleEnvelopeRecord(ctx context.Context, record *business.ModuleEnvelopeRecord, expected uint64) error {
	if current, _ := e.LatestModuleEnvelopeRevision(ctx); current != expected || record.Revision <= expected {
		return business.ErrModuleEnvelopeConflict
	}
	e.records = append(e.records, record)
	return nil
}

func newEnvelopeDeliveryHarness(t *testing.T, pins ...solutionhost.PinnedKey) (*deliveryHarness, *envelopeTable) {
	t.Helper()
	harness := newDeliveryHarness(t, nil)
	service := harness.handler.(*SolutionDeliveryHTTPHandler).service.(*business.Service)
	if len(pins) > 0 {
		keys, err := solutionhost.NewPinnedKeySet(pins...)
		require.NoError(t, err)
		verifier, err := solutionhost.NewVerifier(keys)
		require.NoError(t, err)
		service.SetSolutionDelivery(verifier, harness.store, harness.carrier)
	}
	table := &envelopeTable{}
	service.SetModuleEnvelopeStore(table)
	service.SetModuleEnvelopeDeliveryHost("saas")
	return harness, table
}

func testHostEnvelope(t *testing.T, revision uint64, grant string) *solutionhost.ModuleEnvelope {
	t.Helper()
	envelope := &solutionhost.ModuleEnvelope{
		Schema:     solutionhost.SchemaModuleEnvelopeV1,
		Host:       solutionhost.HostTarget{Coordinate: deliveryTestCoordinate, Domain: deliveryTestDomain},
		Revision:   revision,
		Principals: map[string]json.RawMessage{"reports": json.RawMessage(grant)},
	}
	require.NoError(t, envelope.Validate())
	return envelope
}

func envelopeCarrier(t *testing.T, envelope *solutionhost.ModuleEnvelope, keyID string) []byte {
	t.Helper()
	canonical, err := envelope.CanonicalBytes()
	require.NoError(t, err)
	return deliverytest.Carrier(t, canonical, keyID)
}

type envelopeReceipt struct {
	Disposition string `json:"disposition"`
	Revision    uint64 `json:"revision"`
	Digest      string `json:"digest"`
	Signer      string `json:"signer"`
	Error       string `json:"error"`
}

func TestTheEnvelopePathIsAPostOnTheDeliveryPrefix(t *testing.T) {
	harness, table := newEnvelopeDeliveryHarness(t)
	require.Equal(t, SolutionDeliveryPrefix+"envelope", SolutionDeliveryEnvelopePath)
	require.Equal(t, http.StatusNotFound, harness.post(SolutionDeliveryEnvelopePath+"/extra", []byte("{}")).Code)
	recorder := httptest.NewRecorder()
	harness.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, SolutionDeliveryEnvelopePath, nil))
	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	require.Equal(t, http.MethodPost, recorder.Header().Get("Allow"))
	require.Equal(t, http.StatusBadRequest, harness.post(SolutionDeliveryEnvelopePath, []byte(strings.Repeat("a", (1<<20)+1))).Code)
	require.Empty(t, table.records)
}

func TestADeliveredEnvelopeIsAcceptedThenReplayed(t *testing.T) {
	harness, table := newEnvelopeDeliveryHarness(t)
	envelope := testHostEnvelope(t, 1, `{"queues":["reports.render"]}`)
	carrier := envelopeCarrier(t, envelope, deliveryTestKey)
	digest, err := envelope.Digest()
	require.NoError(t, err)

	for _, disposition := range []string{"accepted", "replayed"} {
		recorder := harness.post(SolutionDeliveryEnvelopePath, carrier)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var receipt envelopeReceipt
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &receipt))
		require.Equal(t, envelopeReceipt{Disposition: disposition, Revision: 1, Digest: digest, Signer: deliveryTestKey}, receipt)
	}
	require.Len(t, table.records, 1)
	require.Equal(t, "platform-acme-staging-saas", harness.carrier.namespace, "the host's own namespace carries its envelope")
}

func TestEnvelopeDeliveryRefusalsMapOntoTheDeliveryTaxonomy(t *testing.T) {
	const grant = `{"queues":["reports.render"]}`
	cases := []struct {
		name    string
		pins    []solutionhost.PinnedKey
		carrier error
		seed    *business.ModuleEnvelopeRecord
		before  func(t *testing.T) []byte
		body    func(t *testing.T) []byte
		code    int
		reason  string
	}{
		{name: "not a carrier", body: func(*testing.T) []byte { return []byte(`{"document":1}`) }, code: 400},
		{name: "a grant naming a workload", code: 400, reason: "workload", body: func(t *testing.T) []byte {
			return envelopeCarrier(t, testHostEnvelope(t, 1, `{"workload":"reports"}`), deliveryTestKey)
		}},
		{name: "unpinned key", code: 401, body: func(t *testing.T) []byte {
			return envelopeCarrier(t, testHostEnvelope(t, 1, grant), "someone-else")
		}},
		{name: "person key", code: 403, reason: "release key",
			pins: []solutionhost.PinnedKey{{KeyID: "person-key", PublicKeyPEM: deliverytest.PublicKey(t, "person-key"),
				Kind: solutionhost.KeyKindPerson, OwnershipDomains: []string{deliveryTestDomain}}},
			body: func(t *testing.T) []byte { return envelopeCarrier(t, testHostEnvelope(t, 1, grant), "person-key") }},
		{name: "not the host's delivery account", code: 403,
			carrier: fmt.Errorf("%w: platform-acme-staging-reports/delivery", infra.ErrCarrierNotAuthorized),
			body:    func(t *testing.T) []byte { return envelopeCarrier(t, testHostEnvelope(t, 1, grant), deliveryTestKey) }},
		{name: "an organization pinned", code: 422, reason: "names no organization", body: func(t *testing.T) []byte {
			return envelopeCarrier(t, testHostEnvelope(t, 1, `{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661"}`), deliveryTestKey)
		}},
		{name: "another host", code: 422, body: func(t *testing.T) []byte {
			envelope := testHostEnvelope(t, 1, grant)
			envelope.Host.Coordinate = "another-host"
			return envelopeCarrier(t, envelope, deliveryTestKey)
		}},
		{name: "at an interactive revision", code: 409, reason: "written interactively",
			seed: &business.ModuleEnvelopeRecord{Revision: 2, Document: []byte(`{"revision":2,"principals":{}}`), WrittenBy: "019f6bf7-5b4b-74e5-8c17-0000000000a1"},
			body: func(t *testing.T) []byte { return envelopeCarrier(t, testHostEnvelope(t, 2, grant), deliveryTestKey) }},
		{name: "a stale revision", code: 409, reason: "older",
			before: func(t *testing.T) []byte { return envelopeCarrier(t, testHostEnvelope(t, 2, grant), deliveryTestKey) },
			body:   func(t *testing.T) []byte { return envelopeCarrier(t, testHostEnvelope(t, 1, grant), deliveryTestKey) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			harness, table := newEnvelopeDeliveryHarness(t, tc.pins...)
			if tc.seed != nil {
				table.records = append(table.records, tc.seed)
			}
			if tc.before != nil {
				require.Equal(t, http.StatusOK, harness.post(SolutionDeliveryEnvelopePath, tc.before(t)).Code)
			}
			harness.carrier.err = tc.carrier
			recorder := harness.post(SolutionDeliveryEnvelopePath, tc.body(t))
			require.Equal(t, tc.code, recorder.Code, recorder.Body.String())
			var receipt envelopeReceipt
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &receipt))
			require.NotEmpty(t, receipt.Error)
			require.Contains(t, receipt.Error, tc.reason)
		})
	}
}
