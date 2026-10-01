//go:build !pure

package business_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func orgEventCount(t *testing.T, ctx context.Context, orgID string, event business.EventType) int64 {
	t.Helper()
	buckets, err := testService.AggregateAuditLog(ctx,
		business.AuditQuery{OrgID: orgID, EventType: string(event)},
		business.AuditAggregationSpec{GroupBy: []string{"event_type"}})
	require.NoError(t, err)
	if len(buckets) == 0 {
		return 0
	}
	return buckets[0].Count
}

func listedOrgIDs(t *testing.T, ctx context.Context, userID string) map[string]bool {
	t.Helper()
	listed, err := testService.ListOrganizations(ctx, userID)
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, org := range listed.Organizations {
		ids[org.Id] = true
	}
	return ids
}

func withCreationPolicy(t *testing.T, policy business.OrganizationCreationPolicy) {
	t.Helper()
	testService.SetOrganizationCreationPolicy(policy)
	t.Cleanup(func() { testService.SetOrganizationCreationPolicy(business.OrganizationCreationOpen) })
}

func TestUpdateOrganizationRenamesAndRefusesATakenSlug(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, orgID := mustUserAndOrg(t, ctx, "rename@lifecycle.test", "lc-rename", "Before")
	_, _ = mustUserAndOrg(t, ctx, "other@lifecycle.test", "lc-other", "Other")

	org, err := testService.UpdateOrganization(ctx, owner, &gen.UpdateOrganizationRequest{
		OrgId: orgID, Name: "After", Slug: "after",
	})
	require.NoError(t, err)
	require.Equal(t, "After", org.Name)
	require.Equal(t, "after", org.Slug)
	require.EqualValues(t, 1, orgEventCount(t, ctx, orgID, business.EventOrgUpdated))

	_, err = testService.UpdateOrganization(ctx, owner, &gen.UpdateOrganizationRequest{
		OrgId: orgID, Name: "After", Slug: "lc-other-org",
	})
	var storeErr *business.StoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, business.ErrTypeConflict, storeErr.StoreErrorType)
}

func TestLeaveOrganizationRemovesOnlyTheCaller(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, orgID := mustUserAndOrg(t, ctx, "leave-owner@lifecycle.test", "lc-leave-owner", "Leave Org")
	memberID, _ := mustUserAndOrg(t, ctx, "leave-member@lifecycle.test", "lc-leave-member", "Member Home")
	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	seedTeamMemberships(t, ctx, owner, orgID, memberID, map[string]gen.TeamRole{"crew": gen.TeamRole_TEAM_ROLE_MEMBER})

	require.NoError(t, testService.LeaveOrganization(ctx, memberID, &gen.LeaveOrganizationRequest{OrgId: orgID}))

	require.False(t, isOrgMember(t, ctx, orgID, memberID))
	require.True(t, isOrgMember(t, ctx, orgID, owner))
	require.Zero(t, teamMembershipCount(t, ctx, orgID, memberID))
	require.False(t, listedOrgIDs(t, ctx, memberID)[orgID])
	require.EqualValues(t, 1, orgEventCount(t, ctx, orgID, business.EventOrgMemberLeft))
}

func TestLeaveOrganizationRefusesTheSoleMemberAndTheLastAdministrator(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, orgID := mustUserAndOrg(t, ctx, "sole@lifecycle.test", "lc-sole", "Sole Org")

	err := testService.LeaveOrganization(ctx, owner, &gen.LeaveOrganizationRequest{OrgId: orgID})
	require.ErrorIs(t, err, business.ErrOrgSoleMember)
	require.True(t, isOrgMember(t, ctx, orgID, owner))

	memberID, _ := mustUserAndOrg(t, ctx, "stays@lifecycle.test", "lc-stays", "Stays Home")
	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	err = testService.LeaveOrganization(ctx, owner, &gen.LeaveOrganizationRequest{OrgId: orgID})
	require.ErrorIs(t, err, business.ErrOrgAdminContinuity,
		"the last administrator may not leave a member stranded in an organization nobody can manage")
	require.True(t, isOrgMember(t, ctx, orgID, owner))
}

func TestDeleteOrganizationArchivesRevokesAndStaysEmpty(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, orgID := mustUserAndOrg(t, ctx, "delete-owner@lifecycle.test", "lc-delete", "Doomed")
	memberID, _ := mustUserAndOrg(t, ctx, "delete-member@lifecycle.test", "lc-delete-member", "Member Home")
	require.NoError(t, testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	_, err := testService.CreateAPIKey(ctx, owner, &gen.CreateAPIKeyRequest{
		OrganizationId: orgID, Name: "doomed-key", Environment: gen.APIKeyEnvironment_API_KEY_ENVIRONMENT_TEST,
	})
	require.NoError(t, err)
	_, err = testService.CreateInvitation(ctx, owner, &gen.CreateInvitationRequest{
		OrgId: orgID, Email: "pending@lifecycle.test", Role: gen.InvitationRole_INVITATION_ROLE_MEMBER,
	})
	require.NoError(t, err)

	err = testService.DeleteOrganization(ctx, owner, &gen.DeleteOrganizationRequest{OrgId: orgID, ConfirmSlug: "not-it"})
	require.ErrorIs(t, err, business.ErrOrgDeleteConfirmation)
	require.True(t, isOrgMember(t, ctx, orgID, owner), "a refused delete changes nothing")

	require.NoError(t, testService.DeleteOrganization(ctx, owner, &gen.DeleteOrganizationRequest{
		OrgId: orgID, ConfirmSlug: "LC-DELETE-ORG",
	}))

	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		org, err := testStore.GetOrganization(ctx, orgID)
		require.NoError(t, err)
		require.NotNil(t, org.ArchivedAt, "the row is kept and marked archived")
		require.NotEqual(t, "lc-delete-org", org.Slug, "the slug is released")
		members, err := testStore.CountOrgMembers(ctx, orgID)
		require.NoError(t, err)
		require.Zero(t, members)
		keys, err := testStore.CountActiveAPIKeys(ctx, orgID)
		require.NoError(t, err)
		require.Zero(t, keys)
		pending, err := testStore.CountPendingInvitations(ctx, orgID)
		require.NoError(t, err)
		require.Zero(t, pending)
		return nil
	}))
	require.False(t, listedOrgIDs(t, ctx, owner)[orgID])
	require.False(t, listedOrgIDs(t, ctx, memberID)[orgID])
	require.EqualValues(t, 1, orgEventCount(t, ctx, orgID, business.EventOrgDeleted))

	// Nothing brings a member back: the trigger refuses whatever path tries.
	err = testService.AddOrgMember(ctx, owner, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "is archived")

	// The slug is free for a new organization.
	_, err = testService.CreateOrganization(ctx, owner, &gen.CreateOrganizationRequest{Name: "Doomed", Slug: "lc-delete-org"})
	require.NoError(t, err)
}

func TestCreateOrganizationFollowsTheCreationPolicy(t *testing.T) {
	clearData(t)
	ctx := testCtx
	userID, _ := mustUserAndOrg(t, ctx, "plain@lifecycle.test", "lc-plain", "Plain")
	supportID, _ := mustUserAndOrg(t, ctx, "support@lifecycle.test", "lc-support", "Support")
	superID, _ := mustUserAndOrg(t, ctx, "super@lifecycle.test", "lc-super", "Super")
	grantPlatformRole(t, supportID, "support", supportID)
	grantPlatformRole(t, superID, "super_admin", superID)

	withCreationPolicy(t, business.OrganizationCreationPlatformAdmin)
	_, err := testService.CreateOrganization(ctx, userID, &gen.CreateOrganizationRequest{Name: "No", Slug: "lc-no"})
	require.ErrorIs(t, err, business.ErrOrganizationCreationRefused)
	listed, err := testService.ListOrganizations(ctx, userID)
	require.NoError(t, err)
	require.False(t, listed.CanCreate)
	_, err = testService.CreateOrganization(ctx, supportID, &gen.CreateOrganizationRequest{Name: "Yes", Slug: "lc-yes"})
	require.NoError(t, err)

	withCreationPolicy(t, business.OrganizationCreationDisabled)
	_, err = testService.CreateOrganization(ctx, supportID, &gen.CreateOrganizationRequest{Name: "No2", Slug: "lc-no2"})
	require.ErrorIs(t, err, business.ErrOrganizationCreationRefused)

	// A platform super administrator may always create, and may create for
	// someone else; nobody else may name another owner.
	_, err = testService.CreateOrganization(ctx, supportID, &gen.CreateOrganizationRequest{
		Name: "Theirs", Slug: "lc-theirs", OwnerUserId: userID,
	})
	require.ErrorIs(t, err, business.ErrOrganizationOwnerRefused)
	created, err := testService.CreateOrganization(ctx, superID, &gen.CreateOrganizationRequest{
		Name: "For Plain", Slug: "lc-for-plain", OwnerUserId: userID,
	})
	require.NoError(t, err)
	require.Equal(t, userID, created.Organization.OwnerId)
	require.True(t, isOrgMember(t, ctx, created.Organization.Id, userID))
	require.False(t, isOrgMember(t, ctx, created.Organization.Id, superID))
}

func TestPlatformOrganizationViewSeesAcrossTenantsOnlyForPlatformAdmins(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, orgID := mustUserAndOrg(t, ctx, "tenant@lifecycle.test", "lc-tenant", "Zeta Tenant")
	supportID, _ := mustUserAndOrg(t, ctx, "ops@lifecycle.test", "lc-ops", "Ops")
	grantPlatformRole(t, supportID, "support", supportID)

	_, err := testService.ListAllOrganizations(ctx, owner, &gen.ListAllOrganizationsRequest{PageSize: 50})
	require.Error(t, err)
	_, err = testService.GetOrganizationRoster(ctx, owner, &gen.GetOrganizationRosterRequest{OrgId: orgID})
	require.Error(t, err)

	page, err := testService.ListAllOrganizations(ctx, supportID, &gen.ListAllOrganizationsRequest{Query: "zeta", PageSize: 50})
	require.NoError(t, err)
	require.Len(t, page.Organizations, 1)
	require.Equal(t, orgID, page.Organizations[0].Organization.Id)
	require.EqualValues(t, 1, page.Organizations[0].MemberCount)

	roster, err := testService.GetOrganizationRoster(ctx, supportID, &gen.GetOrganizationRosterRequest{OrgId: orgID})
	require.NoError(t, err)
	require.Len(t, roster.Members, 1)
	require.Equal(t, owner, roster.Members[0].UserId)

	require.NoError(t, testService.DeleteOrganization(ctx, owner, &gen.DeleteOrganizationRequest{OrgId: orgID, ConfirmSlug: "lc-tenant-org"}))
	page, err = testService.ListAllOrganizations(ctx, supportID, &gen.ListAllOrganizationsRequest{Query: "zeta", PageSize: 50})
	require.NoError(t, err)
	require.Empty(t, page.Organizations, "archived organizations are hidden unless asked for")
	page, err = testService.ListAllOrganizations(ctx, supportID, &gen.ListAllOrganizationsRequest{Query: "zeta", PageSize: 50, IncludeArchived: true})
	require.NoError(t, err)
	require.Len(t, page.Organizations, 1)
	require.NotNil(t, page.Organizations[0].Organization.ArchivedAt)
}

func TestInvitationLinksAreHandedToTheAdministratorAndRotate(t *testing.T) {
	clearData(t)
	ctx, err := auth.WithVerifiedPublicOrigin(testCtx, "http://localhost:54321")
	require.NoError(t, err)
	owner, orgID := mustUserAndOrg(t, ctx, "linker@lifecycle.test", "lc-linker", "Link Org")

	created, err := testService.CreateInvitation(ctx, owner, &gen.CreateInvitationRequest{
		OrgId: orgID, Email: "invitee@lifecycle.test", Role: gen.InvitationRole_INVITATION_ROLE_MEMBER,
	})
	require.NoError(t, err)
	first := acceptToken(t, created.AcceptUrl)

	issued, err := testService.IssueInvitationLink(ctx, owner, &gen.IssueInvitationLinkRequest{Id: created.Invitation.Id}, orgID)
	require.NoError(t, err)
	second := acceptToken(t, issued.AcceptUrl)
	require.NotEqual(t, first, second)
	require.EqualValues(t, 1, orgEventCount(t, ctx, orgID, business.EventInvitationLinkIssued))

	_, err = testService.InspectInvitation(ctx, &gen.InspectInvitationRequest{Token: first})
	require.ErrorIs(t, err, business.ErrInvitationUnavailable, "issuing a link invalidates every earlier one")
	summary, err := testService.InspectInvitation(ctx, &gen.InspectInvitationRequest{Token: second})
	require.NoError(t, err)
	require.Equal(t, gen.InvitationStatus_INVITATION_STATUS_PENDING, summary.Status)
}

func acceptToken(t *testing.T, acceptURL string) string {
	t.Helper()
	parsed, err := url.Parse(acceptURL)
	require.NoError(t, err)
	require.Equal(t, "/invitations/accept", parsed.Path)
	token := parsed.Query().Get("token")
	require.NotEmpty(t, token)
	return token
}
