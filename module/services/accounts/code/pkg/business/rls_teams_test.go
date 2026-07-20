package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// TestRLS_Teams_CrossTenantBlocked — Phase 2C, direct-org-id table.
// Two orgs each create a team. Reads from inside org A's tx must
// see only A's teams; queries asking for B's teams from A's tx
// must return nothing.
func TestRLS_Teams_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	ownerA, orgA := mustUserAndOrg(t, ctx, "alice-team@rls-test.com", "alice-team-rls", "Acme Team A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-team@rls-test.com", "bob-team-rls", "Acme Team B")

	// Each org seeds one team via the Service path (WithOrgTx-wrapped).
	_, err := testService.CreateTeam(ctx, ownerA, &gen.CreateTeamRequest{
		OrgId: orgA, Name: "engineering",
	})
	require.NoError(t, err)
	_, err = testService.CreateTeam(ctx, ownerA, &gen.CreateTeamRequest{
		OrgId: orgB, Name: "engineering-b",
	})
	require.NoError(t, err)

	// As A: see only A's team.
	listA, err := testService.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: orgA})
	require.NoError(t, err)
	require.Len(t, listA.Teams, 1)
	require.Equal(t, "engineering", listA.Teams[0].Name)

	// As B: see only B's team.
	listB, err := testService.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: orgB})
	require.NoError(t, err)
	require.Len(t, listB.Teams, 1)
	require.Equal(t, "engineering-b", listB.Teams[0].Name)

	// Probe: from A's tx, ask for B's teams via the Store directly.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.ListTeams(ctx, orgB)
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's teams from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows.
	noWrap, err := testStore.ListTeams(context.Background(), orgA)
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped ListTeams must return ZERO rows (RLS fail-closed)")
}

// TestRLS_TeamMembers_PolicyJoinsToParent — team_members has no
// direct org_id; the policy walks team_id → teams to scope. Mirrors
// the webhook_deliveries → webhook_subscriptions test in
// rls_webhooks_test.go.
func TestRLS_TeamMembers_PolicyJoinsToParent(t *testing.T) {
	clearData(t)
	ctx := testCtx

	ownerA, orgA := mustUserAndOrg(t, ctx, "alice-tm@rls-test.com", "alice-tm-rls", "Acme TM A")
	ownerB, orgB := mustUserAndOrg(t, ctx, "bob-tm@rls-test.com", "bob-tm-rls", "Acme TM B")

	// Create a team in each org, add the owner as a member.
	teamA, err := testService.CreateTeam(ctx, ownerA, &gen.CreateTeamRequest{
		OrgId: orgA, Name: "team-a",
	})
	require.NoError(t, err)
	teamB, err := testService.CreateTeam(ctx, ownerB, &gen.CreateTeamRequest{
		OrgId: orgB, Name: "team-b",
	})
	require.NoError(t, err)

	require.NoError(t, testService.AddTeamMember(ctx, ownerA, &gen.AddTeamMemberRequest{
		TeamId: teamA.Team.Id, UserId: ownerA, Role: gen.TeamRole_TEAM_ROLE_OWNER,
	}))
	require.NoError(t, testService.AddTeamMember(ctx, ownerB, &gen.AddTeamMemberRequest{
		TeamId: teamB.Team.Id, UserId: ownerB, Role: gen.TeamRole_TEAM_ROLE_OWNER,
	}))

	// As A: ListTeamMembers for A's team returns A's row.
	listA, err := testService.ListTeamMembers(ctx, &gen.ListTeamMembersRequest{
		TeamId: teamA.Team.Id,
	})
	require.NoError(t, err)
	require.Len(t, listA.Members, 1)
	require.Equal(t, ownerA, listA.Members[0].UserId)

	// Cross-tenant probe via Store: from A's tx, query B's team's
	// members. RLS JOIN policy must hide them.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.ListTeamMembers(ctx, teamB.Team.Id)
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS JOIN policy must hide B's team_members from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows.
	noWrap, err := testStore.ListTeamMembers(context.Background(), teamA.Team.Id)
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped ListTeamMembers must return ZERO rows (RLS fail-closed)")
}

// mustUserAndOrg registers a user, creates an org owned by them, and
// returns (userID, orgID).
func mustUserAndOrg(t *testing.T, ctx context.Context, email, providerID, orgName string) (string, string) {
	t.Helper()
	resp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: email,
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: providerID, ProviderEmail: email,
		},
	})
	require.NoError(t, err)
	org, err := testService.CreateOrganization(ctx, resp.User.Uuid, &gen.CreateOrganizationRequest{
		Name: orgName, Slug: providerID + "-org",
	})
	require.NoError(t, err)
	return resp.User.Uuid, org.Organization.Id
}
