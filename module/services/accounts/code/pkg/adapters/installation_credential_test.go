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

// The real signer/verifier supplies no installation seal. That cannot bypass
// current consent: every use obtains today's scope and removed effects refuse.
// This deliberately does not assert identity revocation across a later reinstall;
// sealing the mint's installation identity is the separate wire-format cutover.
func TestUnsealedModuleCredentialCannotExerciseRemovedConsent(t *testing.T) {
	installModuleWorkContextAuthority(t)
	id := business.ModulePrincipalID("example")
	minted := business.ModuleWorkContextAuthority{PrincipalID: id, Tenant: moduleWorkContextTenant, InstallationID: "installation-a", InstallationRevision: 1}
	token, _, err := WorkContextSingleton().StartModuleTask(minted)
	require.NoError(t, err)
	caller, err := WorkContextSingleton().VerifyModuleWorkContext(token.Encoded())
	require.NoError(t, err)
	require.Nil(t, caller.Sealed)
	grant := business.ModulePrincipalGrant{Prefix: "example", Tenant: moduleWorkContextTenant, PresenceBinding: "example-binding", Resources: []string{"records", "retained"}}
	svc, err := business.NewService(&recordingModuleStore{})
	require.NoError(t, err)
	svc.SetModulePrincipals(business.ModulePrincipalRegistry{id: grant})
	live := &credentialInstallationStore{row: &business.LiveModuleAuthority{InstallationID: minted.InstallationID, InstallationRevision: 1, RegistryBindingID: grant.PresenceBinding, Scope: business.FullInstallationScope(grant)}}
	svc.SetModuleAuthorityReads(live, nil)
	initial, err := svc.AuthorizeModuleCapability(context.Background(), caller)
	require.NoError(t, err)
	require.Contains(t, initial.Grant.Resources, "records")
	live.row.Scope.Resources = []string{"retained"}
	live.row.InstallationRevision++
	for _, installation := range []string{minted.InstallationID, "replacement-installation"} {
		live.row.InstallationID = installation
		current, err := svc.AuthorizeModuleCapability(context.Background(), caller)
		require.NoError(t, err)
		require.Equal(t, []string{"retained"}, current.Grant.Resources)
		_, err = svc.ModulePlaceRecord(context.Background(), caller, caller.BoundOrg, "space.example", "record", "Example", "records", "one")
		require.Error(t, err)
	}
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
		caller, err := WorkContextSingleton().VerifyModuleWorkContext(response.Token)
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
