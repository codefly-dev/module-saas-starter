//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func TestModuleCurrentInstallationTenantMembershipAndRevocation(t *testing.T) {
	org, owner, _, installed, _ := installFixture(t, "doc", "read")
	foreignOrg, foreignOwner, _, foreign, _ := installFixture(t, "doc", "read")
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	caller := business.ModuleCaller{PrincipalID: business.ModulePrincipalID("example"), BoundOrg: org}
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{caller.PrincipalID: {Prefix: "example", Tenant: org}})
	ctx := auth.WithVerifiedDatabaseIdentity(testCtx, owner, org)
	out, err := svc.ModuleCurrentInstallation(ctx, caller, org, owner, "example", installed.Id)
	require.NoError(t, err)
	require.Equal(t, installed.Id, out.InstallationId)
	require.Equal(t, org, out.TenantId)
	require.Equal(t, installed.SolutionIdentifier, out.SolutionIdentifier)
	_, err = svc.ModuleCurrentInstallation(ctx, caller, org, owner, "example", foreign.Id)
	require.Error(t, err)
	_, err = svc.ModuleCurrentInstallation(auth.WithVerifiedDatabaseIdentity(testCtx, foreignOwner, foreignOrg), caller, foreignOrg, foreignOwner, "example", foreign.Id)
	require.Error(t, err)
	_, err = svc.ModuleCurrentInstallation(auth.WithVerifiedDatabaseIdentity(testCtx, foreignOwner, org), caller, org, foreignOwner, "example", installed.Id)
	require.Error(t, err)
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		_, _, e := testStore.UninstallSolution(ctx, org, installed.Id)
		return e
	}))
	_, err = svc.ModuleCurrentInstallation(ctx, caller, org, owner, "example", installed.Id)
	require.Error(t, err)
}
