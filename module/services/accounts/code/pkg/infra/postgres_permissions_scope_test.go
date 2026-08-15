package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// seedScopedRole creates a role granting resource:action and assigns it to the
// subject at the given scope ("" for an unscoped/org-wide grant).
func seedScopedRole(t *testing.T, orgID, subjectID string, kind gen.SubjectKind, resource, action, scope string) {
	t.Helper()
	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateRole(ctx, &gen.Role{
			Id:          roleID,
			Name:        "scope role " + roleID,
			Description: "scope semantics fixture",
			OrgId:       orgID,
			Permissions: []*gen.Permission{{Resource: resource, Action: action}},
		}); err != nil {
			return err
		}
		return testStore.AssignRole(ctx, &gen.RoleAssignment{
			Id:          business.NewIDString(),
			SubjectId:   subjectID,
			SubjectKind: kind,
			RoleId:      roleID,
			OrgId:       orgID,
			Scope:       scope,
		})
	}))
}

func checkScope(t *testing.T, orgID, principalID, resource, wantScope string) bool {
	t.Helper()
	var allowed bool
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		allowed, _, err = testStore.CheckPermission(
			ctx, principalID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			resource, "read", orgID, wantScope,
		)
		return err
	}))
	return allowed
}

// A scope-scoped grant must not satisfy an unscoped check, and must satisfy
// only a check for the same scope.
func TestCheckPermissionScopedGrantIsStrict(t *testing.T) {
	principalID := seedUser(t)
	orgID := seedOrg(t, principalID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, principalID, "owner",
	))
	seedScopedRole(t, orgID, principalID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "reports", "read", "module-a")

	require.False(t, checkScope(t, orgID, principalID, "reports", ""),
		"module-a-scoped grant must not satisfy an unscoped check")
	require.True(t, checkScope(t, orgID, principalID, "reports", "module-a"),
		"module-a-scoped grant must satisfy a module-a check")
	require.False(t, checkScope(t, orgID, principalID, "reports", "module-b"),
		"module-a-scoped grant must not satisfy a module-b check")
}

// A NULL-scope (org-wide) grant subsumes all scopes: it satisfies both the
// unscoped check and any scoped check.
func TestCheckPermissionUnscopedGrantSubsumesAllScopes(t *testing.T) {
	principalID := seedUser(t)
	orgID := seedOrg(t, principalID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, principalID, "owner",
	))
	seedScopedRole(t, orgID, principalID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "reports", "read", "")

	require.True(t, checkScope(t, orgID, principalID, "reports", ""),
		"org-wide grant must satisfy an unscoped check")
	require.True(t, checkScope(t, orgID, principalID, "reports", "module-a"),
		"org-wide grant must subsume a module-a check")
}

// Team-inherited assignments follow the same strict semantics as direct ones.
func TestCheckPermissionTeamInheritedScopeIsStrict(t *testing.T) {
	principalID := seedUser(t)
	orgID := seedOrg(t, principalID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, principalID, "owner",
	))

	scopedTeam := business.NewIDString()
	unscopedTeam := business.NewIDString()
	scopedRoleID := business.NewIDString()
	unscopedRoleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateTeam(ctx, &gen.Team{
			Id: scopedTeam, OrgId: orgID, Name: "Scoped Team",
			Slug: "scoped-" + scopedTeam, Path: "scoped-" + scopedTeam,
		}); err != nil {
			return err
		}
		if err := testStore.CreateTeam(ctx, &gen.Team{
			Id: unscopedTeam, OrgId: orgID, Name: "Unscoped Team",
			Slug: "unscoped-" + unscopedTeam, Path: "unscoped-" + unscopedTeam,
		}); err != nil {
			return err
		}
		if err := testStore.AddTeamMember(ctx, scopedTeam, principalID, "member"); err != nil {
			return err
		}
		if err := testStore.AddTeamMember(ctx, unscopedTeam, principalID, "member"); err != nil {
			return err
		}
		if err := testStore.CreateRole(ctx, &gen.Role{
			Id: scopedRoleID, Name: "team scoped " + scopedRoleID, OrgId: orgID,
			Permissions: []*gen.Permission{{Resource: "scoped-res", Action: "read"}},
		}); err != nil {
			return err
		}
		if err := testStore.CreateRole(ctx, &gen.Role{
			Id: unscopedRoleID, Name: "team unscoped " + unscopedRoleID, OrgId: orgID,
			Permissions: []*gen.Permission{{Resource: "unscoped-res", Action: "read"}},
		}); err != nil {
			return err
		}
		if err := testStore.AssignRole(ctx, &gen.RoleAssignment{
			Id: business.NewIDString(), SubjectId: scopedTeam,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
			RoleId:      scopedRoleID, OrgId: orgID, Scope: "module-a",
		}); err != nil {
			return err
		}
		return testStore.AssignRole(ctx, &gen.RoleAssignment{
			Id: business.NewIDString(), SubjectId: unscopedTeam,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
			RoleId:      unscopedRoleID, OrgId: orgID,
		})
	}))

	// Scoped team grant strict on the inherited path.
	require.False(t, checkScope(t, orgID, principalID, "scoped-res", ""),
		"module-a-scoped team grant must not satisfy an unscoped check")
	require.True(t, checkScope(t, orgID, principalID, "scoped-res", "module-a"),
		"module-a-scoped team grant must satisfy a module-a check")
	require.False(t, checkScope(t, orgID, principalID, "scoped-res", "module-b"),
		"module-a-scoped team grant must not satisfy a module-b check")

	// Unscoped team grant: subsumes all scopes on the inherited path.
	require.True(t, checkScope(t, orgID, principalID, "unscoped-res", ""),
		"org-wide team grant must satisfy an unscoped check")
	require.True(t, checkScope(t, orgID, principalID, "unscoped-res", "module-a"),
		"org-wide team grant must subsume a module-a check")
}
