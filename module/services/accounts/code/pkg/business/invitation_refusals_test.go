//go:build !pure

package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// A second invitation to an address that already holds a pending one is
// refused by name, not as the store's constraint: the person who lost the first
// link is told where it is.
func TestCreateInvitationRefusesADuplicatePendingInvitation(t *testing.T) {
	clearData(t)
	ownerID, orgID := mustUserAndOrg(t, testCtx, "dup-owner@test.invalid", "dup-owner", "Duplicate Invite Org")
	invite := func() error {
		_, err := testService.CreateInvitation(testCtx, ownerID, &gen.CreateInvitationRequest{
			OrgId: orgID, Email: "Jane.Doe@test.invalid", Role: gen.InvitationRole_INVITATION_ROLE_MEMBER,
		})
		return err
	}
	require.NoError(t, invite())
	err := invite()
	require.ErrorIs(t, err, business.ErrInvitationAlreadyPending)
	require.NotContains(t, err.Error(), "idx_invitations_pending_unique")
}

// Inviting someone who already belongs to the organization is refused: an
// invitation is not a way to change a member's role.
func TestCreateInvitationRefusesAnExistingMember(t *testing.T) {
	clearData(t)
	ownerID, orgID := mustUserAndOrg(t, testCtx, "member-owner@test.invalid", "member-owner", "Existing Member Org")
	_, err := testService.CreateInvitation(testCtx, ownerID, &gen.CreateInvitationRequest{
		OrgId: orgID, Email: "member-owner@test.invalid", Role: gen.InvitationRole_INVITATION_ROLE_MEMBER,
	})
	require.ErrorIs(t, err, business.ErrAlreadyMember)
}

// Redeeming an invitation never rewrites the role of someone who became a
// member after it was sent: accepting is refused and the role stays.
func TestAcceptInvitationKeepsAnExistingMembersRole(t *testing.T) {
	clearData(t)
	ownerID, orgID := mustUserAndOrg(t, testCtx, "keep-owner@test.invalid", "keep-owner", "Keep Role Org")
	inviteeEmail := "keep-invitee@test.invalid"
	registered, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: inviteeEmail,
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "keep-invitee", ProviderEmail: inviteeEmail, EmailVerified: true,
		},
	})
	require.NoError(t, err)
	inviteeID := registered.User.Uuid

	created, err := testService.CreateInvitation(testCtx, ownerID, &gen.CreateInvitationRequest{
		OrgId: orgID, Email: inviteeEmail, Role: gen.InvitationRole_INVITATION_ROLE_MEMBER,
	})
	require.NoError(t, err)
	require.NoError(t, testService.AddOrgMember(testCtx, ownerID, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: inviteeID, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))

	_, err = testService.AcceptInvitation(testCtx, inviteeID, &gen.AcceptInvitationRequest{
		Credential: &gen.AcceptInvitationRequest_InvitationId{InvitationId: created.GetInvitation().GetId()},
	})
	require.ErrorIs(t, err, business.ErrAlreadyMember)

	var members []*gen.OrgMembership
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		members, err = testStore.ListOrgMembers(ctx, orgID)
		return err
	}))
	roles := map[string]gen.OrgRole{}
	for _, member := range members {
		roles[member.GetUserId()] = member.GetRole()
	}
	require.Equal(t, gen.OrgRole_ORG_ROLE_ADMIN, roles[inviteeID])
}

// Acceptance takes the organization's administration lock and re-reads the
// organization under it, so a pending invitation into an organization that has
// been archived — by a deletion it waited behind, or one that left the
// invitation pending — admits nobody.
func TestAcceptInvitationIntoAnArchivedOrganizationIsRefused(t *testing.T) {
	clearData(t)
	ownerID, orgID := mustUserAndOrg(t, testCtx, "archived-owner@test.invalid", "archived-owner", "Archived Org")
	inviteeEmail := "archived-invitee@test.invalid"
	registered, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: inviteeEmail,
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "archived-invitee", ProviderEmail: inviteeEmail, EmailVerified: true,
		},
	})
	require.NoError(t, err)
	inviteeID := registered.User.Uuid

	created, err := testService.CreateInvitation(testCtx, ownerID, &gen.CreateInvitationRequest{
		OrgId: orgID, Email: inviteeEmail, Role: gen.InvitationRole_INVITATION_ROLE_MEMBER,
	})
	require.NoError(t, err)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.ArchiveOrganization(ctx, orgID, ownerID)
	}))

	_, err = testService.AcceptInvitation(testCtx, inviteeID, &gen.AcceptInvitationRequest{
		Credential: &gen.AcceptInvitationRequest_InvitationId{InvitationId: created.GetInvitation().GetId()},
	})
	require.ErrorIs(t, err, business.ErrInvitationUnavailable)

	var members []*gen.OrgMembership
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		members, err = testStore.ListOrgMembers(ctx, orgID)
		return err
	}))
	for _, member := range members {
		require.NotEqual(t, inviteeID, member.GetUserId(), "no membership may land in an archived organization")
	}
}
