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
