package business

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	setup := func(store *fakeEnvelopeStore) *Service {
		s := &Service{store: reviewControlPlaneStore{}}
		s.SetModuleIdentitySecrets(map[string][sha256.Size]byte{testModulePrefix: sha256.Sum256([]byte("example-secret"))})
		grant := parseTestEnvelope(t).Principals[testModulePrefix].Grant
		s.installDerivedModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID(testModulePrefix): grant}, 3)
		s.SetModuleEnvelopeStore(store)
		return s
	}
	ctx := context.Background()

	current := setup(&fakeEnvelopeStore{latest: 3})
	identity, err := current.ModuleAuthorizeWorkContext(ctx, testModulePrefix, "example-secret")
	require.NoError(t, err)
	operation, err := current.ModuleAuthorizeOperationContext(ctx, testModulePrefix, "example-secret", "run")
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
			stale := setup(store)
			_, err := stale.ModuleAuthorizeWorkContext(ctx, testModulePrefix, "example-secret")
			require.Equal(t, codes.Unavailable, status.Code(err))
			_, err = stale.ModuleAuthorizeOperationContext(ctx, testModulePrefix, "example-secret", "run")
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
}

func (*registryReviewStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *registryReviewStore) ListSolutionHostBindings(context.Context) ([]*SolutionHostBindingRecord, error) {
	return s.applied, s.appliedErr
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

func (s *registryReviewInbox) ListNewestDeliveredDocuments(_ context.Context, kind SolutionDeliveryKind) ([]*SolutionDeliveryRecord, error) {
	if err := s.newestErr[kind]; err != nil {
		return nil, err
	}
	return s.newest[kind], nil
}

func (s *registryReviewInbox) ListDeliveredDocuments(_ context.Context, kind SolutionDeliveryKind) ([]*SolutionDeliveryRecord, error) {
	s.historyReads++
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
// unit. Fresh and stale inputs are never combined: the pass publishes the last
// confirmed entry or less, and honours a presence withdrawal delivery has made.
func TestAFailedPresenceReadNeverAddsAuthority(t *testing.T) {
	newer := func(r *registryReview) {
		document := *r.authority
		document.Generation = 2
		document.Principals = []solutionhost.PrincipalAuthority{{Principal: testModulePrefix,
			Bindings: []solutionhost.AuthorityBinding{testUnit("run", "invoke"), testUnit("run", "lookup"), testUnit("run", "headless")}}}
		r.inbox.deliver(authorityRecord(t, &document))
		r.store.appliedErr = errors.New("applied state unreadable")
	}

	t.Run("the fresh headless unit is not combined with stale presence", func(t *testing.T) {
		r := newRegistryReview(t)
		before := r.service.ModulePrincipals()[ModulePrincipalID(testModulePrefix)]
		require.Empty(t, before.OperationAudiences["run"].HeadlessScopes)
		newer(r)
		require.ErrorIs(t, r.reconciler.RunOnce(context.Background()), ErrModulePrincipalInputsUnreadable)
		after, ok := r.service.ModulePrincipals()[ModulePrincipalID(testModulePrefix)]
		require.True(t, ok)
		require.Empty(t, after.OperationAudiences["run"].HeadlessScopes, "a degraded pass is never wider than the last confirmed one")
		require.Equal(t, before, after)
	})
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
