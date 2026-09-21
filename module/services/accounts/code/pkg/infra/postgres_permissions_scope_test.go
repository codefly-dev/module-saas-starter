//go:build !pure

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

func grantingScopes(t *testing.T, orgID, principalID, resource string) []string {
	t.Helper()
	var scopes []string
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		scopes, err = testStore.ScopesGrantingPermission(
			ctx, principalID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			resource, "read", orgID,
		)
		return err
	}))
	return scopes
}

// The denial an unscoped check returns for a scope-scoped grant is
// indistinguishable from the denial for no grant at all, so the scopes have to
// be reportable beside it. Without this an administrator reads "no matching
// permission found" and concludes the subject cannot act, while the subject
// acts in module-a every day.
func TestScopesGrantingPermissionNamesWhatAnUnscopedCheckHides(t *testing.T) {
	principalID := seedUser(t)
	orgID := seedOrg(t, principalID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, principalID, "owner",
	))
	seedScopedRole(t, orgID, principalID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "reports", "read", "module-a")
	seedScopedRole(t, orgID, principalID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "reports", "read", "module-b")

	require.False(t, checkScope(t, orgID, principalID, "reports", ""),
		"the scoped grants must still not satisfy an unscoped check")
	require.Equal(t, []string{"module-a", "module-b"}, grantingScopes(t, orgID, principalID, "reports"),
		"both scopes must be reportable beside that denial")
}

// A permission the subject does not hold anywhere reports no scopes, so an
// empty list is the honest "there is nothing here" the UI can rely on.
func TestScopesGrantingPermissionIsEmptyWithoutAGrant(t *testing.T) {
	principalID := seedUser(t)
	orgID := seedOrg(t, principalID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, principalID, "owner",
	))
	seedScopedRole(t, orgID, principalID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "reports", "read", "module-a")

	require.Empty(t, grantingScopes(t, orgID, principalID, "invoices"),
		"a permission held at no scope must report no scopes")
}

// An organization-wide assignment already answers every question through the
// decision itself, so listing it as a scope would offer an administrator a
// scope that does not exist.
func TestScopesGrantingPermissionOmitsOrganizationWideGrants(t *testing.T) {
	principalID := seedUser(t)
	orgID := seedOrg(t, principalID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, principalID, "owner",
	))
	seedScopedRole(t, orgID, principalID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "reports", "read", "")

	require.True(t, checkScope(t, orgID, principalID, "reports", ""))
	require.Empty(t, grantingScopes(t, orgID, principalID, "reports"))
}

// The survey resolves a subject exactly as the decision does — a scope reached
// only through a team must be reported, or the explanation would contradict a
// check for that same scope.
func TestScopesGrantingPermissionFollowsTeamInheritance(t *testing.T) {
	principalID := seedUser(t)
	orgID := seedOrg(t, principalID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, principalID, "owner",
	))

	teamID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateTeam(ctx, &gen.Team{
			Id: teamID, OrgId: orgID, Name: "Survey Team",
			Slug: "survey-" + teamID, Path: "survey-" + teamID,
		}); err != nil {
			return err
		}
		return testStore.AddTeamMember(ctx, teamID, principalID, "member")
	}))
	seedScopedRole(t, orgID, teamID, gen.SubjectKind_SUBJECT_KIND_TEAM, "reports", "read", "module-c")

	require.True(t, checkScope(t, orgID, principalID, "reports", "module-c"),
		"the team's scoped grant must satisfy a module-c check for its member")
	require.Equal(t, []string{"module-c"}, grantingScopes(t, orgID, principalID, "reports"),
		"and the survey must name the same scope the check honoured")
}

// A role assigned with no organization is effective inside every tenant:
// role_assignments_polymorphic makes the row readable under any tenant
// transaction and the decision matches it with `ra.org_id IS NULL`. So the
// explanation an administrator reads includes it, and names the role granting
// it — which ListRoleAssignments, filtered to the organization's own rows,
// never shows. That disclosure is deliberate: the grant is real inside their
// tenant, and an administrator verifying access has to be able to see it.
func TestCheckPermissionHonoursAGloballyAssignedRole(t *testing.T) {
	principalID := seedUser(t)
	orgID := seedOrg(t, principalID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, principalID, "owner",
	))

	roleID := business.NewIDString()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		if err := testStore.CreateRole(ctx, &gen.Role{
			Id:          roleID,
			Name:        "global role " + roleID,
			Description: "assigned with no organization",
			Permissions: []*gen.Permission{{Resource: "reports", Action: "read"}},
		}); err != nil {
			return err
		}
		return testStore.AssignRole(ctx, &gen.RoleAssignment{
			Id:          business.NewIDString(),
			SubjectId:   principalID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			RoleId:      roleID,
		})
	}))

	var allowed bool
	var reason string
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		allowed, reason, err = testStore.CheckPermission(
			ctx, principalID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			"reports", "read", orgID, "",
		)
		return err
	}))
	require.True(t, allowed, "a globally assigned role must grant inside the tenant")
	require.Contains(t, reason, "global role "+roleID,
		"and the explanation must name the role the grant came from")
}
