package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// seedPendingInvitation writes a pending invitation directly and returns its
// plaintext token, so tests can drive the invite authentication path without a
// wired email outbox to surface the delivered credential.
func seedPendingInvitation(t *testing.T, orgID, inviterID, email, role string, expiresAt time.Time) string {
	t.Helper()
	token := business.NewIDString()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `
			INSERT INTO invitations (id, org_id, inviter_id, email, role, token_hash, status, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7)`,
			business.NewIDString(), orgID, inviterID, email, role,
			business.HashInvitationToken(token), expiresAt)
		return err
	}))
	return token
}

// TestAuthenticate_Invite_AcceptsAndNotifiesInviter proves the invite
// authentication path both provisions/joins the invitee and fires the business
// side effects that AcceptInvitation would — here, the inviter notification.
func TestAuthenticate_Invite_AcceptsAndNotifiesInviter(t *testing.T) {
	clearData(t)
	inviterID, orgID := mustUserAndOrg(t, testCtx, "inviter@test.invalid", "inviter-sub", "Inviter Org")
	token := seedPendingInvitation(t, orgID, inviterID, "invitee@test.invalid", "admin", time.Now().Add(24*time.Hour))

	resp, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "invitee-sub",
		ProviderEmail: "invitee@test.invalid",
		Profile:       map[string]string{"invitation_token": token},
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.User.GetUuid())

	// The invitee joined the inviting org via the accepted invitation.
	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   "email",
		ProviderId: "invitee-sub",
	})
	require.NoError(t, err)
	require.True(t, resolved.Found)
	require.Equal(t, orgID, resolved.OrgId)

	// The inviter is notified, exactly as AcceptInvitation would have done.
	notifs, _, err := testService.ListNotifications(testCtx, inviterID, 50, "")
	require.NoError(t, err)
	var notified bool
	for _, n := range notifs {
		if n.Title == "Invitation accepted" {
			notified = true
		}
	}
	require.True(t, notified, "inviter must be notified when the invite is accepted through authentication")
}

// TestAuthenticate_Invite_ExpiredMarksInvitationExpired proves an expired invite
// fails closed AND is transitioned out of the pending state, so a stale pending
// row cannot linger and block a fresh invitation to the same address.
func TestAuthenticate_Invite_ExpiredMarksInvitationExpired(t *testing.T) {
	clearData(t)
	inviterID, orgID := mustUserAndOrg(t, testCtx, "inviter2@test.invalid", "inviter2-sub", "Inviter Org Two")
	token := seedPendingInvitation(t, orgID, inviterID, "late@test.invalid", "member", time.Now().Add(-time.Hour))

	_, err := authenticateFixture(testCtx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "late-sub",
		ProviderEmail: "late@test.invalid",
		Profile:       map[string]string{"invitation_token": token},
	})
	require.Error(t, err)

	var status string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		return tx.QueryRow(ctx,
			`SELECT status FROM invitations WHERE token_hash = $1`,
			business.HashInvitationToken(token)).Scan(&status)
	}))
	require.Equal(t, "expired", status, "an expired invite must be transitioned out of pending")

	// Nothing was provisioned for the would-be invitee.
	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   "email",
		ProviderId: "late-sub",
	})
	require.NoError(t, err)
	require.False(t, resolved.Found)
}
