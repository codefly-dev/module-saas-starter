//go:build !pure

package business_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"accounts/pkg/adapters"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func memberIDs(members []*gen.OrgMembership) []string {
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.UserId)
	}
	sort.Strings(ids)
	return ids
}

// DirectoryService is the read-only directory a solution uses to let a person
// pick users and teams. Against the real store, through the real handlers, a
// member reads exactly what OrganizationService and TeamService answer — the
// two are one read, so they can never disagree.
func TestDirectory_AMemberReadsTheSameDirectoryTheAdministrativeServicesServe(t *testing.T) {
	clearData(t)
	ctx := testCtx
	adapters.WithService(testService)

	owner, orgID := mustUserAndOrg(t, ctx, "owner@directory.test", "owner-directory", "Acme Directory")
	member := mustMember(t, owner, orgID, "member@directory.test", "member-directory")
	team, err := testService.CreateTeam(ctx, owner, &gen.CreateTeamRequest{OrgId: orgID, Name: "platform"})
	require.NoError(t, err)
	require.NoError(t, testService.AddTeamMember(ctx, owner, &gen.AddTeamMemberRequest{
		TeamId: team.Team.Id, UserId: member, Role: gen.TeamRole_TEAM_ROLE_MEMBER,
	}))

	// The caller is a plain member, not an administrator.
	caller := storyCallerContext(ctx, member, orgID)
	directory := &adapters.DirectoryServer{}

	members, err := directory.ListOrganizationMembers(caller, &gen.ListOrgMembersRequest{OrgId: orgID})
	require.NoError(t, err)
	require.Equal(t, sortedIDs(owner, member), memberIDs(members.Members))
	twin, err := (&adapters.OrgServer{}).ListMembers(caller, &gen.ListOrgMembersRequest{OrgId: orgID})
	require.NoError(t, err)
	require.True(t, proto.Equal(twin, members), "the directory and OrganizationService answer the same read")

	teams, err := directory.ListTeams(caller, &gen.ListTeamsRequest{OrgId: orgID})
	require.NoError(t, err)
	require.Equal(t, []string{"platform"}, teamNames(teams.Teams))
	mine, err := directory.ListTeams(caller, &gen.ListTeamsRequest{OrgId: orgID, MemberId: member})
	require.NoError(t, err)
	require.Equal(t, []string{"platform"}, teamNames(mine.Teams))

	teamMembers, err := directory.ListTeamMembers(caller, &gen.ListTeamMembersRequest{TeamId: team.Team.Id})
	require.NoError(t, err)
	require.Len(t, teamMembers.Members, 1)
	require.Equal(t, member, teamMembers.Members[0].UserId)
	teamTwin, err := (&adapters.TeamServer{}).ListMembers(caller, &gen.ListTeamMembersRequest{TeamId: team.Team.Id})
	require.NoError(t, err)
	require.True(t, proto.Equal(teamTwin, teamMembers), "the directory and TeamService answer the same read")
}

// The directory never reads across a tenant: a member of another organization
// is refused the organization's members, its teams and a team's members, with
// the same refusal the administrative services give.
func TestDirectory_RefusesACallerOutsideTheOrganization(t *testing.T) {
	clearData(t)
	ctx := testCtx
	adapters.WithService(testService)

	owner, orgID := mustUserAndOrg(t, ctx, "owner@directory-a.test", "owner-directory-a", "Acme Directory A")
	outsider, otherOrg := mustUserAndOrg(t, ctx, "owner@directory-b.test", "owner-directory-b", "Acme Directory B")
	team, err := testService.CreateTeam(ctx, owner, &gen.CreateTeamRequest{OrgId: orgID, Name: "platform"})
	require.NoError(t, err)

	caller := storyCallerContext(ctx, outsider, otherOrg)
	directory := &adapters.DirectoryServer{}

	_, err = directory.ListOrganizationMembers(caller, &gen.ListOrgMembersRequest{OrgId: orgID})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "members: %v", err)
	_, err = directory.ListTeams(caller, &gen.ListTeamsRequest{OrgId: orgID})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "teams: %v", err)

	_, err = directory.ListTeamMembers(caller, &gen.ListTeamMembersRequest{TeamId: team.Team.Id})
	require.Error(t, err)
	_, twinErr := (&adapters.TeamServer{}).ListMembers(caller, &gen.ListTeamMembersRequest{TeamId: team.Team.Id})
	require.Equal(t, status.Code(twinErr), status.Code(err), "team members: the directory refuses exactly as TeamService does")
}

func sortedIDs(ids ...string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}
