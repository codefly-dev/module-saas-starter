//go:build !pure

package business_test

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// installCapabilityFixture records explicit org consent through the production
// store API. Reused services keep the original installation and its scope.
func installCapabilityFixture(t *testing.T, svc *business.Service, org, owner string) {
	t.Helper()
	registry := svc.ModulePrincipals()
	var envelope uint64
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		envelope, err = testStore.LatestModuleEnvelopeRevision(ctx)
		return err
	}))
	for principal, grant := range registry {
		if grant.Tenant != "" && grant.Tenant != org && !grant.CrossTenant {
			continue
		}
		live, err := testStore.LiveModuleAuthority(testCtx, principal, org, "")
		if err == nil {
			grant.PresenceBinding = live.RegistryBindingID
			registry[principal] = grant
			continue
		}
		require.ErrorIs(t, err, business.ErrModuleInstallationInactive)
		alias := uniqueAlias("capability")
		if grant.PresenceBinding == "" {
			grant.PresenceBinding = "acme.test." + alias
		}
		var target string
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			targets, err := testStore.ListSolutionTargets(ctx)
			for _, candidate := range targets {
				if candidate.BindingID == grant.PresenceBinding && candidate.Live() {
					target = candidate.ID
				}
			}
			return err
		}))
		if target == "" {
			target = declareCapabilityPresence(t, grant.PresenceBinding, alias)
		}
		role := business.NewIDString()
		scope := business.FullInstallationScope(grant)
		var installed *gen.Installation
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			if err := testStore.CreateRole(ctx, &gen.Role{Id: role, OrgId: org, Name: "Example role " + role, Permissions: []*gen.Permission{{Resource: "solution", Action: "use"}}}); err != nil {
				return err
			}
			var err error
			installed, err = testStore.InstallSolution(ctx, &business.InstallSolutionParams{
				OrgID: org, OwnerPrincipalID: owner, GrantedBy: owner, RoleID: role,
				AgentIdentifier: "acme.example/" + alias + ":1.0.0", TargetID: target, Kind: "solution",
				RegistryBindingID: grant.PresenceBinding, RegistryPrincipalID: principal, Scope: &scope, EnvelopeRevision: envelope,
			})
			return err
		}))
		acknowledgeInstallation(t, installed)
		live, err = testStore.LiveModuleAuthority(testCtx, principal, org, "")
		require.NoError(t, err)
		require.Equal(t, grant.PresenceBinding, live.RegistryBindingID)
		require.Equal(t, scope, live.Scope)
		require.Equal(t, uint64(1), live.InstallationRevision)
		require.Equal(t, envelope, *live.EnvelopeRevision)
		registry[principal] = grant
	}
	svc.InstallDerivedModulePrincipals(registry, envelope)
}
