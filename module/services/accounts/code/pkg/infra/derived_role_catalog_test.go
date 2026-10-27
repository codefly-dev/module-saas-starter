//go:build !pure

package infra_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"
)

// A role this host DERIVED from a solution's declared consumption is not a role
// anybody authored, and the role catalog importer must not treat it as one. Its
// plan UPDATES the rows it believes it manages and REMOVES the ones the catalog
// no longer declares, so a derived role mistaken for a catalog role would have
// its permission set rewritten — or be deleted, cascading away the carried
// grants that hold it — under a live grant, silently.
//
// `catalog_managed` was already taken by the importer, which is exactly why the
// provenance is its own column rather than a reuse of that flag.
func TestImportRoleCatalogLeavesADerivedRoleUntouched(t *testing.T) {
	resetCatalogRoles(t)
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	const provenance = "installation=11111111-1111-1111-1111-111111111111;audience=evidence;role=chosen"
	var derivedRoleID string
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		derivedRoleID, err = testStore.EnsureDerivedRole(ctx, business.DerivedRole{
			OrgID:       orgID,
			DerivedFrom: provenance,
			Name:        "carried module access · " + provenance,
			Description: "derived",
			Permissions: []*gen.Permission{{Resource: "evidence", Action: "read"}},
		})
		return err
	}))
	require.NotEmpty(t, derivedRoleID)

	// An import that declares a different role entirely: every catalog-managed
	// role it does not name is a removal candidate.
	catalog := parseCatalog(t, `{"version":1,"roles":[
		{"name":"catalog-test:writer","permissions":[{"resource":"docs","action":"write"}]}]}`)
	result, err := testStore.ImportRoleCatalog(testCtx, catalog, infra.ImportOptions{})
	require.NoError(t, err)
	require.True(t, result.Applied)
	for _, removal := range result.Plan.Removes {
		require.NotEqual(t, derivedRoleID, removal.RoleID, "a derived role is never a removal candidate")
	}

	// The row survives, with its provenance and its permission set exactly as
	// the derivation wrote them.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := storetx.Tx(ctx)
		var derivedFrom *string
		var name string
		var catalogManaged bool
		if err := tx.QueryRow(ctx, `
			SELECT derived_from, name, catalog_managed FROM roles WHERE id = $1`, derivedRoleID,
		).Scan(&derivedFrom, &name, &catalogManaged); err != nil {
			return err
		}
		require.NotNil(t, derivedFrom)
		require.Equal(t, provenance, *derivedFrom)
		require.Equal(t, "carried module access · "+provenance, name)
		require.False(t, catalogManaged, "a derived role is not the catalog's")
		var permissions int
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM role_permissions WHERE role_id = $1 AND resource = 'evidence' AND action = 'read'`,
			derivedRoleID).Scan(&permissions); err != nil {
			return err
		}
		require.Equal(t, 1, permissions)
		return nil
	}))
}

// Re-deriving replaces the permission set rather than accumulating it: the set
// is a function of the declaration and the chosen role, and a scope the solution
// no longer declares must stop being permitted. This is where a moved applied
// generation is reconciled — on the next grant or revoke of the cause, with no
// background pass.
func TestReDerivingARoleReplacesItsPermissionSet(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	provenance := "installation=" + business.NewIDString() + ";audience=evidence;role=chosen"

	derive := func(permissions ...*gen.Permission) string {
		var roleID string
		require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			var err error
			roleID, err = testStore.EnsureDerivedRole(ctx, business.DerivedRole{
				OrgID: orgID, DerivedFrom: provenance,
				Name: "carried module access · " + provenance, Description: "derived", Permissions: permissions,
			})
			return err
		}))
		return roleID
	}

	first := derive(
		&gen.Permission{Resource: "evidence", Action: "read"},
		&gen.Permission{Resource: "evidence", Action: "write"},
	)
	second := derive(&gen.Permission{Resource: "evidence", Action: "read"})
	require.Equal(t, first, second, "the same provenance is the same role, not a second one")

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		read, err := testStore.RolePermits(ctx, second, "evidence", "read")
		require.NoError(t, err)
		require.True(t, read)
		write, err := testStore.RolePermits(ctx, second, "evidence", "write")
		require.NoError(t, err)
		require.False(t, write, "a scope the declaration no longer carries stops being permitted")
		return nil
	}))
}

// RolePermittedScopes answers for a whole declared set in one statement, in the
// order asked, through the same match RolePermits uses — including the wildcard,
// because a role that carries '*' permits what the solution declares and the
// intersection must say so.
func TestRolePermittedScopesAnswersTheSetThroughTheSameMatch(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id: roleID, Name: "wildcard " + roleID, OrgId: orgID,
			Permissions: []*gen.Permission{{Resource: "evidence", Action: "*"}},
		})
	}))

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		permitted, err := testStore.RolePermittedScopes(ctx, roleID,
			[]string{"evidence", "evidence", "secret"},
			[]string{"read", "write", "read"})
		require.NoError(t, err)
		require.Equal(t, []bool{true, true, false}, permitted, "answered in the order asked")
		return nil
	}))
}

// chore(scout): a role whose description is NULL is listed, not a failure for
// the whole organisation.
//
// `roles.description` is nullable and the role catalog importer writes NULL for
// a role it declares without one (nilIfEmpty). ListRoles scanned it into a
// string, so after such an import this listing — the picker every role dialog
// and the grant dialog read — failed outright with "cannot scan NULL into
// *string", for every caller in every organisation. Found while adding the
// derived-role import test above, whose import creates exactly that row.
func TestListRolesListsARoleWithNoDescription(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	roleID := business.NewIDString()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := storetx.Tx(ctx)
		_, err := tx.Exec(ctx, `
			INSERT INTO roles (id, name, description, built_in, org_id)
			VALUES ($1, $2, NULL, false, $3)`, roleID, "no-description-"+roleID, orgID)
		return err
	}))

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		roles, err := testStore.ListRoles(ctx, orgID)
		require.NoError(t, err)
		for _, role := range roles {
			if role.GetId() == roleID {
				require.Empty(t, role.GetDescription())
				return nil
			}
		}
		require.Fail(t, "a role with no description must still be listed")
		return nil
	}))
}

var _ = context.Background

// A solution's role is granted only at the authority root of an installation of
// THAT solution, in the organisation it was derived for, and only to a team or
// the installation's agent principal. Installing still writes the agent's
// standing grant after recording its root relationship, and exposing to a team —
// again — still passes. Another solution's root, a node that is not a
// solution's root, and a person are each refused, naming why.
func TestASolutionRoleIsGrantedOnlyAtItsOwnSolutionRootToATeamOrItsAgent(t *testing.T) {
	orgID, userID, _ := entitlementOrg(t)
	solutionRole := func(targetID string) string {
		provenance := "org=" + orgID + ";solution=" + targetID + ";role=viewer"
		var roleID string
		require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			var err error
			roleID, err = testStore.EnsureDerivedRole(ctx, business.DerivedRole{
				OrgID: orgID, DerivedFrom: provenance, Name: "solution role · " + provenance,
				Permissions: []*gen.Permission{{Resource: "evidence", Action: "read"}},
			})
			return err
		}))
		return roleID
	}
	aliasA, aliasB := uniqueAlias("notes"), uniqueAlias("ledger")
	targetA := declarePresence(t, "acme.test."+aliasA, aliasA)
	targetB := declarePresence(t, "acme.test."+aliasB, aliasB)
	roleA, roleB := solutionRole(targetA), solutionRole(targetB)

	// Install still grants its agent the standing role at the fresh root.
	installationA := installSolutionTarget(t, orgID, userID, roleA, aliasA, targetA)
	installationB := installSolutionTarget(t, orgID, userID, roleB, aliasB, targetB)
	rootA := scopeNodePath(t, orgID, installationA.GetRootScopeNodeId())
	rootB := scopeNodePath(t, orgID, installationB.GetRootScopeNodeId())
	var standing bool
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM scope_grants WHERE org_id = $1 AND subject_kind = 'principal'
			              AND subject_id = $2 AND scope_path = $3::ltree AND role_id = $4)`,
			orgID, installationA.GetAgentPrincipalId(), rootA, roleA).Scan(&standing)
	}))
	require.True(t, standing, "the installation's agent holds the solution's role at its own root")

	// A solution-kind node no installation names, and a node of another kind.
	unattached := "unattached_" + strings.ReplaceAll(business.NewIDString(), "-", "_")
	plain := "plain_" + strings.ReplaceAll(business.NewIDString(), "-", "_")
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		for _, node := range []*gen.ScopeNode{
			{Id: business.NewIDString(), OrgId: orgID, ScopePath: unattached, Kind: "solution", Label: "unattached"},
			{Id: business.NewIDString(), OrgId: orgID, ScopePath: plain, Kind: "team", Label: "plain"},
		} {
			if err := testStore.RegisterScopeNode(ctx, node); err != nil {
				return err
			}
		}
		return nil
	}))

	teamID := seedTeamWith(t, orgID, userID)
	grant := func(subjectID string, kind gen.SubjectKind, path, roleID string) error {
		return testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			return testStore.GrantScope(ctx, &gen.ScopeGrant{Id: business.NewIDString(), OrgId: orgID,
				SubjectId: subjectID, SubjectKind: kind, ScopePath: path, RoleId: roleID})
		})
	}
	team, person := gen.SubjectKind_SUBJECT_KIND_TEAM, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL

	require.NoError(t, grant(teamID, team, rootA, roleA), "exposing A to a team at A's own root")
	require.NoError(t, grant(teamID, team, rootA, roleA), "re-exposing is idempotent")
	require.Error(t, grant(installationB.GetAgentPrincipalId(), person, rootA, roleA), "another installation's agent cannot hold A's standing role")
	require.Error(t, grant(installationA.GetAgentPrincipalId(), person, unattached, roleA), "a fresh solution-kind node is not an installation root")
	require.NoError(t, grant(installationA.GetAgentPrincipalId(), person, rootA, roleA), "A's own agent still holds its role")

	err := grant(teamID, team, rootB, roleA)
	require.Error(t, err, "A's role at B's root")
	require.Contains(t, err.Error(), "role of solution "+targetA)
	require.Contains(t, err.Error(), "is the authority root of solution "+targetB)

	err = grant(userID, person, rootA, roleA)
	require.Error(t, err, "A's role to a person directly")
	require.Contains(t, err.Error(), "a person never holds a solution role directly")

	err = grant(teamID, team, plain, roleA)
	require.Error(t, err, "a node that is not a solution's root")
	require.Contains(t, err.Error(), "is not a solution's authority root")

	err = grant(teamID, team, unattached, roleA)
	require.Error(t, err, "a solution node no installation of A names")
	require.Contains(t, err.Error(), "no installation of it has")
}
