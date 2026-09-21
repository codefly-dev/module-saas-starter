//go:build !pure

package business_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/adapters"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func teamNames(teams []*gen.Team) []string {
	names := make([]string, 0, len(teams))
	for _, team := range teams {
		names = append(names, team.Name)
	}
	return names
}

// "Which teams is this person in" is the question a user detail page opens
// with, and without the filter the only answer is to walk every team's member
// list — which needs the org's whole team tree to answer one user's question.
func TestListTeams_FiltersToAMembersTeams(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx, "owner@team-filter.test", "owner-team-filter", "Acme Team Filter")
	member := mustMember(t, owner, orgID, "member@team-filter.test", "member-team-filter")

	joined, err := testService.CreateTeam(ctx, owner, &gen.CreateTeamRequest{OrgId: orgID, Name: "platform"})
	require.NoError(t, err)
	_, err = testService.CreateTeam(ctx, owner, &gen.CreateTeamRequest{OrgId: orgID, Name: "support"})
	require.NoError(t, err)

	require.NoError(t, testService.AddTeamMember(ctx, owner, &gen.AddTeamMemberRequest{
		TeamId: joined.Team.Id, UserId: member, Role: gen.TeamRole_TEAM_ROLE_MEMBER,
	}))

	all, err := testService.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: orgID})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"platform", "support"}, teamNames(all.Teams))

	mine, err := testService.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: orgID, MemberId: member})
	require.NoError(t, err)
	require.Equal(t, []string{"platform"}, teamNames(mine.Teams))

	none, err := testService.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: orgID, MemberId: owner})
	require.NoError(t, err)
	require.Empty(t, none.Teams, "creating a team does not join it")
}

// The filter narrows within the org's own teams; it never widens the read past
// the tenant boundary the unfiltered list already holds.
func TestListTeams_MemberFilterStaysInsideTheOrg(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "a@team-filter-scope.test", "a-team-filter-scope", "Acme Filter A")
	ownerB, orgB := mustUserAndOrg(t, ctx, "b@team-filter-scope.test", "b-team-filter-scope", "Acme Filter B")

	teamB, err := testService.CreateTeam(ctx, ownerB, &gen.CreateTeamRequest{OrgId: orgB, Name: "b-team"})
	require.NoError(t, err)
	require.NoError(t, testService.AddTeamMember(ctx, ownerB, &gen.AddTeamMemberRequest{
		TeamId: teamB.Team.Id, UserId: ownerB, Role: gen.TeamRole_TEAM_ROLE_OWNER,
	}))

	fromA, err := testService.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: orgA, MemberId: ownerB})
	require.NoError(t, err)
	require.Empty(t, fromA.Teams, "B's membership must not surface through A's org-scoped list")
}

// member_id reaches the query as a uuid cast, so a malformed value has to be
// refused at the boundary; without the rule it fails in the database and
// surfaces as an internal error.
func TestListTeams_RefusesAMalformedMemberFilter(t *testing.T) {
	clearData(t)
	ctx := testCtx

	owner, orgID := mustUserAndOrg(t, ctx, "owner@team-filter-bad.test", "owner-team-filter-bad", "Acme Filter Bad")
	_, err := testService.CreateTeam(ctx, owner, &gen.CreateTeamRequest{OrgId: orgID, Name: "platform"})
	require.NoError(t, err)

	_, err = (&adapters.TeamServer{}).ListTeams(ctx, &gen.ListTeamsRequest{
		OrgId: orgID, MemberId: "not-a-uuid",
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
