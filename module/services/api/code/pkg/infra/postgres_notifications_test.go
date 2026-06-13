package infra_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"api/pkg/business"
)

// notifications is RLS-protected by user_id (Phase 2G); each test
// wraps direct Store calls in WithUserTx for the seeded user.

func TestCreateAndListNotifications(t *testing.T) {
	userID := seedUser(t)

	// Each insert in its OWN WithUserTx so created_at increments
	// (CURRENT_TIMESTAMP returns the tx start time — wrapping all
	// 3 in one tx would give them the same value and break the
	// pagination cursor).
	for i := 0; i < 3; i++ {
		n := &business.Notification{
			ID:     business.NewIDString(),
			UserID: userID,
			Title:  "Notif",
			Body:   "Body",
			Type:   "info",
		}
		require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
			return testStore.CreateNotification(ctx, n)
		}))
		time.Sleep(5 * time.Millisecond)
	}

	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		notifs, nextToken, err := testStore.ListNotifications(ctx, userID, 2, "")
		require.NoError(t, err)
		require.Len(t, notifs, 2)
		require.NotEmpty(t, nextToken, "should have a next page token")

		notifs2, nextToken2, err := testStore.ListNotifications(ctx, userID, 2, nextToken)
		require.NoError(t, err)
		require.Len(t, notifs2, 1)
		require.Empty(t, nextToken2, "last page should have no token")
		return nil
	}))
}

func TestGetUnreadCount(t *testing.T) {
	userID := seedUser(t)

	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		for i := 0; i < 3; i++ {
			n := &business.Notification{
				ID:     business.NewIDString(),
				UserID: userID,
				Title:  "Unread",
				Body:   "Body",
				Type:   "info",
			}
			require.NoError(t, testStore.CreateNotification(ctx, n))
		}

		count, err := testStore.GetUnreadCount(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, 3, count)
		return nil
	}))
}

func TestMarkNotificationRead(t *testing.T) {
	userID := seedUser(t)

	n := &business.Notification{
		ID:     business.NewIDString(),
		UserID: userID,
		Title:  "Read me",
		Body:   "Body",
		Type:   "info",
	}
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateNotification(ctx, n))

		count, err := testStore.GetUnreadCount(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, 1, count)

		require.NoError(t, testStore.MarkNotificationRead(ctx, n.ID))

		count, err = testStore.GetUnreadCount(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, 0, count)

		// Marking again should be a no-op (read_at IS NULL guard).
		require.NoError(t, testStore.MarkNotificationRead(ctx, n.ID))
		return nil
	}))
}

func TestMarkAllNotificationsRead(t *testing.T) {
	userID := seedUser(t)

	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		for i := 0; i < 5; i++ {
			n := &business.Notification{
				ID:     business.NewIDString(),
				UserID: userID,
				Title:  "Bulk read",
				Body:   "Body",
				Type:   "info",
			}
			require.NoError(t, testStore.CreateNotification(ctx, n))
		}

		count, err := testStore.GetUnreadCount(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, 5, count)

		require.NoError(t, testStore.MarkAllNotificationsRead(ctx, userID))

		count, err = testStore.GetUnreadCount(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, 0, count)
		return nil
	}))
}

func TestDeleteNotification(t *testing.T) {
	userID := seedUser(t)

	n := &business.Notification{
		ID:     business.NewIDString(),
		UserID: userID,
		Title:  "Delete me",
		Body:   "Body",
		Type:   "warning",
	}
	require.NoError(t, testStore.WithUserTx(testCtx, userID, func(ctx context.Context) error {
		require.NoError(t, testStore.CreateNotification(ctx, n))
		require.NoError(t, testStore.DeleteNotification(ctx, n.ID))

		notifs, _, err := testStore.ListNotifications(ctx, userID, 10, "")
		require.NoError(t, err)
		for _, notif := range notifs {
			require.NotEqual(t, n.ID, notif.ID, "deleted notification should not appear")
		}
		return nil
	}))
}
