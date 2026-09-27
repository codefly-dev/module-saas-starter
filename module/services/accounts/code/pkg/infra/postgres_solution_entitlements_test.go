//go:build !pure

package infra_test

import (
	"context"
	"strings"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// These are the acceptance cases of issue #949, at the layer that decides them.
// A viewer sees the solutions their organization installed and their teams were
// granted, and nothing else — so the tests that matter are the ones where two
// subjects in ONE organization, over ONE registered set, read different answers.

// solutionRole creates a role permitting (solution, use) — the pair a
// solution-visibility question is asked in.
func solutionRole(t *testing.T, orgID string) string {
	t.Helper()
	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id:    roleID,
			Name:  "solution user " + roleID,
			OrgId: orgID,
			Permissions: []*gen.Permission{{
				Resource: business.ResourceTypeSolution,
				Action:   business.ActionUseSolution,
			}},
		})
	}))
	return roleID
}

// installSolution installs one solution into org and returns it. The agent's own
// standing grant is written at its root node; no team grant is, which is the
// "no implicit grants" property several tests below rest on.
func installSolution(t *testing.T, orgID, ownerID, roleID, identifier string) *gen.Installation {
	t.Helper()
	var installation *gen.Installation
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		installation, err = testStore.InstallSolution(ctx, &business.InstallSolutionParams{
			OrgID:              orgID,
			AgentIdentifier:    "acme.example/" + identifier + ":1.0.0",
			SolutionIdentifier: identifier,
			RootScopeLabel:     identifier,
			RoleID:             roleID,
			OwnerPrincipalID:   ownerID,
			GrantedBy:          ownerID,
		})
		return err
	}))
	return installation
}

// seedTeamWith creates a team holding member, and returns its id.
func seedTeamWith(t *testing.T, orgID, member string) string {
	t.Helper()
	teamID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateTeam(ctx, &gen.Team{
			Id: teamID, OrgId: orgID, Name: "Team " + teamID,
			Slug: "team-" + teamID,
			// The whole id, not a prefix of it: teams.path is unique per org and a
			// UUIDv7's leading characters are a timestamp, so two teams created in
			// the same millisecond would collide on any short prefix.
			Path: "team_" + strings.ReplaceAll(teamID, "-", "_"),
		}); err != nil {
			return err
		}
		return testStore.AddTeamMember(ctx, teamID, member, "member")
	}))
	return teamID
}

// grantAtNode grants roleID to a team at an installation's authority-root node.
func grantAtNode(t *testing.T, orgID, teamID, roleID string, installation *gen.Installation) {
	t.Helper()
	path := scopeNodePath(t, orgID, installation.GetRootScopeNodeId())
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.GrantScope(ctx, &gen.ScopeGrant{
			Id: business.NewIDString(), OrgId: orgID, SubjectId: teamID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM, ScopePath: path, RoleId: roleID,
		})
	}))
}

func revokeAtNode(t *testing.T, orgID, teamID, roleID string, installation *gen.Installation) {
	t.Helper()
	path := scopeNodePath(t, orgID, installation.GetRootScopeNodeId())
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.RevokeScope(ctx, orgID, teamID,
			gen.SubjectKind_SUBJECT_KIND_TEAM, path, roleID)
	}))
}

// entitledIdentifiers is what a viewer may use, as the store answers it.
func entitledIdentifiers(t *testing.T, orgID, subjectID string) []string {
	t.Helper()
	var out []*gen.SolutionEntitlement
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		out, err = testStore.ListSolutionEntitlements(ctx, orgID, subjectID, "", 100)
		return err
	}))
	identifiers := make([]string, 0, len(out))
	for _, entitlement := range out {
		identifiers = append(identifiers, entitlement.GetSolutionIdentifier())
	}
	return identifiers
}

// An organization member with a personal org and admin standing, ready to install.
func entitlementOrg(t *testing.T) (orgID, ownerID, roleID string) {
	t.Helper()
	ownerID = seedUser(t)
	seedHumanPrincipal(t, ownerID, "Installer Admin")
	orgID = seedOrg(t, ownerID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, ownerID, "admin"))
	return orgID, ownerID, solutionRole(t, orgID)
}

// THE acceptance case: two teams in one organization, one registered set, and
// genuinely different answers. If this passes with both teams seeing everything,
// the projection is still deployment-wide.
func TestListSolutionEntitlements_TwoTeamsReadDifferentSolutions(t *testing.T) {
	orgID, ownerID, roleID := entitlementOrg(t)
	audit := installSolution(t, orgID, ownerID, roleID, "audit")
	ledger := installSolution(t, orgID, ownerID, roleID, "ledger")

	reviewer := seedUser(t)
	seedHumanPrincipal(t, reviewer, "Reviewer")
	seedOrgMember(t, orgID, reviewer)
	clerk := seedUser(t)
	seedHumanPrincipal(t, clerk, "Clerk")
	seedOrgMember(t, orgID, clerk)

	reviewers := seedTeamWith(t, orgID, reviewer)
	clerks := seedTeamWith(t, orgID, clerk)
	grantAtNode(t, orgID, reviewers, roleID, audit)
	grantAtNode(t, orgID, clerks, roleID, ledger)

	require.Equal(t, []string{"audit"}, entitledIdentifiers(t, orgID, reviewer))
	require.Equal(t, []string{"ledger"}, entitledIdentifiers(t, orgID, clerk))
}

// Installing reaches no team. The install writes a standing grant for the AGENT
// principal only, so until an admin writes a team grant the solution is invisible
// — there is no implicit or inherited admission.
func TestListSolutionEntitlements_FreshInstallReachesNoTeam(t *testing.T) {
	orgID, ownerID, roleID := entitlementOrg(t)
	installSolution(t, orgID, ownerID, roleID, "audit")

	member := seedUser(t)
	seedHumanPrincipal(t, member, "Member")
	seedOrgMember(t, orgID, member)
	seedTeamWith(t, orgID, member)

	require.Empty(t, entitledIdentifiers(t, orgID, member),
		"a newly installed solution must reach no team until a grant is written")
}

// The projection narrows when a grant is revoked, with nothing about the
// registration or the installation changing.
func TestListSolutionEntitlements_NarrowsAfterRevocation(t *testing.T) {
	orgID, ownerID, roleID := entitlementOrg(t)
	audit := installSolution(t, orgID, ownerID, roleID, "audit")
	ledger := installSolution(t, orgID, ownerID, roleID, "ledger")

	member := seedUser(t)
	seedHumanPrincipal(t, member, "Member")
	seedOrgMember(t, orgID, member)
	team := seedTeamWith(t, orgID, member)
	grantAtNode(t, orgID, team, roleID, audit)
	grantAtNode(t, orgID, team, roleID, ledger)
	require.ElementsMatch(t, []string{"audit", "ledger"}, entitledIdentifiers(t, orgID, member))

	revokeAtNode(t, orgID, team, roleID, ledger)
	require.Equal(t, []string{"audit"}, entitledIdentifiers(t, orgID, member),
		"a revoked grant must remove the solution from the projection")
}

// A role that does not permit (solution, use) reveals nothing. This is what keeps
// a grant written for some other purpose from quietly adding a menu entry.
func TestListSolutionEntitlements_UnrelatedRoleRevealsNothing(t *testing.T) {
	orgID, ownerID, roleID := entitlementOrg(t)
	audit := installSolution(t, orgID, ownerID, roleID, "audit")

	member := seedUser(t)
	seedHumanPrincipal(t, member, "Member")
	seedOrgMember(t, orgID, member)
	team := seedTeamWith(t, orgID, member)

	unrelated := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id: unrelated, Name: "doc reader " + unrelated, OrgId: orgID,
			Permissions: []*gen.Permission{{Resource: "doc", Action: "read"}},
		})
	}))
	grantAtNode(t, orgID, team, unrelated, audit)

	require.Empty(t, entitledIdentifiers(t, orgID, member))
}

// An uninstalled solution is invisible even to a team whose grant at the node
// outlives the uninstall — the node is retained for a reinstall to reuse, so the
// grant can still be there. Without the status predicate this is the case that
// leaks.
func TestListSolutionEntitlements_UninstalledIsInvisibleDespiteALingeringGrant(t *testing.T) {
	orgID, ownerID, roleID := entitlementOrg(t)
	audit := installSolution(t, orgID, ownerID, roleID, "audit")

	member := seedUser(t)
	seedHumanPrincipal(t, member, "Member")
	seedOrgMember(t, orgID, member)
	team := seedTeamWith(t, orgID, member)
	grantAtNode(t, orgID, team, roleID, audit)
	require.Equal(t, []string{"audit"}, entitledIdentifiers(t, orgID, member))

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, _, err := testStore.UninstallSolution(ctx, orgID, audit.GetId())
		return err
	}))

	require.Empty(t, entitledIdentifiers(t, orgID, member),
		"an uninstalled solution must not stay in a granted team's projection")
}

// An unhealthy installation stays entitled and reports healthy=false. It is still
// listed by the consumer — the org installed it and the viewer was granted it —
// and simply not routed as serving.
func TestListSolutionEntitlements_UnhealthyStaysEntitled(t *testing.T) {
	orgID, ownerID, roleID := entitlementOrg(t)
	audit := installSolution(t, orgID, ownerID, roleID, "audit")

	member := seedUser(t)
	seedHumanPrincipal(t, member, "Member")
	seedOrgMember(t, orgID, member)
	team := seedTeamWith(t, orgID, member)
	grantAtNode(t, orgID, team, roleID, audit)

	read := func() []*gen.SolutionEntitlement {
		t.Helper()
		var out []*gen.SolutionEntitlement
		require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
			var err error
			out, err = testStore.ListSolutionEntitlements(ctx, orgID, member, "", 100)
			return err
		}))
		return out
	}
	healthy := read()
	require.Len(t, healthy, 1)
	require.True(t, healthy[0].GetHealthy())

	// Drop the owner of record out of org admin: the installation goes
	// NO_ELIGIBLE_OWNER, which is unhealthy without being uninstalled.
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, ownerID, "member"))

	unhealthy := read()
	require.Len(t, unhealthy, 1, "an unhealthy installation stays in the projection")
	require.False(t, unhealthy[0].GetHealthy(), "and reports that it is not healthy")
}

// One organization's installations never reach another's viewer, whatever grants
// exist. The org predicate plus the RLS floor both pin it.
func TestListSolutionEntitlements_IsTenantIsolated(t *testing.T) {
	orgA, ownerA, roleA := entitlementOrg(t)
	installedA := installSolution(t, orgA, ownerA, roleA, "audit")
	memberA := seedUser(t)
	seedHumanPrincipal(t, memberA, "Member A")
	seedOrgMember(t, orgA, memberA)
	teamA := seedTeamWith(t, orgA, memberA)
	grantAtNode(t, orgA, teamA, roleA, installedA)

	orgB, ownerB, roleB := entitlementOrg(t)
	installSolution(t, orgB, ownerB, roleB, "ledger")

	require.Equal(t, []string{"audit"}, entitledIdentifiers(t, orgA, memberA))
	// Asked about org B, the SAME viewer is entitled to nothing: they hold no
	// grant there and are not a member.
	require.Empty(t, entitledIdentifiers(t, orgB, memberA))
}

// A page token this listing never issued is a validation error, not a 500 and not
// a silently empty page. `id` is a uuid column, so binding an arbitrary string
// raw would abort the transaction on a cast.
func TestListSolutionEntitlements_RejectsAForeignCursor(t *testing.T) {
	orgID, _, _ := entitlementOrg(t)
	err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, e := testStore.ListSolutionEntitlements(ctx, orgID, business.NewIDString(), "not-a-cursor", 10)
		return e
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "page_token")
}

// ============================================================================
// ListInstallations — the org-scoped listing the entitlement read is built on
// ============================================================================

func listInstallations(t *testing.T, orgID string, status gen.InstallationStatus, token string, limit int) []*gen.InstallationSummary {
	t.Helper()
	var out []*gen.InstallationSummary
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		out, err = testStore.ListInstallations(ctx, orgID, status, token, limit)
		return err
	}))
	return out
}

func TestListInstallations_ReturnsTheOrgsInstallationsWithHealth(t *testing.T) {
	orgID, ownerID, roleID := entitlementOrg(t)
	installSolution(t, orgID, ownerID, roleID, "audit")
	installSolution(t, orgID, ownerID, roleID, "ledger")

	summaries := listInstallations(t, orgID, gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED, "", 100)
	require.Len(t, summaries, 2)
	identifiers := make([]string, 0, 2)
	for _, summary := range summaries {
		identifiers = append(identifiers, summary.GetInstallation().GetSolutionIdentifier())
		require.Equal(t, gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, summary.GetHealth())
	}
	require.ElementsMatch(t, []string{"audit", "ledger"}, identifiers)
}

// UNSPECIFIED returns every status, so a caller that wants only live installs must
// ask for ACTIVE — the default must not silently narrow either.
func TestListInstallations_FiltersByStatus(t *testing.T) {
	orgID, ownerID, roleID := entitlementOrg(t)
	audit := installSolution(t, orgID, ownerID, roleID, "audit")
	installSolution(t, orgID, ownerID, roleID, "ledger")
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, _, err := testStore.UninstallSolution(ctx, orgID, audit.GetId())
		return err
	}))

	active := listInstallations(t, orgID, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, "", 100)
	require.Len(t, active, 1)
	require.Equal(t, "ledger", active[0].GetInstallation().GetSolutionIdentifier())

	revoked := listInstallations(t, orgID, gen.InstallationStatus_INSTALLATION_STATUS_REVOKED, "", 100)
	require.Len(t, revoked, 1)
	require.Equal(t, "audit", revoked[0].GetInstallation().GetSolutionIdentifier())

	require.Len(t, listInstallations(t, orgID, gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED, "", 100), 2)
}

// The keyset cursor walks every row exactly once. It is `id` rather than
// solution_identifier because only `id` is unique across statuses.
func TestListInstallations_PagesWithoutRepeatingOrSkipping(t *testing.T) {
	orgID, ownerID, roleID := entitlementOrg(t)
	for _, identifier := range []string{"one", "two", "three", "four"} {
		installSolution(t, orgID, ownerID, roleID, identifier)
	}

	seen := map[string]int{}
	token := ""
	for page := 0; page < 10; page++ {
		summaries := listInstallations(t, orgID, gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED, token, 2)
		if len(summaries) == 0 {
			break
		}
		for _, summary := range summaries {
			seen[summary.GetInstallation().GetSolutionIdentifier()]++
		}
		token = summaries[len(summaries)-1].GetInstallation().GetId()
		if len(summaries) < 2 {
			break
		}
	}
	require.Len(t, seen, 4)
	for identifier, count := range seen {
		require.Equal(t, 1, count, "%s appeared %d times", identifier, count)
	}
}

func TestListInstallations_IsTenantIsolated(t *testing.T) {
	orgA, ownerA, roleA := entitlementOrg(t)
	installSolution(t, orgA, ownerA, roleA, "audit")
	orgB, ownerB, roleB := entitlementOrg(t)
	installSolution(t, orgB, ownerB, roleB, "ledger")

	a := listInstallations(t, orgA, gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED, "", 100)
	require.Len(t, a, 1)
	require.Equal(t, "audit", a[0].GetInstallation().GetSolutionIdentifier())
}

func TestListInstallations_RejectsAForeignCursor(t *testing.T) {
	orgID, _, _ := entitlementOrg(t)
	err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, e := testStore.ListInstallations(ctx, orgID, gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED, "nope", 10)
		return e
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "page_token")
}
