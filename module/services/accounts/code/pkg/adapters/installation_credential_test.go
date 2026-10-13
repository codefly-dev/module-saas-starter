package adapters

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type credentialInstallationStore struct{ row *business.LiveModuleAuthority }

func (s *credentialInstallationStore) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (s *credentialInstallationStore) LiveModuleAuthority(context.Context, string, string, string) (*business.LiveModuleAuthority, error) {
	if s.row == nil {
		return nil, business.ErrModuleInstallationInactive
	}
	return s.row, nil
}

// A module credential is SEALED to the installation it was minted for, its
// revision and the principal's producer epoch, and every use compares the live
// installation against what the credential claims. So consent removed after the
// mint is refused twice over — by the live scope, and by the sealed revision no
// longer matching — and a reinstall under a new installation id revokes every
// credential minted for the old one, which an unsealed credential could not do.
func TestSealedModuleCredentialIsHeldToItsInstallation(t *testing.T) {
	installModuleWorkContextAuthority(t)
	id := business.ModulePrincipalID("example")
	build := testBuildFor(id)
	workContextSingleton.seals.(*sealAnswersFake).approve(id, build)
	minted := business.ModuleWorkContextAuthority{
		PrincipalID: id, Tenant: moduleWorkContextTenant, InstallationID: "installation-a", InstallationRevision: 1,
		Execution: business.ExecutionIdentity{Running: business.RunningDigest(build.ImageDigest), Incarnation: build.BuildIncarnation},
	}
	token, _, err := WorkContextSingleton().StartModuleTask(context.Background(), minted)
	require.NoError(t, err)
	caller, err := WorkContextSingleton().VerifyModuleWorkContext(context.Background(), token)
	require.NoError(t, err)
	require.NotNil(t, caller.Sealed)
	require.Equal(t, minted.InstallationID, *caller.Sealed.InstallationID)
	require.Equal(t, uint64(1), *caller.Sealed.InstallationRevision)
	require.Equal(t, uint64(1), *caller.Sealed.ProducerEpoch)

	grant := business.ModulePrincipalGrant{Prefix: "example", Tenant: moduleWorkContextTenant, PresenceBinding: "example-binding", Resources: []string{"records", "retained"}}
	svc, err := business.NewService(&recordingModuleStore{})
	require.NoError(t, err)
	svc.SetModulePrincipals(business.ModulePrincipalRegistry{id: grant})
	live := &credentialInstallationStore{row: &business.LiveModuleAuthority{
		InstallationID: minted.InstallationID, InstallationRevision: 1, ProducerEpoch: 1,
		RegistryBindingID: grant.PresenceBinding, Scope: business.FullInstallationScope(grant),
	}}
	svc.SetModuleAuthorityReads(live, nil)
	initial, err := svc.AuthorizeModuleCapability(context.Background(), caller)
	require.NoError(t, err)
	require.Contains(t, initial.Grant.Resources, "records")

	// Consent narrowed: the installation's revision moves, and the credential
	// sealed at the earlier one is refused outright.
	live.row.Scope.Resources = []string{"retained"}
	live.row.InstallationRevision++
	_, err = svc.AuthorizeModuleCapability(context.Background(), caller)
	require.ErrorIs(t, err, business.ErrModuleAuthorityStale)

	// Reinstalled under another installation id, at the sealed revision: still
	// refused, because the credential names the installation it was minted for.
	live.row.InstallationID, live.row.InstallationRevision = "replacement-installation", 1
	_, err = svc.AuthorizeModuleCapability(context.Background(), caller)
	require.ErrorIs(t, err, business.ErrModuleAuthorityStale)

	live.row = nil
	_, err = svc.ModulePlaceRecord(context.Background(), caller, caller.BoundOrg, "space.example", "record", "Example", "records", "one")
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive)
}

// The request selector must survive the RPC, mint and real token verification.
// This test joins the executable suite after regenerating module_registration.
type sharedMintInstallations struct {
	rows map[string]*business.LiveModuleAuthority
}

func (s sharedMintInstallations) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (s sharedMintInstallations) LiveModuleAuthority(_ context.Context, principal, org, _ string) (*business.LiveModuleAuthority, error) {
	if row := s.rows[org+"/"+principal]; row != nil {
		return row, nil
	}
	return nil, business.ErrModuleInstallationInactive
}
func TestSharedOrganizationSelectorSurvivesModuleMintRPC(t *testing.T) {
	installModuleWorkContextService(t, `{"example":{"cross_tenant":true,"resources":["records"],"workload":{"service_account":"example","namespace":"example-system","container":"app"}}}`)
	installModuleWorkContextAuthority(t)
	id := business.ModulePrincipalID("example")
	grant := service.ModulePrincipals()[id]
	orgA, orgB, orgC := moduleWorkContextTenant, "66666666-6666-4666-8666-666666666666", "77777777-7777-4777-8777-777777777777"
	live := sharedMintInstallations{rows: map[string]*business.LiveModuleAuthority{}}
	for _, org := range []string{orgA, orgB} {
		live.rows[org+"/"+id] = &business.LiveModuleAuthority{InstallationID: org, InstallationRevision: 1, RegistryBindingID: grant.PresenceBinding, Scope: business.FullInstallationScope(grant)}
	}
	service.SetModuleAuthorityReads(live, nil)
	projected := moduleToken(t, "example")
	for _, org := range []string{orgA, orgB} {
		response, err := ModuleCapabilitiesSingleton().MintModuleWorkContext(context.Background(), &gen.ModuleMintWorkContextRequest{Prefix: "example", ServiceAccountToken: projected, OrganizationId: org})
		require.NoError(t, err)
		require.Equal(t, org, response.Tenant)
		caller, err := WorkContextSingleton().VerifyModuleWorkContext(context.Background(), response.Token)
		require.NoError(t, err)
		require.Equal(t, org, caller.BoundOrg)
	}
	_, err := ModuleCapabilitiesSingleton().MintModuleWorkContext(context.Background(), &gen.ModuleMintWorkContextRequest{Prefix: "example", ServiceAccountToken: projected, OrganizationId: orgC})
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive)
	grant.Tenant = orgA
	service.SetModulePrincipals(business.ModulePrincipalRegistry{id: grant})
	_, err = ModuleCapabilitiesSingleton().MintModuleWorkContext(context.Background(), &gen.ModuleMintWorkContextRequest{Prefix: "example", ServiceAccountToken: projected, OrganizationId: orgB})
	require.ErrorIs(t, err, business.ErrModuleTenantNotCurrent)
}
