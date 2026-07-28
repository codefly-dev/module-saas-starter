package business_test

// Phase 2G — user-scoped RLS (notifications, mfa_devices,
// mfa_backup_codes). Symmetric to the org-scoped tests but the GUC
// is `app.current_user_id` and the helper is WithUserTx.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// TestRLS_Notifications_CrossUserBlocked — two users each get a
// notification. User A's tx must see only A's row; user B's row
// must be invisible from A's scope. Un-wrapped reads return zero.
func TestRLS_Notifications_CrossUserBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	userA, _ := mustUserAndOrg(t, ctx, "alice-notif@rls-test.com", "alice-notif-rls", "Acme NotifA")
	userB, _ := mustUserAndOrg(t, ctx, "bob-notif@rls-test.com", "bob-notif-rls", "Acme NotifB")

	noteA, err := testService.CreateNotification(ctx, business.CreateNotificationInput{
		UserID: userA, Category: business.NotificationCategoryProduct,
		Title: "Hi A", Body: "msg-a", Type: "info",
	})
	require.NoError(t, err)
	noteB, err := testService.CreateNotification(ctx, business.CreateNotificationInput{
		UserID: userB, Category: business.NotificationCategoryProduct,
		Title: "Hi B", Body: "msg-b", Type: "info",
	})
	require.NoError(t, err)

	// Resource-ID substitution: A cannot mutate B's notification even with the
	// exact UUID. The business boundary resolves and compares ownership before
	// entering the user-scoped mutation transaction.
	require.Error(t, testService.MarkRead(ctx, userA, noteB.ID))
	require.Error(t, testService.DeleteNotification(ctx, userA, noteB.ID))

	// Owners can still mutate their own rows.
	require.NoError(t, testService.MarkRead(ctx, userA, noteA.ID))

	// As A: see exactly 1 notification (theirs).
	listA, _, err := testService.ListNotifications(ctx, userA, 50, "")
	require.NoError(t, err)
	require.Len(t, listA, 1)
	require.Equal(t, "Hi A", listA[0].Title)

	// As B: see exactly 1 (theirs).
	listB, _, err := testService.ListNotifications(ctx, userB, 50, "")
	require.NoError(t, err)
	require.Len(t, listB, 1)
	require.Equal(t, "Hi B", listB[0].Title)
	require.Nil(t, listB[0].ReadAt, "cross-user MarkRead must not alter the row")

	// Cross-user probe via Store: from A's WithUserTx, ListNotifications
	// asking for B returns zero — RLS hides B's row even though the
	// SQL filters by user_id = $1.
	require.NoError(t, testStore.WithUserTx(ctx, userA, func(ctx context.Context) error {
		stolen, _, err := testStore.ListNotifications(ctx, userB, 50, "")
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's notifications from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows — fail-closed.
	noWrap, _, err := testStore.ListNotifications(context.Background(), userA, 50, "")
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped ListNotifications must return ZERO rows (RLS fail-closed)")
}

// TestRLS_MFADevices_CrossUserBlocked — two users each enroll a
// TOTP device. User A's WithUserTx must see only A's device. RLS
// hides B's even when SQL passes B's user_id.
func TestRLS_MFADevices_CrossUserBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	userA, _ := mustUserAndOrg(t, ctx, "alice-mfa@rls-test.com", "alice-mfa-rls", "Acme MfaA")
	userB, _ := mustUserAndOrg(t, ctx, "bob-mfa@rls-test.com", "bob-mfa-rls", "Acme MfaB")

	_, _, err := testService.SetupTOTP(ctx, userA)
	require.NoError(t, err)
	_, _, err = testService.SetupTOTP(ctx, userB)
	require.NoError(t, err)

	listA, err := testService.ListMFADevices(ctx, userA)
	require.NoError(t, err)
	require.Len(t, listA, 1, "user A sees exactly their own device")
	require.Equal(t, userA, listA[0].UserID)

	listB, err := testService.ListMFADevices(ctx, userB)
	require.NoError(t, err)
	require.Len(t, listB, 1)
	require.Equal(t, userB, listB[0].UserID)

	// Cross-user probe via the MFAStore directly under A's tx,
	// asking for B's devices.
	var mfaStore business.MFAStore = testStore
	require.NoError(t, testStore.WithUserTx(ctx, userA, func(ctx context.Context) error {
		stolen, err := mfaStore.ListMFADevices(ctx, userB)
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's mfa_devices from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows.
	noWrap, err := mfaStore.ListMFADevices(context.Background(), userA)
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped ListMFADevices must return ZERO rows (RLS fail-closed)")
}

// TestRLS_MFABackupCodes_CrossUserBlocked — backup codes are even
// more sensitive than devices (one-time auth bypass). A's codes
// must NEVER appear in B's scope.
func TestRLS_MFABackupCodes_CrossUserBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	userA, _ := mustUserAndOrg(t, ctx, "alice-bc@rls-test.com", "alice-bc-rls", "Acme BcA")
	userB, _ := mustUserAndOrg(t, ctx, "bob-bc@rls-test.com", "bob-bc-rls", "Acme BcB")

	codesA, err := testService.GenerateBackupCodes(ctx, userA)
	require.NoError(t, err)
	require.NotEmpty(t, codesA)
	codesB, err := testService.GenerateBackupCodes(ctx, userB)
	require.NoError(t, err)
	require.NotEmpty(t, codesB)

	var mfaStore business.MFAStore = testStore
	// Cross-user probe via Store under A's tx for B's codes.
	require.NoError(t, testStore.WithUserTx(ctx, userA, func(ctx context.Context) error {
		stolen, err := mfaStore.GetUnusedBackupCodes(ctx, userB)
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's backup codes from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows.
	noWrap, err := mfaStore.GetUnusedBackupCodes(context.Background(), userA)
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped GetUnusedBackupCodes must return ZERO rows (RLS fail-closed)")
}

// TestWithUserTx_RejectsEmptyUserID — symmetric to the org guard.
func TestWithUserTx_RejectsEmptyUserID(t *testing.T) {
	err := testStore.WithUserTx(context.Background(), "", func(ctx context.Context) error {
		t.Fatal("fn must not run on empty userID")
		return nil
	})
	require.Error(t, err)
}

var _ = gen.SubjectKind_SUBJECT_KIND_PRINCIPAL // keep gen import for parity with other tests
