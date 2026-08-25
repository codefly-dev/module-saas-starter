package business_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// AcceptInvitation authorizes on the caller's email matching the invitation.
// That equality is only sound when the provider verified the address, so an
// unverified caller must be refused even when the address matches — otherwise a
// provider that permits unverified email claims could let a user inherit an
// organization addressed to someone else.
func TestAcceptInvitationRequiresVerifiedCallerEmail(t *testing.T) {
	clearData(t)
	ownerID, orgID := mustUserAndOrg(t, testCtx, "verify-owner@test.invalid", "verify-owner", "Verify Org")

	inviteeEmail := "verify-invitee@test.invalid"
	registered, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: inviteeEmail,
		Identity: &gen.UserIdentity{
			Provider:      "email",
			ProviderId:    "verify-invitee",
			ProviderEmail: inviteeEmail,
			EmailVerified: false,
		},
	})
	require.NoError(t, err)
	inviteeID := registered.User.Uuid

	created, err := testService.CreateInvitation(testCtx, ownerID, &gen.CreateInvitationRequest{
		OrgId: orgID,
		Email: inviteeEmail,
		Role:  gen.InvitationRole_INVITATION_ROLE_MEMBER,
	})
	require.NoError(t, err)
	invitationID := created.GetInvitation().GetId()

	acceptByID := func() error {
		_, err := testService.AcceptInvitation(testCtx, inviteeID, &gen.AcceptInvitationRequest{
			Credential: &gen.AcceptInvitationRequest_InvitationId{InvitationId: invitationID},
		})
		return err
	}

	require.ErrorIs(t, acceptByID(), business.ErrInvitationEmailUnverified,
		"an unverified caller cannot accept an invitation addressed to its email")

	// Once the caller's email is verified, the same acceptance succeeds.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `UPDATE users SET email_verified = true WHERE uuid = $1`, inviteeID)
		return err
	}))
	require.NoError(t, acceptByID())
}
