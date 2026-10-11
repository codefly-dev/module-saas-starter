//go:build pure

package business

import (
	"context"
	"sync"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestInstallationArtifactPolicyAtEveryDecision(t *testing.T) {
	tenant, target := uuid.NewString(), uuid.NewString()
	caller := ModuleCaller{PrincipalID: ModulePrincipalID("example"), BoundOrg: tenant}
	store := &artifactDecisionStore{rows: map[string]*ExecutableArtifactApproval{}, member: true, admin: true, permission: true,
		installation: &gen.Installation{Status: gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, TargetId: target},
		targets:      map[string]*SolutionTarget{target: {ID: target, BindingID: artifactTestBindingID}}}
	s, err := NewService(store)
	require.NoError(t, err)
	grant := ModulePrincipalGrant{Prefix: "example", Tenant: tenant, PresenceBinding: artifactTestBindingID,
		ArtifactPolicies: map[string]ExecutableArtifactPolicy{"definitions": artifactTestPolicy()}}
	s.SetModulePrincipals(ModulePrincipalRegistry{caller.PrincipalID: grant})
	live := &installationDecisionStore{rows: map[string]*LiveModuleAuthority{tenant + "/" + caller.PrincipalID: {
		InstallationID: uuid.NewString(), InstallationRevision: 1, RegistryBindingID: artifactTestBindingID, Scope: FullInstallationScope(grant)}}}
	s.SetModuleAuthorityReads(live, nil)
	req := artifactTestRequest()
	for _, action := range []string{"approve", "authorize"} {
		_, err = s.DecideExecutableArtifact(context.Background(), caller, tenant, uuid.NewString(), "example", action, req)
		require.NoError(t, err)
	}
	// HOST-INST-002, HOST-INST-004: even retained approvals are unavailable.
	for _, state := range []gen.InstallationStatus{gen.InstallationStatus_INSTALLATION_STATUS_PENDING, gen.InstallationStatus_INSTALLATION_STATUS_FAILED} {
		store.installation.Status = state
		for _, action := range []string{"approve", "authorize"} {
			_, err = s.DecideExecutableArtifact(context.Background(), caller, tenant, uuid.NewString(), "example", action, req)
			require.Error(t, err)
		}
	}
	store.installation.Status = gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE
	live.rows[tenant+"/"+caller.PrincipalID].Scope.ArtifactPolicies = nil
	live.rows[tenant+"/"+caller.PrincipalID].InstallationRevision++
	for _, action := range []string{"approve", "authorize"} {
		_, err = s.DecideExecutableArtifact(context.Background(), caller, tenant, uuid.NewString(), "example", action, req)
		require.ErrorContains(t, err, "installed artifact policy required")
	}
	live.rows = map[string]*LiveModuleAuthority{}
	for _, action := range []string{"approve", "authorize"} {
		_, err = s.DecideExecutableArtifact(context.Background(), caller, tenant, uuid.NewString(), "example", action, req)
		require.ErrorIs(t, err, ErrModuleInstallationInactive)
	}
	require.Equal(t, 1, store.puts, "retained artifact consent cannot replace module installation consent")
}

type installationUseTestKey struct{}
type fencedInstallationStore struct {
	*installationDecisionStore
	mu sync.RWMutex
}

func (f *fencedInstallationStore) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	if ctx.Value(installationUseTestKey{}) == f {
		return ctx, func() {}, nil
	}
	f.mu.RLock()
	return context.WithValue(ctx, installationUseTestKey{}, f), f.mu.RUnlock, nil
}

type fencedPlacementStore struct {
	noopControlPlaneStore
	beforeWrite func()
	written     bool
}

func (s *fencedPlacementStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	s.beforeWrite()
	return fn(ctx)
}
func (s *fencedPlacementStore) PlaceRecordNode(_ context.Context, n *gen.ScopeNode) (*gen.ScopeNode, error) {
	s.written = true
	return n, nil
}

func TestInstallationFenceHoldsThroughPlacement(t *testing.T) {
	s, live, caller := dynamicInstallationFixture()
	fenced := &fencedInstallationStore{installationDecisionStore: live}
	s.SetModuleAuthorityReads(fenced, nil)
	store := &fencedPlacementStore{}
	attempted, committed := make(chan struct{}), make(chan struct{})
	store.beforeWrite = func() {
		// Invert the review's deterministic interleaving at the actual effect.
		if fenced.mu.TryLock() {
			fenced.mu.Unlock()
			t.Fatal("installation lock released before placement")
		}
		go func() {
			close(attempted)
			fenced.mu.Lock()
			defer fenced.mu.Unlock()
			live.rows["org-a/principal"].Scope.Resources = nil
			live.rows["org-a/principal"].InstallationRevision++
			close(committed)
		}()
		<-attempted
		select {
		case <-committed:
			t.Fatal("narrowing interleaved before placement committed")
		default:
		}
	}
	s.store = store
	_, err := s.ModulePlaceRecord(context.Background(), caller, caller.BoundOrg, "space.example", "record", "Example", "records", "one")
	require.NoError(t, err)
	require.True(t, store.written)
	<-committed
	store.written = false
	_, err = s.ModulePlaceRecord(context.Background(), caller, caller.BoundOrg, "space.example", "record", "Example", "records", "two")
	require.Error(t, err)
	require.False(t, store.written)
}

func TestSharedInstallationMintSelectsOnlyConsentingOrganization(t *testing.T) {
	s, live, caller := dynamicInstallationFixture()
	grant := s.ModulePrincipals()[caller.PrincipalID]
	grant.Workload = ModuleWorkload{Namespace: "example", ServiceAccount: "example", Container: "app"}
	binding := grant.OperationAudiences["operate"]
	binding.HeadlessScopes = cloneOperationScopes(binding.InvokeScopes)
	grant.OperationAudiences["operate"] = binding
	live.rows["org-a/principal"].Scope = FullInstallationScope(grant)
	readonly := live.rows["org-b/principal"].Scope.OperationAudiences["operate"]
	readonly.HeadlessScopes = cloneOperationScopes(readonly.InvokeScopes)
	live.rows["org-b/principal"].Scope.OperationAudiences["operate"] = readonly
	id := ModulePrincipalID(grant.Prefix)
	orgA, orgB, orgC := uuid.NewString(), uuid.NewString(), uuid.NewString()
	live.rows[orgA+"/"+id] = live.rows["org-a/principal"]
	live.rows[orgB+"/"+id] = live.rows["org-b/principal"]
	grant.Tenant = ""
	s.SetModulePrincipals(ModulePrincipalRegistry{id: grant})
	token := bindTestModule(s, grant.Prefix)
	for _, org := range []string{orgA, orgB} {
		authority, err := s.ModuleAuthorizeWorkContext(context.Background(), grant.Prefix, token, org)
		require.NoError(t, err)
		require.Equal(t, org, authority.Tenant)
		require.Equal(t, live.rows[org+"/"+id].InstallationID, authority.InstallationID)
		operation, err := s.ModuleAuthorizeOperationContext(context.Background(), grant.Prefix, token, "operate", org)
		require.NoError(t, err)
		require.Equal(t, org, operation.Tenant)
		require.Equal(t, live.rows[org+"/"+id].Scope.OperationAudiences["operate"].HeadlessScopes, operation.Scopes)
	}
	_, err := s.ModuleAuthorizeWorkContext(context.Background(), grant.Prefix, token, orgC)
	require.ErrorIs(t, err, ErrModuleInstallationInactive)
	_, err = s.ModuleAuthorizeWorkContext(context.Background(), grant.Prefix, token)
	require.ErrorIs(t, err, ErrModuleTenantNotCurrent)
	grant.CrossTenant = false
	s.SetModulePrincipals(ModulePrincipalRegistry{id: grant})
	_, err = s.ModuleAuthorizeWorkContext(context.Background(), grant.Prefix, token, orgA)
	require.ErrorIs(t, err, ErrModuleTenantNotCurrent)
	grant.CrossTenant = true
	grant.Tenant = orgA
	s.SetModulePrincipals(ModulePrincipalRegistry{id: grant})
	_, err = s.ModuleAuthorizeWorkContext(context.Background(), grant.Prefix, token, orgB)
	require.ErrorIs(t, err, ErrModuleTenantNotCurrent)
	for _, org := range []string{"", orgA} {
		authority, err := s.ModuleAuthorizeWorkContext(context.Background(), grant.Prefix, token, org)
		require.NoError(t, err)
		require.Equal(t, orgA, authority.Tenant)
	}
}
