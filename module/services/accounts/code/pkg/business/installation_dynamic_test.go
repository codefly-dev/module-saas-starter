//go:build pure

package business

import (
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type installationDecisionStore struct {
	rows       map[string]*LiveModuleAuthority
	calls      []string
	duringRead func()
}

func (f *installationDecisionStore) LiveModuleAuthority(_ context.Context, principal, org, id string) (*LiveModuleAuthority, error) {
	key := org + "/" + principal
	f.calls = append(f.calls, key)
	if f.duringRead != nil {
		f.duringRead()
	}
	row := f.rows[key]
	if row == nil || id != "" && row.InstallationID != id {
		return nil, ErrModuleInstallationInactive
	}
	return row, nil
}
func dynamicInstallationFixture() (*Service, *installationDecisionStore, ModuleCaller) {
	grant := ModulePrincipalGrant{Prefix: "example", Tenant: "org-a", CrossTenant: true, PresenceBinding: "binding-example",
		Queues: []string{"example.ingest"}, Resources: []string{"records"},
		OperationAudiences: map[string]ModuleOperationAudience{"operate": {Audience: "example.api",
			InvokeScopes: []ModuleOperationScope{{ResourceKind: "records", Actions: []string{"read", "write"}, ResourceIDs: []string{"one", "two"}}},
			LookupScopes: []ModuleOperationScope{{ResourceKind: "records", Actions: []string{"read"}, ResourceIDs: []string{"one", "two"}}}}}}
	readonly := FullInstallationScope(grant)
	readonly.Queues = nil
	readBinding := readonly.OperationAudiences["operate"]
	readBinding.InvokeScopes = []ModuleOperationScope{{ResourceKind: "records", Actions: []string{"read"}, ResourceIDs: []string{"one"}}}
	readBinding.LookupScopes = cloneOperationScopes(readBinding.InvokeScopes)
	readonly.OperationAudiences["operate"] = readBinding
	f := &installationDecisionStore{rows: map[string]*LiveModuleAuthority{
		"org-a/principal": {InstallationID: "installation-a", InstallationRevision: 3, RegistryBindingID: "binding-example", Scope: FullInstallationScope(grant)},
		"org-b/principal": {InstallationID: "installation-b", InstallationRevision: 7, RegistryBindingID: "binding-example", Scope: readonly},
	}}
	service := &Service{store: noopControlPlaneStore{}}
	service.SetModulePrincipals(ModulePrincipalRegistry{"principal": grant})
	service.SetModuleAuthorityReads(f, nil)
	return service, f, ModuleCaller{PrincipalID: "principal", BoundOrg: "org-a"}
}

func TestDynamicInstallationIsolationAndAttribution(t *testing.T) {
	s, f, caller := dynamicInstallationFixture()
	a, err := s.AuthorizeModuleCapability(context.Background(), caller)
	require.NoError(t, err)
	require.Equal(t, "installation-a", a.InstallationID)
	require.Equal(t, uint64(3), a.InstallationRevision)
	require.True(t, a.Grant.allowsQueue("example.ingest"))
	caller.BoundOrg = "org-b"
	b, err := s.AuthorizeModuleCapability(context.Background(), caller)
	require.NoError(t, err)
	require.Equal(t, "installation-b", b.InstallationID)
	require.False(t, b.Grant.allowsQueue("example.ingest"))
	require.Equal(t, []string{"read"}, b.Grant.OperationAudiences["operate"].InvokeScopes[0].Actions)
	require.Equal(t, []string{"one"}, b.Grant.OperationAudiences["operate"].InvokeScopes[0].ResourceIDs)
	caller.BoundOrg = "org-c"
	_, err = s.AuthorizeModuleCapability(context.Background(), caller)
	require.ErrorIs(t, err, ErrModuleInstallationInactive)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Equal(t, []string{"org-a/principal", "org-b/principal", "org-c/principal"}, f.calls)
	// A's seal never selects A's consent while claiming to act in B.
	caller.BoundOrg = "org-b"
	caller.Sealed = &SealedModuleAuthority{InstallationID: &a.InstallationID, InstallationRevision: &a.InstallationRevision}
	_, err = s.AuthorizeModuleCapability(context.Background(), caller)
	require.ErrorIs(t, err, ErrModuleInstallationInactive)
	require.Error(t, authorizeTenant(ModuleCaller{PrincipalID: "principal", BoundOrg: "org-a"}, a.Grant, "org-c"))
}

func TestDynamicInstallationScopeCeilingAtInstallAndUse(t *testing.T) {
	s, f, caller := dynamicInstallationFixture()
	grant := s.ModulePrincipals()["principal"]
	full := FullInstallationScope(grant)
	require.NoError(t, validateInstallationScope(full, grant))
	for _, mutate := range []func(*InstallationScope){
		func(s *InstallationScope) { s.Queues = append(s.Queues, "foreign.queue") },
		func(s *InstallationScope) {
			b := s.OperationAudiences["operate"]
			b.InvokeScopes[0].Actions = []string{"delete"}
			s.OperationAudiences["operate"] = b
		},
		func(s *InstallationScope) {
			b := s.OperationAudiences["operate"]
			b.InvokeScopes[0].ResourceIDs = nil
			s.OperationAudiences["operate"] = b
		},
		func(s *InstallationScope) {
			b := s.OperationAudiences["operate"]
			b.Audience = "foreign.api"
			s.OperationAudiences["operate"] = b
		},
	} {
		wider := FullInstallationScope(grant)
		mutate(&wider)
		require.ErrorContains(t, validateInstallationScope(wider, grant), "exceeds")
	}
	// Persisted consent cannot reinstate authority removed from the envelope.
	grant.Queues = nil
	binding := grant.OperationAudiences["operate"]
	binding.InvokeScopes = []ModuleOperationScope{{ResourceKind: "records", Actions: []string{"read"}, ResourceIDs: []string{"two"}}}
	grant.OperationAudiences["operate"] = binding
	s.SetModulePrincipals(ModulePrincipalRegistry{"principal": grant})
	a, err := s.AuthorizeModuleCapability(context.Background(), caller)
	require.NoError(t, err)
	require.Empty(t, a.Grant.Queues)
	require.Equal(t, []string{"read"}, a.Grant.OperationAudiences["operate"].InvokeScopes[0].Actions)
	require.Equal(t, []string{"two"}, a.Grant.OperationAudiences["operate"].InvokeScopes[0].ResourceIDs)
	require.Equal(t, full, f.rows["org-a/principal"].Scope, "deciding must not mutate persisted consent")
}

func TestDynamicInstallationNarrowingRefusesNextUse(t *testing.T) {
	s, f, caller := dynamicInstallationFixture()
	a, err := s.AuthorizeModuleCapability(context.Background(), caller)
	require.NoError(t, err)
	stamp := a.StampDeferredWork()
	caller.Sealed = &SealedModuleAuthority{InstallationID: &a.InstallationID, InstallationRevision: &a.InstallationRevision}
	row := f.rows["org-a/principal"]
	row.Scope.Queues = nil
	row.InstallationRevision++
	_, err = s.AuthorizeModuleCapability(context.Background(), caller)
	require.ErrorIs(t, err, ErrModuleAuthorityStale)
	require.ErrorContains(t, err, "live revision is 4")
	_, err = s.AuthorizeDeferredWork(context.Background(), stamp)
	require.ErrorIs(t, err, ErrDeferredWorkRevoked)
	caller.Sealed = nil
	narrowed, err := s.AuthorizeModuleCapability(context.Background(), caller)
	require.NoError(t, err)
	require.Equal(t, uint64(4), narrowed.InstallationRevision)
	require.Empty(t, narrowed.Grant.Queues)
	delete(f.rows, "org-a/principal")
	_, err = s.AuthorizeModuleCapability(context.Background(), caller)
	require.ErrorIs(t, err, ErrModuleInstallationInactive)
	caller.BoundOrg = "org-b"
	other, err := s.AuthorizeModuleCapability(context.Background(), caller)
	require.NoError(t, err)
	require.Equal(t, uint64(7), other.InstallationRevision)
}

func TestDynamicInstallationSnapshotAndRegistryIdentity(t *testing.T) {
	s, f, caller := dynamicInstallationFixture()
	row := f.rows["org-a/principal"]
	row.RegistryBindingID = "replacement-binding"
	_, err := s.AuthorizeModuleCapability(context.Background(), caller)
	require.ErrorIs(t, err, ErrModuleInstallationInactive)
	row.RegistryBindingID = "binding-example"
	revision := uint64(2)
	row.EnvelopeRevision = &revision
	_, err = s.AuthorizeModuleCapability(context.Background(), caller)
	require.ErrorIs(t, err, ErrModuleRegistryStale)
	row.EnvelopeRevision = nil
	f.duringRead = func() { s.SetModulePrincipals(ModulePrincipalRegistry{}) }
	a, err := s.AuthorizeModuleCapability(context.Background(), caller)
	require.NoError(t, err)
	require.Equal(t, "binding-example", a.Grant.PresenceBinding, "one captured registry throughout the decision")
}

type availableInstallationStore struct {
	Store
	targets []*AvailableSolutionTarget
}

func (f availableInstallationStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f availableInstallationStore) ListAvailableSolutionTargets(_ context.Context, q AvailableSolutionQuery) ([]*AvailableSolutionTarget, error) {
	var out []*AvailableSolutionTarget
	for _, target := range f.targets {
		if target.TargetID > q.Cursor && (q.TargetID == "" || target.TargetID == q.TargetID) && (q.BindingID == "" || target.BindingID == q.BindingID) {
			out = append(out, target)
			if len(out) == q.Limit {
				break
			}
		}
	}
	return out, nil
}
func TestDynamicInstallationTenancyFiltersBeforePaginationAndInstall(t *testing.T) {
	s := &Service{store: availableInstallationStore{targets: []*AvailableSolutionTarget{
		{TargetID: "1", BindingID: "dedicated"}, {TargetID: "2", BindingID: "shared"}, {TargetID: "3", BindingID: "cross"},
	}}}
	s.SetModulePrincipals(ModulePrincipalRegistry{
		"dedicated": {PresenceBinding: "dedicated", Tenant: "org-b"},
		"shared":    {PresenceBinding: "shared"},
		"cross":     {PresenceBinding: "cross", Tenant: "org-b", CrossTenant: true},
	})
	page, next, err := s.ListAvailableSolutions(context.Background(), "org-a", "", 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "2", page[0].TargetID)
	require.Equal(t, "2", next)
	page, next, err = s.ListAvailableSolutions(context.Background(), "org-a", next, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "3", page[0].TargetID)
	require.Empty(t, next)
	_, err = s.prepareInstallation(context.Background(), &InstallSolutionParams{OrgID: "org-a", TargetID: "1"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = s.prepareInstallation(context.Background(), &InstallSolutionParams{OrgID: "org-b", TargetID: "1"})
	require.NoError(t, err)
}

func TestDynamicInstallationJobStampCannotBeForged(t *testing.T) {
	s, _, caller := dynamicInstallationFixture()
	a, err := s.AuthorizeModuleCapability(context.Background(), caller)
	require.NoError(t, err)
	req := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{Attributes: map[string]string{"codefly.installation_id": "foreign", "codefly.organization_id": "org-c", "ordinary": "kept"}}}
	stamped := jobs.StampModuleJob(req, a.JobAuthority())
	require.Equal(t, "installation-a", stamped.Job.Attributes["codefly.installation_id"])
	require.Equal(t, "org-a", stamped.Job.Attributes["codefly.organization_id"])
	require.Equal(t, "3", stamped.Job.Attributes["codefly.installation_revision"])
	require.Equal(t, "principal", stamped.Job.Attributes["codefly.principal_id"])
	require.Equal(t, "binding-example", stamped.Job.Attributes["codefly.binding_id"])
	require.Equal(t, "kept", stamped.Job.Attributes["ordinary"])
	require.Equal(t, "foreign", req.Job.Attributes["codefly.installation_id"], "do not mutate caller data")
}

func TestDynamicInstallationMintRequiresConsent(t *testing.T) {
	s, f, caller := dynamicInstallationFixture()
	grant := s.ModulePrincipals()[caller.PrincipalID]
	grant.Workloads = []ModuleWorkload{ModuleWorkload{Namespace: "example", ServiceAccount: "example", Container: "app"}}
	id := ModulePrincipalID(grant.Prefix)
	s.SetModulePrincipals(ModulePrincipalRegistry{id: grant})
	f.rows["org-a/"+id] = f.rows["org-a/principal"]
	token := bindTestModule(s, grant.Prefix)
	identity, err := s.ModuleAuthorizeWorkContext(context.Background(), grant.Prefix, token)
	require.NoError(t, err)
	require.Equal(t, "installation-a", identity.InstallationID)
	require.Equal(t, uint64(3), identity.InstallationRevision)
	delete(f.rows, "org-a/"+id)
	_, err = s.ModuleAuthorizeWorkContext(context.Background(), grant.Prefix, token)
	require.ErrorIs(t, err, ErrModuleInstallationInactive)
}

func TestDynamicInstallationEmptyBackfillGrantsNothing(t *testing.T) {
	s, f, caller := dynamicInstallationFixture()
	f.rows["org-a/principal"].Scope = InstallationScope{}
	_, err := s.AuthorizeModuleCapability(context.Background(), caller)
	require.ErrorIs(t, err, ErrModuleInstallationInactive)
}

func TestDynamicInstallationProxyUsesConsentAndCurrentScope(t *testing.T) {
	s, f, _ := dynamicInstallationFixture()
	state := &InstallationAuthorityState{Kind: "module", PrincipalID: "principal", BindingID: "binding-example", Revision: 3}
	allowed, err := s.installationEntitlementCurrent(context.Background(), s.moduleRegistrySnapshot(), "org-a", "installation-a", state)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = s.installationEntitlementCurrent(context.Background(), s.moduleRegistrySnapshot(), "org-b", "installation-a", state)
	require.NoError(t, err)
	require.False(t, allowed, "another org's consent cannot admit a proxy")
	f.rows["org-a/principal"].Scope.Capabilities = false
	f.rows["org-a/principal"].InstallationRevision++
	allowed, err = s.installationEntitlementCurrent(context.Background(), s.moduleRegistrySnapshot(), "org-a", "installation-a", state)
	require.NoError(t, err)
	require.False(t, allowed)
	state.PrincipalID = ""
	allowed, err = s.installationEntitlementCurrent(context.Background(), s.moduleRegistrySnapshot(), "org-a", "installation-a", state)
	require.NoError(t, err)
	require.False(t, allowed, "a backfilled module gets no proxy authority")
	state.Kind = "solution"
	allowed, err = s.installationEntitlementCurrent(context.Background(), s.moduleRegistrySnapshot(), "org-a", "installation-a", state)
	require.NoError(t, err)
	require.False(t, allowed, "a legacy solution cannot gain module access through a later declaration")
}

func TestDynamicInstallationTenancyRevocationSelection(t *testing.T) {
	principal := ModulePrincipalID("example")
	envelope := &ModuleEnvelope{Principals: map[string]ModuleEnvelopePrincipal{"example": {Grant: ModulePrincipalGrant{Tenant: "org-b"}}}}
	require.False(t, installationTenancyAllowed(envelope, principal, "org-a"))
	require.True(t, installationTenancyAllowed(envelope, principal, "org-b"))
	entry := envelope.Principals["example"]
	entry.Grant.CrossTenant = true
	envelope.Principals["example"] = entry
	require.True(t, installationTenancyAllowed(envelope, principal, "org-a"))
	require.False(t, installationTenancyAllowed(envelope, ModulePrincipalID("foreign"), "org-a"))
}

func TestDynamicInstallationMintReleaseFence(t *testing.T) {
	s, f, caller := dynamicInstallationFixture()
	identity := ModuleWorkContextAuthority{PrincipalID: caller.PrincipalID, Tenant: caller.BoundOrg, InstallationID: "installation-a", InstallationRevision: 3, snapshot: s.moduleRegistrySnapshot()}
	require.NoError(t, s.FenceModuleMint(context.Background(), identity))
	f.rows["org-a/principal"].InstallationRevision++
	require.ErrorIs(t, s.FenceModuleMint(context.Background(), identity), ErrModuleAuthorityStale)
	delete(f.rows, "org-a/principal")
	require.ErrorIs(t, s.FenceModuleMint(context.Background(), identity), ErrModuleInstallationInactive)
}

// Supplying the production store must wire its live-read seam automatically.
// A store without that seam still fails closed.
type wiredInstallationStore struct {
	noopControlPlaneStore
	*installationDecisionStore
}

func TestDynamicInstallationServiceUsesSuppliedStore(t *testing.T) {
	original, rows, caller := dynamicInstallationFixture()
	svc, err := NewService(wiredInstallationStore{installationDecisionStore: rows})
	require.NoError(t, err)
	svc.SetModulePrincipals(original.ModulePrincipals())
	authority, err := svc.AuthorizeModuleCapability(context.Background(), caller)
	require.NoError(t, err)
	require.Equal(t, "installation-a", authority.InstallationID)
	delete(rows.rows, "org-a/principal")
	_, err = svc.AuthorizeModuleCapability(context.Background(), caller)
	require.ErrorIs(t, err, ErrModuleInstallationInactive)
	unwired, err := NewService(noopControlPlaneStore{})
	require.NoError(t, err)
	unwired.SetModulePrincipals(original.ModulePrincipals())
	_, err = unwired.AuthorizeModuleCapability(context.Background(), caller)
	require.Equal(t, codes.Unavailable, status.Code(err))
}

func (f *installationDecisionStore) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
