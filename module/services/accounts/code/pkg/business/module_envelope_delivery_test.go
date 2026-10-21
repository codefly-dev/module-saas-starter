//go:build pure

package business

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"accounts/internal/deliverytest"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// A delivered module envelope, end to end through the business layer with
// in-memory stores and real signatures: verification, the carrier, the host's
// rules, admission, the shared writer, the witness and the audit record.

const (
	testHostModule      = "saas"
	testCarrierToken    = "Bearer host-delivery"
	testAdministratorID = "019f6bf7-5b4b-74e5-8c17-0000000000a1"
)

// envelopeTx tags every context a control-plane transaction runs, so a test
// can tell that the row and its audit event were written in the same one.
type envelopeTx struct{}

type envelopeTxStore struct {
	Store
	next int
}

func (s *envelopeTxStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	s.next++
	return fn(context.WithValue(ctx, envelopeTx{}, s.next))
}

// memoryEnvelopeStore is the append-only table in memory.
type memoryEnvelopeStore struct {
	records []*ModuleEnvelopeRecord
	tx      []any
}

func (m *memoryEnvelopeStore) ModuleInstallations(context.Context) ([]ModuleInstallationConsent, error) {
	return nil, nil
}

func (m *memoryEnvelopeStore) LatestModuleEnvelopeRecord(context.Context) (*ModuleEnvelopeRecord, error) {
	if len(m.records) == 0 {
		return nil, nil
	}
	return m.records[len(m.records)-1], nil
}

func (m *memoryEnvelopeStore) LatestDeliveredModuleEnvelopeRecord(context.Context) (*ModuleEnvelopeRecord, error) {
	for i := len(m.records) - 1; i >= 0; i-- {
		if m.records[i].WrittenBy == "" {
			return m.records[i], nil
		}
	}
	return nil, nil
}

func (m *memoryEnvelopeStore) LatestModuleEnvelopeRevision(context.Context) (uint64, error) {
	if len(m.records) == 0 {
		return 0, nil
	}
	return m.records[len(m.records)-1].Revision, nil
}

func (m *memoryEnvelopeStore) AppendModuleEnvelopeRecord(ctx context.Context, record *ModuleEnvelopeRecord, expected uint64) error {
	current, _ := m.LatestModuleEnvelopeRevision(ctx)
	if record.Revision <= expected || current != expected {
		return ErrModuleEnvelopeConflict
	}
	record.WrittenAt = time.Now()
	m.records = append(m.records, record)
	m.tx = append(m.tx, ctx.Value(envelopeTx{}))
	return nil
}

type recordingCarrier struct {
	namespaces []string
}

func (c *recordingCarrier) AuthorizeCarrier(_ context.Context, credential, namespace string) error {
	c.namespaces = append(c.namespaces, namespace)
	if credential != testCarrierToken {
		return errors.New("not the delivery account")
	}
	return nil
}

type recordingTxAudit struct {
	entries []AuditEntry
	tx      []any
}

func (a *recordingTxAudit) Emit(context.Context, AuditEntry) {}
func (a *recordingTxAudit) EmitTx(ctx context.Context, entry AuditEntry) error {
	a.entries = append(a.entries, entry)
	a.tx = append(a.tx, ctx.Value(envelopeTx{}))
	return nil
}

type envelopeDeliveryHarness struct {
	service *Service
	store   *memoryEnvelopeStore
	carrier *recordingCarrier
	audit   *recordingTxAudit
	passes  int
}

func newEnvelopeDeliveryHarness(t *testing.T, verifier *solutionhost.Verifier) *envelopeDeliveryHarness {
	t.Helper()
	h := &envelopeDeliveryHarness{store: &memoryEnvelopeStore{}, carrier: &recordingCarrier{}, audit: &recordingTxAudit{}}
	h.service = &Service{store: &envelopeTxStore{}}
	h.service.SetModuleEnvelopeStore(h.store)
	h.service.SetSolutionDelivery(verifier, struct{ SolutionDeliveryStore }{}, h.carrier)
	h.service.SetSolutionDeliveryCoordinate(testDeliveryCoordinate)
	h.service.SetModuleEnvelopeDeliveryHost(testHostModule)
	h.service.SetAuditEmitter(h.audit)
	h.service.SetModuleRegistryPassTrigger(func() { h.passes++ })
	return h
}

func (h *envelopeDeliveryHarness) deliver(t *testing.T, carrier []byte) (*ModuleEnvelopeDeliveryReceipt, error) {
	t.Helper()
	return h.service.ReceiveModuleEnvelopeDelivery(context.Background(), carrier, testCarrierToken)
}

// deliveredGrant is the test grant as a composition states it: no tenant.
func deliveredGrant(t *testing.T, mutate func(map[string]any)) json.RawMessage {
	t.Helper()
	grant := testEnvelopeDocument()["principals"].(map[string]any)[testModulePrefix].(map[string]any)["grant"].(map[string]any)
	delete(grant, "tenant")
	if mutate != nil {
		mutate(grant)
	}
	raw, err := json.Marshal(grant)
	require.NoError(t, err)
	return raw
}

func testDeliveredEnvelope(t *testing.T, revision uint64, domain string, grant json.RawMessage) *solutionhost.ModuleEnvelope {
	t.Helper()
	envelope := &solutionhost.ModuleEnvelope{
		Schema:     solutionhost.SchemaModuleEnvelopeV1,
		Host:       solutionhost.HostTarget{Coordinate: testDeliveryCoordinate, Domain: domain},
		Revision:   revision,
		Principals: map[string]json.RawMessage{testModulePrefix: grant},
	}
	require.NoError(t, envelope.Validate())
	return envelope
}

func signedEnvelope(t *testing.T, envelope *solutionhost.ModuleEnvelope, keyID string) []byte {
	t.Helper()
	canonical, err := envelope.CanonicalBytes()
	require.NoError(t, err)
	return deliverytest.Carrier(t, canonical, keyID)
}

func TestADeliveredEnvelopeIsWrittenAndTheRegistryDerivesFromIt(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
	envelope := testDeliveredEnvelope(t, 1, testDeliveryDomain, deliveredGrant(t, nil))

	receipt, err := h.deliver(t, signedEnvelope(t, envelope, testDeliveryKey))
	require.NoError(t, err)
	digest, err := envelope.Digest()
	require.NoError(t, err)
	require.Equal(t, &ModuleEnvelopeDeliveryReceipt{Disposition: SolutionDeliveryAccepted, Revision: 1, Digest: digest, Signer: testDeliveryKey}, receipt)
	require.Equal(t, []string{testHostModule}, h.carrier.namespaces, "the carrier is the host's own delivery account")
	require.Equal(t, 1, h.passes)

	require.Len(t, h.store.records, 1)
	row := h.store.records[0]
	require.Empty(t, row.WrittenBy, "no person wrote a delivered revision")
	require.Equal(t, testDeliveryKey, row.Signer)
	require.Equal(t, testDeliveryDomain, row.OwnershipDomain)
	canonical, _ := envelope.CanonicalBytes()
	require.Equal(t, canonical, row.Payload)
	require.NotEmpty(t, row.Envelope)

	current, err := h.service.CurrentModuleEnvelope(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(1), current.Revision)
	grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, current.Principals[testModulePrefix],
		testManifest(t, map[string][]string{"invoke": {"profiles:read"}}), []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.Empty(t, grant.Tenant, "tenancy resolves through each organization's installation")
	require.Equal(t, []string{"reports.render"}, grant.Queues)

	require.Len(t, h.audit.entries, 1)
	event := h.audit.entries[0]
	require.Equal(t, EventModuleEnvelopeDelivered, event.EventType)
	require.Equal(t, moduleEnvelopeDeliveryActor, event.ActorID)
	require.Equal(t, ActorTypeSystem, event.ActorType)
	require.Equal(t, map[string]any{
		"revision": int64(1), "previous_revision": int64(0), "principals": 1,
		"signer": testDeliveryKey, "ownership_domain": testDeliveryDomain, "digest": digest,
	}, event.Payload)
	require.NotNil(t, h.audit.tx[0])
	require.Equal(t, h.store.tx[0], h.audit.tx[0], "the row and its audit event commit in one transaction")
}

func TestARedeliveredEnvelopeIsAReplayAndWritesNothing(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
	carrier := signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, deliveredGrant(t, nil)), testDeliveryKey)
	_, err := h.deliver(t, carrier)
	require.NoError(t, err)

	receipt, err := h.deliver(t, carrier)
	require.NoError(t, err)
	require.Equal(t, SolutionDeliveryReplayed, receipt.Disposition)
	require.Len(t, h.store.records, 1)
	require.Len(t, h.audit.entries, 1)
}

func TestAStaleOrRewrittenEnvelopeRevisionConflicts(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
	_, err := h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 2, testDeliveryDomain, deliveredGrant(t, nil)), testDeliveryKey))
	require.NoError(t, err)

	_, err = h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, deliveredGrant(t, nil)), testDeliveryKey))
	require.ErrorIs(t, err, ErrSolutionDeliveryConflict)
	require.ErrorIs(t, err, solutionhost.ErrEnvelopeStaleRevision)

	rewritten := deliveredGrant(t, func(g map[string]any) { g["queues"] = []any{"reports.render"} })
	_, err = h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 2, testDeliveryDomain, rewritten), testDeliveryKey))
	require.ErrorIs(t, err, ErrSolutionDeliveryConflict)
	require.ErrorIs(t, err, solutionhost.ErrEnvelopeRewrittenRevision)
	require.Len(t, h.store.records, 1)
}

// An administrator's interactive revision is never silently replaced: a
// delivery at or below it is refused naming it, and one above it lands.
func TestADeliveryAtOrBelowAnInteractiveRevisionIsRefusedNamingIt(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
	same, err := deliveredModuleEnvelopeDocument(testDeliveredEnvelope(t, 3, testDeliveryDomain, deliveredGrant(t, nil)))
	require.NoError(t, err)
	h.store.records = append(h.store.records, &ModuleEnvelopeRecord{Revision: 3, Document: same, WrittenBy: testAdministratorID})

	for _, revision := range []uint64{2, 3} {
		_, err := h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, revision, testDeliveryDomain, deliveredGrant(t, nil)), testDeliveryKey))
		require.ErrorIs(t, err, ErrSolutionDeliveryConflict)
		require.ErrorContains(t, err, "revision 3 was written interactively by a platform administrator")
	}
	receipt, err := h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 4, testDeliveryDomain, deliveredGrant(t, nil)), testDeliveryKey))
	require.NoError(t, err)
	require.Equal(t, SolutionDeliveryAccepted, receipt.Disposition)
	require.Equal(t, int64(3), h.audit.entries[0].Payload["previous_revision"])
}

func TestADeliveredEnvelopeNamesNoOrganization(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
	pinned := deliveredGrant(t, func(g map[string]any) { g["tenant"] = testEnvelopeTenant })
	_, err := h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, pinned), testDeliveryKey))
	require.ErrorIs(t, err, ErrSolutionDeliveryRefused)
	require.ErrorContains(t, err, "a delivered module envelope names no organization")
	require.Empty(t, h.store.records)

	shared := deliveredGrant(t, func(g map[string]any) { g["tenant"] = "" })
	_, err = h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, shared), testDeliveryKey))
	require.NoError(t, err, "an empty tenant is the shared tenancy, not an organization")

	// The rule reads the PARSED envelope, so it sees the field the host will
	// actually apply. A literal lookup in the raw grant was a second decoder
	// of the one field this rule is about, and encoding/json's
	// case-insensitive fallback made the two disagree: "Tenant" passed the
	// lookup and was then stored and applied as a pin, which makes
	// moduleAvailableToOrganization false everywhere else and revokes every
	// active installation of that module in every other organization.
	for _, spelling := range []string{"Tenant", "TENANT", "tEnAnT"} {
		t.Run(spelling, func(t *testing.T) {
			h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
			variant := deliveredGrant(t, func(g map[string]any) {
				delete(g, "tenant")
				g[spelling] = testEnvelopeTenant
			})
			_, err := h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, variant), testDeliveryKey))
			require.ErrorIs(t, err, ErrSolutionDeliveryRefused)
			require.ErrorContains(t, err, "a delivered module envelope names no organization")
			require.Empty(t, h.store.records)
		})
	}
}

func TestAGrantTheHostRefusesIsMalformedByName(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
	withWorkload := deliveredGrant(t, func(g map[string]any) { g["workload"] = "reports" })
	_, err := h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, withWorkload), testDeliveryKey))
	require.ErrorIs(t, err, ErrSolutionDeliveryMalformed)
	require.ErrorIs(t, err, ErrModuleEnvelopeInvalid)
	require.ErrorContains(t, err, "workload")
	require.Empty(t, h.store.records)
}

func TestAPersonKeyCannotDeliverAnEnvelope(t *testing.T) {
	const personKey = "example-person"
	keys, err := solutionhost.NewPinnedKeySet(solutionhost.PinnedKey{
		KeyID: personKey, PublicKeyPEM: deliverytest.PublicKey(t, personKey),
		Kind: solutionhost.KeyKindPerson, OwnershipDomains: []string{testDeliveryDomain},
	})
	require.NoError(t, err)
	verifier, err := solutionhost.NewVerifier(keys)
	require.NoError(t, err)
	h := newEnvelopeDeliveryHarness(t, verifier)

	_, err = h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, deliveredGrant(t, nil)), personKey))
	require.ErrorIs(t, err, ErrSolutionDeliverySignerNotRelease)
	require.Empty(t, h.store.records)
	require.Empty(t, h.carrier.namespaces, "the signature is judged before the carrier")
}

func TestAnEnvelopeForAnotherDomainOrHostIsRefused(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, deliverytest.Verifier(t, map[string][]string{
		testDeliveryKey: {testDeliveryDomain, "beta"}, "other-release": {"beta"},
	}))
	// A release key not pinned for the domain the envelope speaks for.
	_, err := h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, deliveredGrant(t, nil)), "other-release"))
	require.ErrorIs(t, err, ErrSolutionDeliveryDomainNotGranted)

	// The first delivered domain owns the host's envelope.
	_, err = h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, deliveredGrant(t, nil)), testDeliveryKey))
	require.NoError(t, err)
	_, err = h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 2, "beta", deliveredGrant(t, nil)), testDeliveryKey))
	require.ErrorIs(t, err, ErrSolutionDeliveryDomainNotGranted)

	elsewhere := testDeliveredEnvelope(t, 2, testDeliveryDomain, deliveredGrant(t, nil))
	elsewhere.Host.Coordinate = "another-host"
	_, err = h.deliver(t, signedEnvelope(t, elsewhere, testDeliveryKey))
	require.ErrorIs(t, err, ErrSolutionDeliveryRefused)
	require.Len(t, h.store.records, 1)
}

func TestTheCarrierIsTheHostsOwnNamespaceDeliveryAccount(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
	rule, err := NewModuleNamespaceRule("platform-example-"+testHostModule, testHostModule)
	require.NoError(t, err)
	h.service.SetModuleNamespaceRule(rule)
	carrier := signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, deliveredGrant(t, nil)), testDeliveryKey)

	_, err = h.service.ReceiveModuleEnvelopeDelivery(context.Background(), carrier, "Bearer a-module-delivery")
	require.ErrorContains(t, err, "not the delivery account")
	require.Empty(t, h.store.records)
	require.Equal(t, []string{"platform-example-" + testHostModule}, h.carrier.namespaces)
}

func TestEnvelopeDeliveryFailsClosedUntilFullyWired(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
	h.service.SetModuleEnvelopeDeliveryHost("")
	_, err := h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, deliveredGrant(t, nil)), testDeliveryKey))
	require.ErrorIs(t, err, ErrSolutionDeliveryUnattested)
	require.Empty(t, h.store.records)
}

// A delivered narrowing goes through the witness exactly as an interactive one:
// refused with nothing written where no policy log is wired, appended to the
// log under the delivery's actor where one is.
func TestADeliveredNarrowingIsWitnessedLikeAnInteractiveOne(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
	_, err := h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, deliveredGrant(t, nil)), testDeliveryKey))
	require.NoError(t, err, "a first envelope narrows nothing")

	narrower := signedEnvelope(t, testDeliveredEnvelope(t, 2, testDeliveryDomain,
		deliveredGrant(t, func(g map[string]any) { g["queues"] = []any{"reports.render"} })), testDeliveryKey)
	_, err = h.deliver(t, narrower)
	require.ErrorIs(t, err, ErrPolicyLogUnreachable)
	require.Len(t, h.store.records, 1, "an unwitnessed narrowing writes nothing")

	log := newFakePolicyLog()
	h.service.SetPolicyLog(log, newFakePolicyLogStore())
	receipt, err := h.deliver(t, narrower)
	require.NoError(t, err)
	require.Equal(t, SolutionDeliveryAccepted, receipt.Disposition)
	require.Len(t, h.store.records, 2)
	require.Len(t, log.appends, 1)
	require.Equal(t, moduleEnvelopeDeliveryActor, log.appends[0].Actor)
	require.Equal(t, uint64(2), log.appends[0].EnvelopeRevision)
	require.True(t, bytes.Contains(h.store.records[1].Document, []byte(`"revision":2`)))
}

// UNPINNING THE KEY WITHDRAWS THE CEILING IT STATED.
//
// A delivered envelope is somebody else's statement, so every read holds it to
// the custody that may make it — the way the manifest half of the same
// reconcile pass re-verifies every applied manifest. Parsing proves the
// document is well formed, not that its signer may still state it. Without
// this, unpinning a compromised release key — the operator's revocation lever
// — withdrew nothing: the registry is manifest intersect envelope, the
// manifests are signed by other keys and keep re-verifying, so the ceiling the
// compromised key stated would keep granting until an administrator wrote a
// higher revision by hand.
func TestADeliveredEnvelopeStopsGrantingOnceItsKeyIsUnpinned(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
	_, err := h.deliver(t, signedEnvelope(t, testDeliveredEnvelope(t, 1, testDeliveryDomain, deliveredGrant(t, nil)), testDeliveryKey))
	require.NoError(t, err)

	envelope, err := h.service.CurrentModuleEnvelope(context.Background())
	require.NoError(t, err)
	require.NotNil(t, envelope, "the delivered ceiling is in force while its key is pinned")

	// The key is rotated out of the host's pinned set, exactly as an operator
	// revokes a compromised one.
	rotated, err := solutionhost.NewPinnedKeySet(solutionhost.PinnedKey{
		KeyID: "example-other-release", PublicKeyPEM: deliverytest.PublicKey(t, "example-other-release"),
		Kind: solutionhost.KeyKindRelease, OwnershipDomains: []string{testDeliveryDomain},
	})
	require.NoError(t, err)
	verifier, err := solutionhost.NewVerifier(rotated)
	require.NoError(t, err)
	h.service.SetSolutionDelivery(verifier, struct{ SolutionDeliveryStore }{}, h.carrier)

	_, err = h.service.CurrentModuleEnvelope(context.Background())
	require.Error(t, err, "a ceiling whose signer is no longer pinned must stop granting")
	require.ErrorContains(t, err, "no longer verifies against this host's pinned keys")
}

// An interactively written revision carries no carrier: it is this host's own
// record, there is nothing to re-verify, and a host with no verifier at all
// (the native profile) still reads it.
func TestAnInteractiveEnvelopeNeedsNoSignature(t *testing.T) {
	h := newEnvelopeDeliveryHarness(t, fixtureSigner(t))
	interactive := testEnvelopeDocument()
	interactive["revision"] = 1
	h.store.records = append(h.store.records, &ModuleEnvelopeRecord{
		Revision: 1, Document: encodeEnvelope(t, interactive), WrittenBy: "someone", WrittenAt: time.Now(),
	})
	envelope, err := h.service.CurrentModuleEnvelope(context.Background())
	require.NoError(t, err)
	require.NotNil(t, envelope)

	h.service.deliveryVerifier = nil
	envelope, err = h.service.CurrentModuleEnvelope(context.Background())
	require.NoError(t, err, "a host with no delivery verifier still reads its own interactive ceiling")
	require.NotNil(t, envelope)
}
