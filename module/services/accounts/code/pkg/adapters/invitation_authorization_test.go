package adapters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	invitationActorID = "019f6bf7-5b1c-730d-9687-fe6d4aff31ed"
	invitationOrgAID  = "019f6bf7-5b4b-74e5-8c17-092259bb1661"
	invitationOrgBID  = "019f6bf7-5b4b-74e5-8c17-092259bb1662"
)

type invitationAuthorizationStore struct {
	business.Store
	invitationOrgID string
	members         map[string][]*gen.OrgMembership
	revoked         bool
}

func (f *invitationAuthorizationStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *invitationAuthorizationStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *invitationAuthorizationStore) GetInvitationOrgID(context.Context, string) (string, error) {
	return f.invitationOrgID, nil
}

func (f *invitationAuthorizationStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	for _, member := range f.members[orgID] {
		if member.UserId == userID {
			return member, nil
		}
	}
	return nil, nil
}

func (f *invitationAuthorizationStore) GetPlatformRole(context.Context, string) (string, error) {
	return "", nil
}

func (f *invitationAuthorizationStore) UpdateInvitationStatus(context.Context, string, string, string) error {
	f.revoked = true
	return nil
}

func invitationActorContext(userID, orgID string) context.Context {
	return stampVerifiedIdentity(context.Background(), userID, orgID, auth.Assurance{})
}

func installInvitationAuthorizationService(t *testing.T, store business.Store) {
	t.Helper()
	previous := service
	svc, err := business.NewService(store)
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })
}

func TestRevokeInvitationRejectsForeignTenantID(t *testing.T) {
	store := &invitationAuthorizationStore{
		invitationOrgID: invitationOrgBID,
		members: map[string][]*gen.OrgMembership{
			invitationOrgBID: {{UserId: invitationActorID, Role: gen.OrgRole_ORG_ROLE_MEMBER}},
		},
	}
	installInvitationAuthorizationService(t, store)

	_, err := (&InvitationServer{}).RevokeInvitation(invitationActorContext(invitationActorID, invitationOrgAID), &gen.RevokeInvitationRequest{Id: "00000000-0000-4000-8000-000000000002"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, store.revoked)
}

func TestRevokeInvitationAllowsOwningTenantAdmin(t *testing.T) {
	store := &invitationAuthorizationStore{
		invitationOrgID: invitationOrgAID,
		members: map[string][]*gen.OrgMembership{
			invitationOrgAID: {{UserId: invitationActorID, Role: gen.OrgRole_ORG_ROLE_ADMIN}},
		},
	}
	installInvitationAuthorizationService(t, store)

	_, err := (&InvitationServer{}).RevokeInvitation(invitationActorContext(invitationActorID, invitationOrgAID), &gen.RevokeInvitationRequest{Id: "00000000-0000-4000-8000-000000000001"})
	require.NoError(t, err)
	require.True(t, store.revoked)
}
