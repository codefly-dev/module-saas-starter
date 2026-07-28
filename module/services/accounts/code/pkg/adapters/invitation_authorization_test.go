package adapters

import (
	"context"
	"testing"

	"connectrpc.com/connect"
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
	invitationOrgID  string
	members          map[string][]*gen.OrgMembership
	revoked          bool
	platformRole     string
	mfaEnrolled      bool
	invitationStatus string
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
	return f.platformRole, nil
}

func (f *invitationAuthorizationStore) WithUserTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *invitationAuthorizationStore) HasVerifiedMFA(context.Context, string) (bool, error) {
	return f.mfaEnrolled, nil
}

func (f *invitationAuthorizationStore) GetInvitationByID(context.Context, string) (*business.Invitation, error) {
	return &business.Invitation{
		ID:     "00000000-0000-4000-8000-000000000001",
		OrgID:  f.invitationOrgID,
		Status: f.invitationStatus,
	}, nil
}

func (f *invitationAuthorizationStore) UpdateInvitationStatus(context.Context, string, string, string) (bool, error) {
	f.revoked = true
	f.invitationStatus = "revoked"
	return true, nil
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
		invitationOrgID:  invitationOrgAID,
		invitationStatus: "pending",
		members: map[string][]*gen.OrgMembership{
			invitationOrgAID: {{UserId: invitationActorID, Role: gen.OrgRole_ORG_ROLE_ADMIN}},
		},
	}
	installInvitationAuthorizationService(t, store)

	_, err := (&InvitationServer{}).RevokeInvitation(invitationActorContext(invitationActorID, invitationOrgAID), &gen.RevokeInvitationRequest{Id: "00000000-0000-4000-8000-000000000001"})
	require.NoError(t, err)
	require.True(t, store.revoked)
}

func TestResendInvitationRequiresAndForwardsIdempotencyKey(t *testing.T) {
	store := &invitationAuthorizationStore{
		invitationOrgID:  invitationOrgAID,
		invitationStatus: "pending",
		members: map[string][]*gen.OrgMembership{
			invitationOrgAID: {{UserId: invitationActorID, Role: gen.OrgRole_ORG_ROLE_ADMIN}},
		},
	}
	installInvitationAuthorizationService(t, store)
	handler := &invitationConnectHandler{inner: &InvitationServer{}}

	missing := connect.NewRequest(&gen.ResendInvitationRequest{
		Id: "00000000-0000-4000-8000-000000000001",
	})
	_, err := handler.ResendInvitation(
		invitationActorContext(invitationActorID, invitationOrgAID),
		missing,
	)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	present := connect.NewRequest(&gen.ResendInvitationRequest{
		Id: "00000000-0000-4000-8000-000000000001",
	})
	present.Header().Set("Idempotency-Key", "resend-operation")
	_, err = handler.ResendInvitation(
		invitationActorContext(invitationActorID, invitationOrgAID),
		present,
	)
	require.NotEqual(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.ErrorContains(t, err, "delivery is unavailable")
}

func TestInspectInvitationByIdRequiresAuthentication(t *testing.T) {
	_, err := (&InvitationServer{}).InspectInvitationById(
		context.Background(),
		&gen.InspectInvitationByIdRequest{
			InvitationId: "00000000-0000-4000-8000-000000000001",
		},
	)

	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
