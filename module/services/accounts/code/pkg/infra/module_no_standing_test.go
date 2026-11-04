//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/stretchr/testify/require"
)

// This exercises the existing module installation store. Signed producer and
// browser review qualification are separate; declarations here use the typed
// store seam so the test isolates the persisted no-standing contract.
func TestModuleDeclaredNonePersistsConsentWithoutStandingGrant(t *testing.T) {
	owner := seedUser(t)
	seedHumanPrincipal(t, owner, "Jane Doe")
	org := seedOrg(t, owner)
	require.NoError(t, testStore.As(business.Identity{OrgID: org}).AddOrgMember(testCtx, owner, "admin"))
	alias := uniqueAlias("example-module")
	binding := "acme.test." + alias
	target := declarePresenceKind(t, binding, alias, business.SolutionDeclaredKindModule)
	m := domain.Module{Name: alias, Principal: alias, Kind: domain.KindModule, Generation: 1, InstallationRole: domain.NoInstallationRole}
	require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error { return tx.PutModule(m) }))
	params := &business.InstallSolutionParams{OrgID: org, TargetID: target, Kind: "module", RegistryBindingID: binding,
		RegistryPrincipalID: business.ModulePrincipalID(alias), AgentIdentifier: "acme.example/" + alias + ":1.0.0",
		OwnerPrincipalID: owner, GrantedBy: owner, NoStandingAuthority: true, Scope: &business.InstallationScope{Capabilities: true}}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		params.EnvelopeRevision, err = testStore.LatestModuleEnvelopeRevision(ctx)
		return err
	}))
	var installation *gen.Installation
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		var err error
		installation, err = testStore.InstallSolution(ctx, params)
		return err
	}))
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, installation.Status)
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		q := storetx.Tx(ctx)
		var count int
		require.NoError(t, q.QueryRow(ctx, `SELECT count(*) FROM public.scope_grants WHERE org_id=$1 AND subject_kind='principal' AND subject_id=$2`, org, installation.AgentPrincipalId).Scan(&count))
		require.Zero(t, count)
		id, role, revision, err := testStore.InstallationRoleRevision(ctx, org, installation.Id)
		require.NoError(t, err)
		require.Empty(t, id)
		require.Empty(t, role)
		require.Equal(t, installation.Revision, revision)
		_, health, err := testStore.GetInstallation(ctx, org, installation.Id)
		require.NoError(t, err)
		require.Equal(t, gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, health)
		rows, err := testStore.ListInstallations(ctx, org, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, "", 10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, health, rows[0].Health)
		return nil
	}))
	err := testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx, `UPDATE public.installations SET no_standing_authority=false WHERE id=$1`, installation.Id)
		return err
	})
	require.ErrorContains(t, err, "machine selection is immutable")
	role := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{Id: role, OrgId: org, Name: "Example role", Permissions: []*gen.Permission{{Resource: "records", Action: "read"}}})
	}))
	err = testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		path, err := testStore.InstallationRootScopePath(ctx, org, installation.Id)
		if err != nil {
			return err
		}
		return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: org, SubjectId: installation.AgentPrincipalId,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, ScopePath: path, RoleId: role, GrantedBy: owner})
	})
	require.ErrorContains(t, err, "no standing machine grant")
	params.RoleID = business.NewIDString()
	err = testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error { _, err := testStore.InstallSolution(ctx, params); return err })
	require.Error(t, err, "a role input cannot be silently discarded")
	params.RoleID = ""
	m.InstallationRole = ""
	require.NoError(t, testStore.AuthorityCore().Write(testCtx, func(tx authority.Tx) error { return tx.PutModule(m) }))
	err = testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error { _, err := testStore.InstallSolution(ctx, params); return err })
	require.ErrorContains(t, err, "does not select no standing authority")
}
