//go:build !pure

package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// visibilityTree is one org whose team tree is
//
//	field
//	├── field/east
//	│   └── field/east/branch
//	└── field/west
//
// with one member placed at each node, so every question the projection has to
// answer — down, up, sideways, and the root — has a distinct subject.
type visibilityTree struct {
	org        string
	chief      string // field
	supervisor string // field/east
	advisor    string // field/east/branch
	westerner  string // field/west
	unplaced   string // an org member in no team at all
	teams      map[string]string
}

func seedVisibilityTree(t *testing.T, ctx context.Context) visibilityTree {
	t.Helper()

	chief, org := mustUserAndOrg(t, ctx, "chief@example.com", "vis-chief", "Example Org")
	tree := visibilityTree{org: org, chief: chief, teams: map[string]string{}}

	for _, seat := range []struct {
		email, providerID string
		into              *string
	}{
		{"supervisor@example.com", "vis-supervisor", &tree.supervisor},
		{"advisor@example.com", "vis-advisor", &tree.advisor},
		{"westerner@example.com", "vis-westerner", &tree.westerner},
		{"unplaced@example.com", "vis-unplaced", &tree.unplaced},
	} {
		userID, _ := mustUserAndOrg(t, ctx, seat.email, seat.providerID, "Example Home Org")
		require.NoError(t, testService.AddOrgMember(ctx, chief, &gen.AddOrgMemberRequest{
			OrgId: org, UserId: userID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
		}))
		*seat.into = userID
	}

	field := mustTeam(t, ctx, chief, org, "", "Field")
	east := mustTeam(t, ctx, chief, org, field, "East")
	branch := mustTeam(t, ctx, chief, org, east, "Branch")
	west := mustTeam(t, ctx, chief, org, field, "West")
	tree.teams = map[string]string{
		"field": field, "field/east": east, "field/east/branch": branch, "field/west": west,
	}

	for teamID, userID := range map[string]string{
		field:  tree.chief,
		east:   tree.supervisor,
		branch: tree.advisor,
		west:   tree.westerner,
	} {
		require.NoError(t, testService.AddTeamMember(ctx, chief, &gen.AddTeamMemberRequest{
			TeamId: teamID, UserId: userID, Role: gen.TeamRole_TEAM_ROLE_MEMBER,
		}))
	}
	return tree
}

func mustTeam(t *testing.T, ctx context.Context, actorID, orgID, parentID, name string) string {
	t.Helper()
	created, err := testService.CreateTeam(ctx, actorID, &gen.CreateTeamRequest{
		OrgId: orgID, Name: name, ParentTeamId: parentID,
	})
	require.NoError(t, err)
	return created.Team.Id
}

// visibleSet is the viewer's whole set as a consuming module receives it.
func visibleSet(t *testing.T, ctx context.Context, svc *business.Service, caller business.ModuleCaller, tenant, viewer string) map[string]bool {
	t.Helper()
	grants, err := svc.ModuleListSubjectVisibility(ctx, caller, tenant, viewer)
	require.NoError(t, err)
	set := map[string]bool{}
	for _, grant := range grants {
		require.False(t, set[grant.VisibleSubjectID], "subject %s returned twice", grant.VisibleSubjectID)
		require.True(t, grant.ExpiresAt.IsZero(), "team membership carries no end, so its grant must be open-ended")
		set[grant.VisibleSubjectID] = true
	}
	return set
}

func newModuleVisibilityService(t *testing.T, tenant string) (*business.Service, business.ModuleCaller) {
	t.Helper()
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	principal := business.ModulePrincipalID("records")
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{
		principal: {Prefix: "records", Tenant: tenant},
	})
	return svc, business.ModuleCaller{PrincipalID: principal, BoundOrg: tenant}
}

// TestModuleListSubjectVisibility_ProjectsTheTeamSubtree is the behaviour a
// module's read predicate composes: a viewer may see the rows of the subjects at
// or below them in the tenant's team tree, and of nobody else. Visibility runs
// down the tree only — an advisor never acquires their supervisor's book by
// being under it — and a member of no team is visible to their ancestors while
// seeing nobody through a grant.
func TestModuleListSubjectVisibility_ProjectsTheTeamSubtree(t *testing.T) {
	clearData(t)
	ctx := testCtx

	tree := seedVisibilityTree(t, ctx)
	svc, caller := newModuleVisibilityService(t, tree.org)

	require.Equal(t,
		map[string]bool{tree.supervisor: true, tree.advisor: true, tree.westerner: true},
		visibleSet(t, ctx, svc, caller, tree.org, tree.chief),
		"the root sees its whole subtree")

	require.Equal(t,
		map[string]bool{tree.advisor: true},
		visibleSet(t, ctx, svc, caller, tree.org, tree.supervisor),
		"a branch sees below it, never its parent or a sibling branch")

	require.Empty(t,
		visibleSet(t, ctx, svc, caller, tree.org, tree.advisor),
		"a leaf is granted nothing")

	require.Empty(t,
		visibleSet(t, ctx, svc, caller, tree.org, tree.unplaced),
		"a member with no place in the hierarchy is granted nothing")
}

// The viewer is never in their own set: seeing one's own rows is ownership, not
// a grant from the hierarchy. Without this the set's size would depend on
// whether the viewer happens to be in a team, and a consumer counting or
// diffing it would be wrong for exactly the unplaced members.
func TestModuleListSubjectVisibility_ExcludesTheViewer(t *testing.T) {
	clearData(t)
	ctx := testCtx

	tree := seedVisibilityTree(t, ctx)
	svc, caller := newModuleVisibilityService(t, tree.org)

	for _, viewer := range []string{tree.chief, tree.supervisor, tree.advisor, tree.unplaced} {
		require.False(t, visibleSet(t, ctx, svc, caller, tree.org, viewer)[viewer],
			"viewer %s must not appear in their own set", viewer)
	}
}

// A viewer in two teams where one is an ancestor of the other joins the
// descendant's members twice before DISTINCT; the set must still name each
// subject once.
func TestModuleListSubjectVisibility_DeduplicatesOverlappingSubtrees(t *testing.T) {
	clearData(t)
	ctx := testCtx

	tree := seedVisibilityTree(t, ctx)
	svc, caller := newModuleVisibilityService(t, tree.org)

	require.NoError(t, testService.AddTeamMember(ctx, tree.chief, &gen.AddTeamMemberRequest{
		TeamId: tree.teams["field/east"], UserId: tree.chief, Role: gen.TeamRole_TEAM_ROLE_MEMBER,
	}))

	require.Equal(t,
		map[string]bool{tree.supervisor: true, tree.advisor: true, tree.westerner: true},
		visibleSet(t, ctx, svc, caller, tree.org, tree.chief),
		"overlapping subtrees must not duplicate or drop a subject")
}

// TestModuleListSubjectVisibility_IsOneConsistentSnapshot is the regression for
// the shape this surface deliberately does not have. A viewer's set used to be
// paginated, and a consumer reassembling it across pages read each page in its
// own transaction: a membership revoked between two pages still landed in the
// set, so a bulk replace reinstated an authority an administrator had withdrawn.
// One call, one transaction, so a revocation is either wholly before the read or
// wholly after it.
func TestModuleListSubjectVisibility_IsOneConsistentSnapshot(t *testing.T) {
	clearData(t)
	ctx := testCtx

	tree := seedVisibilityTree(t, ctx)
	svc, caller := newModuleVisibilityService(t, tree.org)

	before := visibleSet(t, ctx, svc, caller, tree.org, tree.chief)
	require.True(t, before[tree.supervisor])

	require.NoError(t, testService.RemoveTeamMember(ctx, tree.chief, &gen.RemoveTeamMemberRequest{
		TeamId: tree.teams["field/east"], UserId: tree.supervisor,
	}))

	after := visibleSet(t, ctx, svc, caller, tree.org, tree.chief)
	require.False(t, after[tree.supervisor],
		"a revoked membership must not survive into the next whole-set read")
	require.True(t, after[tree.advisor],
		"revoking the supervisor must not withdraw the subtree below them from the root")
}

// The viewer must belong to the tenant the caller named: a module bound to one
// tenant cannot use another tenant's subject as a probe.
func TestModuleListSubjectVisibility_RefusesAViewerOutsideTheTenant(t *testing.T) {
	clearData(t)
	ctx := testCtx

	tree := seedVisibilityTree(t, ctx)
	outsider, _ := mustUserAndOrg(t, ctx, "outsider@example.com", "vis-outsider", "Other Example Org")
	svc, caller := newModuleVisibilityService(t, tree.org)

	_, err := svc.ModuleListSubjectVisibility(ctx, caller, tree.org, outsider)
	require.Error(t, err)
}
