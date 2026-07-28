package adapters

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestWaitlistAdministrationRequiresPlatformRole(t *testing.T) {
	store := &invitationAuthorizationStore{}
	installInvitationAuthorizationService(t, store)

	handler := waitlistConnectHandler{svc: service}
	_, err := handler.List(
		invitationActorContext(invitationActorID, invitationOrgAID),
		connect.NewRequest(&gen.ListWaitlistRequest{PageSize: 50}),
	)

	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestWaitlistStateChangesRequireMFAForEnrolledAdministrators(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(waitlistConnectHandler) error
	}{
		{
			name: "review",
			call: func(handler waitlistConnectHandler) error {
				_, err := handler.Review(
					invitationActorContext(invitationActorID, invitationOrgAID),
					connect.NewRequest(&gen.ReviewWaitlistRequest{
						Id:    "00000000-0000-4000-8000-000000000010",
						State: gen.WaitlistState_WAITLIST_STATE_APPROVED,
					}),
				)
				return err
			},
		},
		{
			name: "invite",
			call: func(handler waitlistConnectHandler) error {
				_, err := handler.Invite(
					invitationActorContext(invitationActorID, invitationOrgAID),
					connect.NewRequest(&gen.InviteWaitlistRequest{
						Id: "00000000-0000-4000-8000-000000000010",
					}),
				)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &invitationAuthorizationStore{
				platformRole: "super_admin",
				mfaEnrolled:  true,
			}
			installInvitationAuthorizationService(t, store)

			err := tc.call(waitlistConnectHandler{svc: service})

			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		})
	}
}

func TestOnboardingRejectsForeignOrganization(t *testing.T) {
	store := &invitationAuthorizationStore{
		members: map[string][]*gen.OrgMembership{
			invitationOrgAID: {{UserId: invitationActorID, Role: gen.OrgRole_ORG_ROLE_MEMBER}},
		},
	}
	installInvitationAuthorizationService(t, store)

	handler := onboardingConnectHandler{svc: service}
	_, err := handler.GetProgress(
		invitationActorContext(invitationActorID, invitationOrgAID),
		connect.NewRequest(&gen.GetOnboardingProgressRequest{
			OrganizationId: invitationOrgBID,
		}),
	)

	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}
