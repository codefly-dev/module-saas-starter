package business

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Regressions for the second review of the derived module registry: the
// envelope fence every grant is checked against, with in-memory stores and the
// real authorization. Each test is the attack, and asserts the refusal by its
// reason.

// reviewControlPlaneStore runs a control-plane transaction as a plain call.
type reviewControlPlaneStore struct{ Store }

func (reviewControlPlaneStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// reviewLiveAuthority answers every live read as current.
type reviewLiveAuthority struct{ service *Service }

func (f reviewLiveAuthority) LiveModuleAuthority(_ context.Context, principal string, _ string, _ string) (*LiveModuleAuthority, error) {
	grant := f.service.ModulePrincipals()[principal]
	return &LiveModuleAuthority{InstallationID: "current", RegistryBindingID: grant.PresenceBinding, Scope: FullInstallationScope(grant)}, nil
}

// reviewService holds one principal of tenant org-1, as a capability check
// sees it.
func reviewService() *Service {
	service := &Service{store: reviewControlPlaneStore{}}
	service.SetModuleAuthorityReads(reviewLiveAuthority{service: service}, nil)
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
	s.InstallDerivedModulePrincipals(ModulePrincipalRegistry{"principal-1": {Prefix: "crm", Tenant: "org-1", Queues: []string{"a"}}}, 3, nil)
	store := &fakeEnvelopeStore{latest: 4}
	s.SetModuleEnvelopeStore(store)
	store.duringRead = func() { s.InstallDerivedModulePrincipals(ModulePrincipalRegistry{}, 4, nil) }

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
	s.InstallDerivedModulePrincipals(ModulePrincipalRegistry{"principal-1": {Prefix: "crm", Tenant: "org-1", Queues: []string{"a"}}}, 3, nil)
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
		grant.Workloads = []ModuleWorkload{testWorkload}
		s.InstallDerivedModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID(testModulePrefix): grant}, 3, nil)
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

func (*fakeEnvelopeStore) ModuleInstallations(context.Context) ([]ModuleInstallationConsent, error) {
	return nil, nil
}

func (f reviewLiveAuthority) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

func (*fakeEnvelopeStore) LatestDeliveredModuleEnvelopeRecord(context.Context) (*ModuleEnvelopeRecord, error) {
	panic("not used")
}
