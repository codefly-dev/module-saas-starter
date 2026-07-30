package infra_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/email"
	"accounts/pkg/infra"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestResendDeliveryProjectionIsDurableReplaySafeAndMonotonic(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	invitationID := business.NewIDString()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `
			INSERT INTO invitations (
				id, org_id, inviter_id, inviter_display_name, email, role,
				token_hash, status, delivery_status, expires_at, last_sent_at,
				send_count
			)
			VALUES (
				$1, $2, $3, 'Test Inviter', $4, 'member',
				$5, 'pending', 'queued', NOW() + INTERVAL '7 days', NOW(), 1
			)`,
			invitationID,
			orgID,
			userID,
			fmt.Sprintf("invite-%s@test.local", invitationID),
			"token-"+invitationID,
		)
		return err
	}))

	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	recorder := infra.NewPostgresJobStore(pool)
	now := time.Now().UTC()

	inserted, err := recorder.RecordResendEvent(testCtx, email.ResendEvent{
		SvixID:          "msg-delivered-" + invitationID,
		Type:            "email.delivered",
		ProviderEmailID: "email-" + invitationID,
		InvitationID:    invitationID,
		CreatedAt:       now,
	})
	require.NoError(t, err)
	require.True(t, inserted)

	inserted, err = recorder.RecordResendEvent(testCtx, email.ResendEvent{
		SvixID:          "msg-delivered-" + invitationID,
		Type:            "email.complained",
		ProviderEmailID: "email-" + invitationID,
		InvitationID:    invitationID,
		CreatedAt:       now.Add(time.Second),
	})
	require.NoError(t, err)
	require.False(t, inserted, "the Svix id is the durable replay key")
	require.Equal(t, "delivered", invitationDeliveryStatus(t, orgID, invitationID))

	inserted, err = recorder.RecordResendEvent(testCtx, email.ResendEvent{
		SvixID:          "msg-sent-late-" + invitationID,
		Type:            "email.sent",
		ProviderEmailID: "email-" + invitationID,
		InvitationID:    invitationID,
		CreatedAt:       now.Add(-time.Second),
	})
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, "delivered", invitationDeliveryStatus(t, orgID, invitationID),
		"out-of-order sent events must not regress delivery state")

	inserted, err = recorder.RecordResendEvent(testCtx, email.ResendEvent{
		SvixID:          "msg-complained-" + invitationID,
		Type:            "email.complained",
		ProviderEmailID: "email-" + invitationID,
		InvitationID:    invitationID,
		CreatedAt:       now.Add(2 * time.Second),
	})
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, "complained", invitationDeliveryStatus(t, orgID, invitationID))
}

func TestResendProjectionRoleHasFunctionOnlyAuthority(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var canSelect, canInsert, canUpdate, canDelete, canExecute bool
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT has_table_privilege(current_user, 'email_delivery_events', 'SELECT'),
		       has_table_privilege(current_user, 'email_delivery_events', 'INSERT'),
		       has_table_privilege(current_user, 'invitations', 'UPDATE'),
		       has_table_privilege(current_user, 'email_delivery_events', 'DELETE'),
		       has_function_privilege(
		           current_user,
		           'record_resend_delivery_event(text,text,text,timestamptz,uuid)',
		           'EXECUTE'
		       )`,
	).Scan(&canSelect, &canInsert, &canUpdate, &canDelete, &canExecute))
	require.False(t, canSelect)
	require.False(t, canInsert)
	require.False(t, canUpdate)
	require.False(t, canDelete)
	require.True(t, canExecute)
}

func invitationDeliveryStatus(t *testing.T, orgID, invitationID string) string {
	t.Helper()
	var status string
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		invitation, err := testStore.GetInvitationByID(ctx, invitationID)
		if err != nil {
			return err
		}
		status = invitation.DeliveryStatus
		return nil
	}))
	return status
}
