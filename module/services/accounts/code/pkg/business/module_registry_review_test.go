package business

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Regressions for the second review of the derived module registry, built on
// the reviewer's own probes: in-memory stores and a stub signature verifier,
// with the real parsing, receipt, reconciliation and authorization. Each test
// is the attack, and asserts the refusal by its reason.

// reviewControlPlaneStore runs a control-plane transaction as a plain call.
type reviewControlPlaneStore struct{ Store }

func (reviewControlPlaneStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// reviewLiveAuthority answers every live read as current.
type reviewLiveAuthority struct{}

func (reviewLiveAuthority) LiveModuleAuthority(context.Context, string, string, string) (*LiveModuleAuthority, error) {
	return &LiveModuleAuthority{}, nil
}

// reviewService holds one principal of tenant org-1, as a capability check
// sees it.
func reviewService() *Service {
	service := &Service{store: reviewControlPlaneStore{}}
	service.SetModuleAuthorityReads(reviewLiveAuthority{}, nil)
	service.SetModulePrincipals(ModulePrincipalRegistry{"principal-1": {Prefix: "crm", Tenant: "org-1", Queues: []string{"a"}}})
	return service
}

func reviewCaller() ModuleCaller { return ModuleCaller{PrincipalID: "principal-1", BoundOrg: "org-1"} }

// fakeEnvelopeStore serves one envelope record and a committed revision, and
// can run something during the revision read — the moment a concurrent install
// would land.
type fakeEnvelopeStore struct {
	record      *ModuleEnvelopeRecord
	latest      uint64
	revisionErr error
	duringRead  func()
	reads       int
}

func (f *fakeEnvelopeStore) LatestModuleEnvelopeRecord(context.Context) (*ModuleEnvelopeRecord, error) {
	return f.record, nil
}

func (f *fakeEnvelopeStore) LatestModuleEnvelopeRevision(context.Context) (uint64, error) {
	f.reads++
	if f.duringRead != nil {
		f.duringRead()
	}
	return f.latest, f.revisionErr
}

func (*fakeEnvelopeStore) AppendModuleEnvelopeRecord(context.Context, *ModuleEnvelopeRecord, uint64) error {
	panic("not used")
}

// TestTheFenceChecksTheSnapshotTheGrantCameFrom is finding 1: revision 4 is
// committed elsewhere; this replica holds revision 3's grant, and installs
// revision 4's empty registry DURING the fence's store read. Fencing a second
// load would see revision 4, pass, and hand out revision 3's queue.
func TestTheFenceChecksTheSnapshotTheGrantCameFrom(t *testing.T) {
	s := reviewService()
	s.installDerivedModulePrincipals(ModulePrincipalRegistry{"principal-1": {Prefix: "crm", Tenant: "org-1", Queues: []string{"a"}}}, 3)
	store := &fakeEnvelopeStore{latest: 4}
	s.SetModuleEnvelopeStore(store)
	store.duringRead = func() { s.installDerivedModulePrincipals(ModulePrincipalRegistry{}, 4) }

	authority, err := s.AuthorizeModuleCapability(context.Background(), reviewCaller())
	require.ErrorIs(t, err, ErrModuleRegistryStale, "the grant's own registry predates the committed revision")
	require.Nil(t, authority, "no grant from the revision-3 registry is returned")
	require.Equal(t, codes.Unavailable, status.Code(err))
}

func TestTheFenceRefusesWhenTheCommittedRevisionCannotBeRead(t *testing.T) {
	s := reviewService()
	s.SetModuleEnvelopeStore(&fakeEnvelopeStore{revisionErr: errors.New("unreadable")})
	_, err := s.AuthorizeModuleCapability(context.Background(), reviewCaller())
	require.Equal(t, codes.Unavailable, status.Code(err))
}

// TestOneRequestReadsTheCommittedRevisionOnce: a request scoped with the memo
// pays one revision read however many decisions it makes.
func TestOneRequestReadsTheCommittedRevisionOnce(t *testing.T) {
	s := reviewService()
	s.installDerivedModulePrincipals(ModulePrincipalRegistry{"principal-1": {Prefix: "crm", Tenant: "org-1", Queues: []string{"a"}}}, 3)
	store := &fakeEnvelopeStore{latest: 3}
	s.SetModuleEnvelopeStore(store)
	ctx := WithModuleEnvelopeRevisionMemo(context.Background())
	for range 3 {
		_, err := s.AuthorizeModuleCapability(ctx, reviewCaller())
		require.NoError(t, err)
	}
	require.Equal(t, 1, store.reads)
}

// TestMintsAndRevisionConfirmationReadTheCommittedRevision is finding 4: a
// replica that has not observed a committed narrowing (revision 4) still holds
// revision 3. The work-context mint, the headless operation-context mint and
// the consumer's revision confirmation all go through the durable fence — and
// with the revision unreadable, they refuse rather than answer from memory.
func TestMintsAndRevisionConfirmationReadTheCommittedRevision(t *testing.T) {
	setup := func(store *fakeEnvelopeStore) (*Service, string) {
		s := &Service{store: reviewControlPlaneStore{}}
		grant := parseTestEnvelope(t).Principals[testModulePrefix].Grant
		grant.Workload = ModuleWorkload{Namespace: "acme-prod", ServiceAccount: "reports", Container: "app"}
		s.installDerivedModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID(testModulePrefix): grant}, 3)
		s.SetModuleEnvelopeStore(store)
		return s, bindTestModule(s, testModulePrefix)
	}
	ctx := context.Background()

	current, token := setup(&fakeEnvelopeStore{latest: 3})
	identity, err := current.ModuleAuthorizeWorkContext(ctx, testModulePrefix, token)
	require.NoError(t, err)
	operation, err := current.ModuleAuthorizeOperationContext(ctx, testModulePrefix, token, "run")
	require.NoError(t, err)
	subjects := []ModuleOperationRevisionSubject{{PrincipalID: identity.PrincipalID, Scopes: operation.Scopes}}
	handled, err := current.CheckModuleOperationContextRevision(ctx, identity.Tenant, identity.PrincipalID, operation.Revision, subjects)
	require.True(t, handled)
	require.NoError(t, err, "a current registry mints and confirms")

	for name, store := range map[string]*fakeEnvelopeStore{
		"a committed narrowing this replica has not observed": {latest: 4},
		"a committed revision this replica cannot read":       {latest: 3, revisionErr: errors.New("unreadable")},
	} {
		t.Run(name, func(t *testing.T) {
			stale, token := setup(store)
			_, err := stale.ModuleAuthorizeWorkContext(ctx, testModulePrefix, token)
			require.Equal(t, codes.Unavailable, status.Code(err))
			_, err = stale.ModuleAuthorizeOperationContext(ctx, testModulePrefix, token, "run")
			require.Equal(t, codes.Unavailable, status.Code(err))
			handled, err := stale.CheckModuleOperationContextRevision(ctx, identity.Tenant, identity.PrincipalID, operation.Revision, subjects)
			require.True(t, handled, "a module context is still the module path's to answer")
			require.Equal(t, codes.Unavailable, status.Code(err), "and it is not confirmed from memory")
			require.Positive(t, store.reads, "the committed revision was read")
		})
	}
}

// registryReviewStore is the applied-state half of the store.
type registryReviewStore struct {
	Store
	applied    []*SolutionHostBindingRecord
	appliedErr error
	inbox      *registryReviewInbox
	// A failed commit invalidates all reads, including a successful owner read.
	snapshotCommitErr error
	afterAppliedRead  func()
}

func (*registryReviewStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// The fake stores share the context's snapshot, as the Postgres store methods
// share its transaction. Records are immutable; deliveries replace the slices.
type registryReviewSnapshotKey struct{}

type registryReviewSnapshot struct {
	applied []*SolutionHostBindingRecord
	newest  map[SolutionDeliveryKind][]*SolutionDeliveryRecord
	history map[SolutionDeliveryKind][]*SolutionDeliveryRecord
}

func (s *registryReviewStore) WithControlPlaneReadSnapshot(ctx context.Context, fn func(context.Context) error) error {
	snapshot := &registryReviewSnapshot{applied: slices.Clone(s.applied)}
	if s.inbox != nil {
		snapshot.newest, snapshot.history = maps.Clone(s.inbox.newest), maps.Clone(s.inbox.history)
	}
	if err := fn(context.WithValue(ctx, registryReviewSnapshotKey{}, snapshot)); err != nil {
		return err
	}
	return s.snapshotCommitErr
}

func (s *registryReviewStore) ListSolutionHostBindings(ctx context.Context) ([]*SolutionHostBindingRecord, error) {
	records := s.applied
	if snapshot, ok := ctx.Value(registryReviewSnapshotKey{}).(*registryReviewSnapshot); ok {
		records = snapshot.applied
	}
	if after := s.afterAppliedRead; after != nil {
		s.afterAppliedRead = nil
		after()
	}
	return records, s.appliedErr
}

// registryReviewInbox is the delivery inbox, kind by kind: the newest
// generation per document and the whole history.
type registryReviewInbox struct {
	newest       map[SolutionDeliveryKind][]*SolutionDeliveryRecord
	history      map[SolutionDeliveryKind][]*SolutionDeliveryRecord
	historyReads int
	// newestErr fails the newest-document read of one kind.
	newestErr map[SolutionDeliveryKind]error
}

func (s *registryReviewInbox) ListNewestDeliveredDocuments(ctx context.Context, kind SolutionDeliveryKind) ([]*SolutionDeliveryRecord, error) {
	if err := s.newestErr[kind]; err != nil {
		return nil, err
	}
	if snapshot, ok := ctx.Value(registryReviewSnapshotKey{}).(*registryReviewSnapshot); ok {
		return snapshot.newest[kind], nil
	}
	return s.newest[kind], nil
}

func (s *registryReviewInbox) ListDeliveredDocuments(ctx context.Context, kind SolutionDeliveryKind) ([]*SolutionDeliveryRecord, error) {
	s.historyReads++
	if snapshot, ok := ctx.Value(registryReviewSnapshotKey{}).(*registryReviewSnapshot); ok {
		return snapshot.history[kind], nil
	}
	return s.history[kind], nil
}

func (s *registryReviewInbox) RecordDeliveredDocument(_ context.Context, record *SolutionDeliveryRecord) (bool, error) {
	s.deliver(record)
	return true, nil
}

// deliver adds a generation to the history and makes it the newest of its
// document.
func (s *registryReviewInbox) deliver(record *SolutionDeliveryRecord) {
	s.history[record.Kind] = append(s.history[record.Kind], record)
	var kept []*SolutionDeliveryRecord
	for _, newest := range s.newest[record.Kind] {
		if newest.DocumentID != record.DocumentID {
			kept = append(kept, newest)
		}
	}
	s.newest[record.Kind] = append(kept, record)
}

type permissiveCarrier struct{}

func (permissiveCarrier) AuthorizeCarrier(context.Context, string, SolutionDeliveryKind, []string) error {
	return nil
}

func authorityRecord(t *testing.T, document *solutionhost.AuthorityDocument) *SolutionDeliveryRecord {
	t.Helper()
	payload, err := document.CanonicalBytes()
	require.NoError(t, err)
	return &SolutionDeliveryRecord{Kind: SolutionDeliveryAuthority, DocumentID: document.Authority,
		Generation: document.Generation, Payload: payload, Bundle: json.RawMessage(solutionhost.FixtureBundle)}
}

func presenceRecord(t *testing.T, document *solutionhost.SolutionHostBinding) *SolutionDeliveryRecord {
	t.Helper()
	payload, err := document.CanonicalBytes()
	require.NoError(t, err)
	return &SolutionDeliveryRecord{Kind: SolutionDeliveryPresence, DocumentID: document.Binding,
		Generation: document.Generation, Payload: payload, Bundle: json.RawMessage(solutionhost.FixtureBundle)}
}

func tombstoneOf(t *testing.T, document *solutionhost.AuthorityDocument) *solutionhost.AuthorityDocument {
	t.Helper()
	removed := *document
	removed.Generation++
	removed.Removed = true
	removed.ApprovedBuild, removed.EffectiveFrom, removed.Principals = "", 0, nil
	removed.Queues, removed.Namespaces, removed.ScopeCeilings = []string{}, []string{}, []solutionhost.ScopeCeiling{}
	require.NoError(t, removed.Validate())
	return &removed
}

type registryReview struct {
	service    *Service
	reconciler *ModulePrincipalReconciler
	inbox      *registryReviewInbox
	store      *registryReviewStore
	verifier   *stubBundleVerifier
	authority  *solutionhost.AuthorityDocument
	presence   *solutionhost.SolutionHostBinding
}

// newRegistryReview is one module whose authority and applied presence are
// derived into the registry at envelope revision 3.
func newRegistryReview(t *testing.T) *registryReview {
	t.Helper()
	authority := testAuthority(t, testUnit("run", "invoke"), testUnit("run", "lookup"))
	raw, err := solutionhost.FixtureDocument(solutionhost.DocumentTypePresence, "valid")
	require.NoError(t, err)
	presence, err := solutionhost.Parse(raw)
	require.NoError(t, err)
	presence.Binding, presence.EnvelopeRevision, presence.Generation = testPresenceBinding, 3, 1
	presence.Host, presence.OwnershipDomain = authority.Host, authority.OwnershipDomain
	presence.Workloads[0].Image.Digest = testEnvelopeBuild
	presence.Workloads[0].Identity.SPIFFEID = testSPIFFEID
	presence.Workloads[0].Container = testContainer
	canonical, err := presence.CanonicalBytes()
	require.NoError(t, err)

	store := &registryReviewStore{applied: []*SolutionHostBindingRecord{{
		BindingID: testPresenceBinding,
		Applied: &SolutionHostBindingApplied{Domain: authority.OwnershipDomain,
			SolutionHostBindingGeneration: SolutionHostBindingGeneration{Generation: 1, Document: string(canonical)}},
	}}}
	inbox := &registryReviewInbox{newest: map[SolutionDeliveryKind][]*SolutionDeliveryRecord{}, history: map[SolutionDeliveryKind][]*SolutionDeliveryRecord{}}
	store.inbox = inbox
	inbox.deliver(presenceRecord(t, presence))
	inbox.deliver(authorityRecord(t, authority))
	verifier := &stubBundleVerifier{signer: "example-signer"}
	service := &Service{store: store, deliveryStore: inbox}
	service.SetModuleEnvelopeStore(&fakeEnvelopeStore{latest: 3, record: &ModuleEnvelopeRecord{Revision: 3, Document: encodeEnvelope(t, testEnvelopeDocument())}})
	service.SetModuleAuthorityReads(reviewLiveAuthority{}, nil)
	host := solutionhost.Host{
		Coordinate:      authority.Host.Coordinate,
		Domains:         []string{authority.OwnershipDomain, "other-domain"},
		DomainsBySigner: map[string][]string{"example-signer": {authority.OwnershipDomain}, "foreign-signer": {"other-domain"}},
	}
	activation := &SolutionAuthorityActivation{reconciler: &SolutionHostBindingReconciler{service: service, host: host, verifier: verifier}, envelopes: service}
	reconciler, err := NewModulePrincipalReconciler(activation)
	require.NoError(t, err)
	require.NoError(t, reconciler.RunOnce(context.Background()))
	require.Contains(t, service.ModulePrincipals(), ModulePrincipalID(testModulePrefix))
	return &registryReview{service: service, reconciler: reconciler, inbox: inbox, store: store, verifier: verifier, authority: authority, presence: presence}
}

func (r *registryReview) present() bool {
	_, ok := r.service.ModulePrincipals()[ModulePrincipalID(testModulePrefix)]
	return ok
}

// TestAWithdrawalIsHonouredWhenTheNewestDocumentNoLongerVerifies is finding 2,
// scenario A: the binding is withdrawn at generation 2, and the newest document
// over it no longer verifies. The withdrawal history is read first and on its
// own, so the principal is gone — not reinstated from the last pass.
func TestAWithdrawalIsHonouredWhenTheNewestDocumentNoLongerVerifies(t *testing.T) {
	r := newRegistryReview(t)
	r.inbox.deliver(authorityRecord(t, tombstoneOf(t, r.authority)))
	later := *r.authority
	later.Generation = 3
	r.inbox.deliver(authorityRecord(t, &later))
	r.verifier.err = errors.New("signer no longer verifies")

	before := r.inbox.historyReads
	err := r.reconciler.RunOnce(context.Background())
	require.Greater(t, r.inbox.historyReads, before, "the withdrawal history is read whatever the newest documents do")
	require.ErrorIs(t, err, ErrSolutionAuthorityUnverifiable, "the unverifiable document is refused by name")
	require.False(t, r.present(), "a withdrawn binding is never reinstated from an earlier pass")
	_, err = r.service.AuthorizeModuleCapability(context.Background(), ModuleCaller{PrincipalID: ModulePrincipalID(testModulePrefix), BoundOrg: testEnvelopeTenant})
	require.Error(t, err)
}

// TestAWithdrawalIsHonouredWhileTheNewestDocumentsCannotBeRead: the newest
// authority read fails outright, so the pass is degraded — and a degraded pass
// still drops the entry whose binding the readable history withdrew.
func TestAWithdrawalIsHonouredWhileTheNewestDocumentsCannotBeRead(t *testing.T) {
	r := newRegistryReview(t)
	r.inbox.deliver(authorityRecord(t, tombstoneOf(t, r.authority)))
	r.inbox.newestErr = map[SolutionDeliveryKind]error{SolutionDeliveryAuthority: errors.New("newest documents unreadable")}
	require.ErrorIs(t, r.reconciler.RunOnce(context.Background()), ErrModulePrincipalInputsUnreadable)
	require.False(t, r.present(), "a degraded registry honours every withdrawal that could be read")
}

// TestAnUnreadableWithdrawalHistoryKeepsNothing: with no way to establish the
// withdrawals, no entry can be kept.
func TestAnUnreadableWithdrawalHistoryKeepsNothing(t *testing.T) {
	r := newRegistryReview(t)
	failing := &failingHistoryInbox{registryReviewInbox: r.inbox}
	r.service.deliveryStore = failing
	require.ErrorIs(t, r.reconciler.RunOnce(context.Background()), ErrModulePrincipalInputsUnreadable)
	require.False(t, r.present())
}

type failingHistoryInbox struct{ *registryReviewInbox }

func (*failingHistoryInbox) ListDeliveredDocuments(context.Context, SolutionDeliveryKind) ([]*SolutionDeliveryRecord, error) {
	return nil, errors.New("history unreadable")
}

// TestAnUnverifiableDocumentGrantsNothingAtANewRevision: cached inputs never
// carry authority across an envelope revision.
func TestAnUnverifiableDocumentGrantsNothingAtANewRevision(t *testing.T) {
	r := newRegistryReview(t)
	document := testEnvelopeDocument()
	document["revision"] = float64(4)
	r.service.SetModuleEnvelopeStore(&fakeEnvelopeStore{latest: 4, record: &ModuleEnvelopeRecord{Revision: 4, Document: encodeEnvelope(t, document)}})
	r.verifier.err = errors.New("inbox cannot verify")
	require.Error(t, r.reconciler.RunOnce(context.Background()))
	require.Empty(t, r.service.ModulePrincipals())
}

// TestAFailedPresenceReadNeverAddsAuthority is finding 2, scenario B: the
// applied-state read fails while a fresh authority generation adds a headless
// unit. Fresh and stale inputs are never combined, and a cached entry never
// outlives the document it came from — superseded here by generation 2.
func TestAFailedPresenceReadNeverAddsAuthority(t *testing.T) {
	newer := func(r *registryReview) {
		document := *r.authority
		document.Generation = 2
		document.Principals = []solutionhost.PrincipalAuthority{{Principal: testModulePrefix,
			Bindings: []solutionhost.AuthorityBinding{testUnit("run", "invoke"), testUnit("run", "lookup"), testUnit("run", "headless")}}}
		r.inbox.deliver(authorityRecord(t, &document))
		r.store.appliedErr = errors.New("applied state unreadable")
	}

	for name, fail := range map[string]func(*registryReviewStore){
		"the applied-state read fails": func(s *registryReviewStore) { s.appliedErr = errors.New("applied state unreadable") },
		"the snapshot commit fails after the owner read": func(s *registryReviewStore) {
			s.appliedErr = nil
			s.snapshotCommitErr = errors.New("snapshot commit failed")
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRegistryReview(t)
			newer(r)
			fail(r.store)
			require.ErrorIs(t, r.reconciler.RunOnce(context.Background()), ErrModulePrincipalInputsUnreadable)
			after, present := r.service.ModulePrincipals()[ModulePrincipalID(testModulePrefix)]
			require.Empty(t, after.OperationAudiences["run"].HeadlessScopes, "a degraded pass is never wider than the last confirmed one")
			require.False(t, present, "the cached entry's document was superseded, so it is not kept")
		})
	}
	t.Run("a presence withdrawal delivery made is honoured", func(t *testing.T) {
		r := newRegistryReview(t)
		removed := *r.presence
		removed.Generation = 2
		removed.Removed = true
		removed.Routes, removed.Artifacts, removed.Workloads, removed.Modules, removed.Endpoints = nil, nil, nil, nil, nil
		require.NoError(t, removed.Validate())
		r.inbox.deliver(presenceRecord(t, &removed))
		newer(r)
		require.ErrorIs(t, r.reconciler.RunOnce(context.Background()), ErrModulePrincipalInputsUnreadable)
		require.False(t, r.present(), "the withdrawn presence takes its principal with it even while applied state is unreadable")
	})
}

// TestAnUnverifiableAuthorityDoesNotSurviveAFailedPresenceRead is the third
// review's residual A: the verifier refuses the document this pass while the
// applied-state read fails. The refusal is known, so the degraded pass drops
// the cached entry, including when the snapshot fails after reading the owner.
func TestAnUnverifiableAuthorityDoesNotSurviveAFailedPresenceRead(t *testing.T) {
	for name, fail := range map[string]func(*registryReviewStore){
		"the applied-state read fails": func(s *registryReviewStore) { s.appliedErr = errors.New("applied state unreadable") },
		"the snapshot commit fails after the owner read": func(s *registryReviewStore) {
			s.appliedErr = nil
			s.snapshotCommitErr = errors.New("snapshot commit failed")
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRegistryReview(t)
			r.verifier.err = errors.New("signer removed from verification trust policy")
			r.store.appliedErr = errors.New("applied presence unreadable")
			fail(r.store)
			require.ErrorIs(t, r.reconciler.RunOnce(context.Background()), ErrModulePrincipalInputsUnreadable)
			require.False(t, r.present(), "a cached grant never survives a document this pass refused")
			_, err := r.service.AuthorizeModuleCapability(context.Background(), ModuleCaller{PrincipalID: ModulePrincipalID(testModulePrefix), BoundOrg: testEnvelopeTenant})
			require.Error(t, err)
		})
	}
}

// TestARefusedForeignPresenceEstablishesNoOwnership is the third review's
// residual B: the binding is admitted under domain A at generation 5; a foreign
// signer delivers generation 1 under domain B, which core's lifecycle admission
// refuses. The owner is the ADMITTED generation's domain, from the applied
// state — so B's tombstone is refused at receipt and, if stored anyway,
// withdraws nothing.
func TestARefusedForeignPresenceEstablishesNoOwnership(t *testing.T) {
	setup := func(t *testing.T) *registryReview {
		r := newRegistryReview(t)
		own := *r.presence
		own.Generation = 5
		canonical, err := own.CanonicalBytes()
		require.NoError(t, err)
		digest, err := own.Digest()
		require.NoError(t, err)
		r.store.applied[0].Applied.Generation, r.store.applied[0].Applied.Document, r.store.applied[0].Applied.Digest = 5, string(canonical), digest
		r.inbox.history[SolutionDeliveryPresence], r.inbox.newest[SolutionDeliveryPresence] = nil, nil
		r.inbox.deliver(presenceRecord(t, &own))
		require.NoError(t, r.reconciler.RunOnce(context.Background()))
		require.True(t, r.present())

		policy := map[string][]string{"example-signer": {r.authority.OwnershipDomain}, "foreign-signer": {"other-domain"}}
		r.service.SetSolutionDelivery(&stubBundleVerifier{signer: "foreign-signer"}, r.inbox, permissiveCarrier{}, policy)
		r.service.SetSolutionDeliveryCoordinate(r.authority.Host.Coordinate)
		foreign := *r.presence
		foreign.OwnershipDomain, foreign.Generation = "other-domain", 1
		foreign.Workloads = append([]solutionhost.Workload(nil), r.presence.Workloads...)
		foreign.Workloads[0].Identity.SPIFFEID = "spiffe://acme.test/ns/other-prod/sa/other"
		_, _, err = r.service.ReceiveSolutionDelivery(context.Background(),
			SolutionDeliveryCarrier{Kind: SolutionDeliveryPresence, Raw: carrierOver(t, presenceRecord(t, &foreign).Payload)}, "carrier")
		require.NoError(t, err, "receipt keeps the row; core's admission is what refuses it")
		host := r.reconciler.activation.reconciler.host
		state, ok := r.store.applied[0].AppliedState()
		require.True(t, ok)
		host.Applied = []solutionhost.Applied{state}
		foreignDelivered, err := solutionhost.VerifyDelivered(context.Background(), carrierOf(presenceRecord(t, &foreign)), &stubBundleVerifier{signer: "foreign-signer"})
		require.NoError(t, err)
		_, err = host.Admit(foreignDelivered)
		require.ErrorIs(t, err, solutionhost.ErrWrongDomain)
		r.inbox.newest[SolutionDeliveryPresence] = []*SolutionDeliveryRecord{presenceRecord(t, &own)}
		return r
	}
	foreignTombstone := func(r *registryReview) *solutionhost.AuthorityDocument {
		tombstone := tombstoneOf(t, r.authority)
		tombstone.Authority, tombstone.OwnershipDomain = "foreign-authority", "other-domain"
		return tombstone
	}

	t.Run("refused at receipt", func(t *testing.T) {
		r := setup(t)
		_, _, err := r.service.ReceiveSolutionDelivery(context.Background(),
			SolutionDeliveryCarrier{Kind: SolutionDeliveryAuthority, Raw: carrierOver(t, authorityRecord(t, foreignTombstone(r)).Payload)}, "carrier")
		require.ErrorIs(t, err, ErrSolutionDeliveryDomainNotGranted)
	})
	t.Run("ignored in history", func(t *testing.T) {
		r := setup(t)
		r.inbox.deliver(authorityRecord(t, foreignTombstone(r)))
		if err := r.reconciler.RunOnce(context.Background()); err != nil {
			require.NotErrorIs(t, err, ErrSolutionAuthorityWithdrawn)
		}
		require.True(t, r.present(), "the binding's admitted owner is still domain A")
		require.Equal(t, r.authority.OwnershipDomain, r.store.applied[0].Applied.Domain)
	})
}

// TestAForeignTombstoneWithdrawsNothing is finding 3: a tombstone speaking for
// another ownership domain, or addressed to another host, cannot withdraw a
// binding — refused at receipt, and ignored in history if it is there anyway.
// A tombstone from the binding's own domain and host still withdraws it.
func TestAForeignTombstoneWithdrawsNothing(t *testing.T) {
	foreign := map[string]func(*solutionhost.AuthorityDocument){
		"another domain": func(document *solutionhost.AuthorityDocument) { document.OwnershipDomain = "other-domain" },
		"another host":   func(document *solutionhost.AuthorityDocument) { document.Host.Coordinate = "another-host" },
	}
	for name, mutate := range foreign {
		t.Run(name+" is refused at receipt", func(t *testing.T) {
			r := newRegistryReview(t)
			tombstone := tombstoneOf(t, r.authority)
			tombstone.Authority = "other-authority"
			mutate(tombstone)
			policy := map[string][]string{"example-signer": {r.authority.OwnershipDomain, "other-domain"}}
			r.service.SetSolutionDelivery(&stubBundleVerifier{signer: "example-signer"}, r.inbox, permissiveCarrier{}, policy)
			r.service.SetSolutionDeliveryCoordinate(r.authority.Host.Coordinate)
			_, _, err := r.service.ReceiveSolutionDelivery(context.Background(),
				SolutionDeliveryCarrier{Kind: SolutionDeliveryAuthority, Raw: carrierOver(t, authorityRecord(t, tombstone).Payload)}, "carrier")
			require.ErrorIs(t, err, ErrSolutionDeliveryDomainNotGranted)
		})
		t.Run(name+" in history is ignored", func(t *testing.T) {
			r := newRegistryReview(t)
			tombstone := tombstoneOf(t, r.authority)
			tombstone.Authority = "other-authority"
			mutate(tombstone)
			r.inbox.deliver(authorityRecord(t, tombstone))
			if err := r.reconciler.RunOnce(context.Background()); err != nil {
				require.NotErrorIs(t, err, ErrSolutionAuthorityWithdrawn)
			}
			require.True(t, r.present(), "a tombstone that could not speak for the binding withdraws nothing")
		})
	}
	t.Run("the binding's own tombstone withdraws it", func(t *testing.T) {
		r := newRegistryReview(t)
		renamed := tombstoneOf(t, r.authority)
		renamed.Authority = "renamed-authority"
		r.inbox.deliver(authorityRecord(t, renamed))
		_ = r.reconciler.RunOnce(context.Background())
		require.False(t, r.present())
	})
}

// TestAnAdmittedWithdrawalSurvivesRenameAndRestart: once admitted, a withdrawal
// is terminal for the binding — a later generation of the same id or a renamed
// authority grants nothing, for a reconciler built after a restart too.
func TestAnAdmittedWithdrawalSurvivesRenameAndRestart(t *testing.T) {
	r := newRegistryReview(t)
	r.inbox.deliver(authorityRecord(t, tombstoneOf(t, r.authority)))
	later := *r.authority
	later.Generation = 3
	r.inbox.deliver(authorityRecord(t, &later))
	renamed := *r.authority
	renamed.Authority, renamed.Generation = "new-authority", 1
	for _, document := range []*solutionhost.AuthorityDocument{&later, &renamed} {
		r.inbox.deliver(authorityRecord(t, document))
		restarted, err := NewModulePrincipalReconciler(r.reconciler.activation)
		require.NoError(t, err)
		require.ErrorIs(t, restarted.RunOnce(context.Background()), ErrSolutionAuthorityWithdrawn)
		require.False(t, r.present())
	}
}

// referenceExchangeStore serves one source delegation, and can run something
// while it is read — the moment a concurrent registry install would land.
type referenceExchangeStore struct {
	Store
	delegation *SourceDelegation
	duringGet  func()
}

func (*referenceExchangeStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *referenceExchangeStore) GetSourceDelegation(context.Context, string, string) (*SourceDelegation, error) {
	if s.duringGet != nil {
		s.duringGet()
	}
	return s.delegation, nil
}

func (*referenceExchangeStore) SourceDelegationFacts(context.Context, string, string, string) (*SourceDelegationFacts, error) {
	return &SourceDelegationFacts{SourceExists: true, MemberRole: "admin", UserStatus: "active", PrincipalRevision: 42}, nil
}

func (s *referenceExchangeStore) ActiveSourceDelegationsForPrincipal(context.Context, string, string, string) ([]*SourceDelegation, error) {
	return []*SourceDelegation{s.delegation}, nil
}

// referenceExchange is revision 3 — a worker allowed to write, and no source
// module — and revision 4 — the source module restored, the worker narrowed to
// reads. Neither registry admits a write exchange through the delegation.
func referenceExchange(t *testing.T) (*Service, *referenceExchangeStore, *fakeEnvelopeStore, ModuleCaller, ModulePrincipalRegistry) {
	t.Helper()
	read := []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"read"}}}
	write := []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"write"}}}
	both := []ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"read", "write"}}}
	sourceBinding := ModuleOperationAudience{Audience: "worker", InvokeScopes: both, LookupScopes: read, SourceDelegationScopes: write}
	worker := ModulePrincipalGrant{Prefix: "worker", Tenant: testEnvelopeTenant,
		OperationAudiences: map[string]ModuleOperationAudience{"run": {Audience: "sink", InvokeScopes: both, LookupScopes: read}}}
	source := ModulePrincipalGrant{Prefix: "source", Tenant: "another-org", OperationAudiences: map[string]ModuleOperationAudience{"sync": sourceBinding}}
	narrowed := worker
	narrowed.OperationAudiences = map[string]ModuleOperationAudience{"run": {Audience: "sink", InvokeScopes: read, LookupScopes: read}}
	store := &referenceExchangeStore{delegation: &SourceDelegation{ID: "example-delegation", OrgID: "delegated-org",
		PrincipalID: "example-person", SourceID: "example-source", ModulePrefix: "source", BindingID: "sync",
		BindingDigest: SourceDelegationBindingDigest(sourceBinding)}}
	s := &Service{store: store}
	s.SetModuleAuthorityReads(reviewLiveAuthority{}, nil)
	envelopes := &fakeEnvelopeStore{latest: 3}
	s.SetModuleEnvelopeStore(envelopes)
	s.installDerivedModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID("worker"): worker}, 3)
	return s, store, envelopes, ModuleCaller{PrincipalID: ModulePrincipalID("worker"), BoundOrg: testEnvelopeTenant},
		ModulePrincipalRegistry{ModulePrincipalID("worker"): narrowed, ModulePrincipalID("source"): source}
}

// TestAReferenceExchangeReadsBothPrincipalsFromOneSnapshot is the third
// review's residual C: revision 4 is installed after the caller's fence and
// before the delegator is looked up. Both are read from the snapshot the fence
// passed, so the revision-3 worker's write is never paired with the revision-4
// source — and a revision-4 decision refuses too.
func TestAReferenceExchangeReadsBothPrincipalsFromOneSnapshot(t *testing.T) {
	s, store, envelopes, caller, newer := referenceExchange(t)
	_, err := s.AuthorizeDelegationReferenceExchange(context.Background(), caller, store.delegation.ID, "run", false)
	require.ErrorIs(t, err, ErrSourceDelegationInvalid, "revision 3 has no source module")

	store.duringGet = func() {
		envelopes.latest = 4
		s.installDerivedModulePrincipals(newer, 4)
	}
	_, err = s.AuthorizeDelegationReferenceExchange(WithModuleEnvelopeRevisionMemo(context.Background()), caller, store.delegation.ID, "run", false)
	require.ErrorIs(t, err, ErrSourceDelegationInvalid, "the delegator is read from the caller's snapshot, never a later install")

	store.duringGet = nil
	_, err = s.AuthorizeDelegationReferenceExchange(context.Background(), caller, store.delegation.ID, "run", false)
	require.ErrorIs(t, err, ErrSourceDelegationInvalid, "revision 4's narrowed worker cannot write either")
}

// TestAReferenceExchangeFencesAnUnobservantReplica: the exchange refuses a
// registry older than the committed revision, or one it cannot compare.
func TestAReferenceExchangeFencesAnUnobservantReplica(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		s, store, envelopes, caller, _ := referenceExchange(t)
		envelopes.latest = 4
		if unreadable {
			envelopes.revisionErr = errors.New("revision unavailable")
		}
		_, err := s.AuthorizeDelegationReferenceExchange(context.Background(), caller, store.delegation.ID, "run", false)
		require.Equal(t, codes.Unavailable, status.Code(err))
	}
}

// The reviewer's interleaving, inverted: neither committed state grants a
// principal. Before admission it has no presence; after admission its owner's
// tombstone is terminal. A pass spanning the commit must grant nothing too.
func TestFirstAdmissionBetweenOwnerAndDerivationReadsCannotRestoreWithdrawnAuthority(t *testing.T) {
	r := newRegistryReview(t)
	applied := r.store.applied
	r.store.applied = nil
	r.inbox.deliver(authorityRecord(t, tombstoneOf(t, r.authority)))
	renamed := *r.authority
	renamed.Authority = "renamed-authority"
	r.inbox.deliver(authorityRecord(t, &renamed))
	ctx := context.Background()
	require.ErrorIs(t, r.reconciler.RunOnce(ctx), ErrModulePrincipalOmitted)
	require.False(t, r.present(), "no applied presence before the concurrent admission")

	// Core really admits the presence: the authority tombstone does not prevent
	// another replica committing this first presence generation.
	delivered, err := solutionhost.VerifyDelivered(ctx, carrierOf(presenceRecord(t, r.presence)), r.verifier)
	require.NoError(t, err)
	_, err = r.reconciler.activation.reconciler.host.Admit(delivered)
	require.NoError(t, err)
	committed := false
	r.store.afterAppliedRead = func() {
		r.store.applied = applied
		committed = true
	}
	err = r.reconciler.RunOnce(ctx)
	require.True(t, committed, "the other replica committed after the owner read")
	require.False(t, r.present(), "the renamed grant must not combine new presence with an ownerless withdrawal map")
	require.ErrorIs(t, err, ErrModulePrincipalOmitted)
	grant, err := r.service.AuthorizeModuleCapability(ctx, ModuleCaller{PrincipalID: ModulePrincipalID(testModulePrefix), BoundOrg: testEnvelopeTenant})
	require.Error(t, err)
	require.Nil(t, grant)

	// No delivery or envelope change: the next snapshot sees both the admitted
	// owner and the tombstone, and still grants nothing.
	require.ErrorIs(t, r.reconciler.RunOnce(ctx), ErrSolutionAuthorityWithdrawn)
	require.False(t, r.present())
}

func TestRegistryInputsKeepConcurrentWithdrawalInTheNextSnapshot(t *testing.T) {
	r := newRegistryReview(t)
	removed := authorityRecord(t, tombstoneOf(t, r.authority))
	r.store.afterAppliedRead = func() { r.inbox.deliver(removed) }
	inputs, err := r.reconciler.activation.readRegistryInputs(context.Background())
	require.NoError(t, err)
	withdrawn, err := r.reconciler.activation.withdrawnIn(inputs)
	require.NoError(t, err)
	require.Empty(t, withdrawn, "history must be from the same snapshot as the applied presence")
	require.Len(t, inputs.newestAuthority, 1)
	require.Equal(t, r.authority.Generation, inputs.newestAuthority[0].Generation)

	// The committed withdrawal is visible on the following pass.
	require.NoError(t, r.reconciler.RunOnce(context.Background()))
	require.False(t, r.present())
}
