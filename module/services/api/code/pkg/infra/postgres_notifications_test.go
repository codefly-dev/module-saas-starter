package infra_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"api/pkg/business"
)

// ============================================================================
// Notification tests
// ============================================================================

func TestCreateAndListNotifications(t *testing.T) {
	userID := seedUser(t)

	for i := 0; i < 3; i++ {
		n := &business.Notification{
			ID:     business.NewIDString(),
			UserID: userID,
			Title:  "Notif",
			Body:   "Body",
			Type:   "info",
		}
		require.NoError(t, testStore.CreateNotification(testCtx, n))
		// Small sleep to ensure distinct created_at for pagination ordering.
		time.Sleep(5 * time.Millisecond)
	}

	notifs, nextToken, err := testStore.ListNotifications(testCtx, userID, 2, "")
	require.NoError(t, err)
	require.Len(t, notifs, 2)
	require.NotEmpty(t, nextToken, "should have a next page token")

	notifs2, nextToken2, err := testStore.ListNotifications(testCtx, userID, 2, nextToken)
	require.NoError(t, err)
	require.Len(t, notifs2, 1)
	require.Empty(t, nextToken2, "last page should have no token")
}

func TestGetUnreadCount(t *testing.T) {
	userID := seedUser(t)

	for i := 0; i < 3; i++ {
		n := &business.Notification{
			ID:     business.NewIDString(),
			UserID: userID,
			Title:  "Unread",
			Body:   "Body",
			Type:   "info",
		}
		require.NoError(t, testStore.CreateNotification(testCtx, n))
	}

	count, err := testStore.GetUnreadCount(testCtx, userID)
	require.NoError(t, err)
	require.Equal(t, 3, count)
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
	require.NoError(t, testStore.CreateNotification(testCtx, n))

	count, err := testStore.GetUnreadCount(testCtx, userID)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	require.NoError(t, testStore.MarkNotificationRead(testCtx, n.ID))

	count, err = testStore.GetUnreadCount(testCtx, userID)
	require.NoError(t, err)
	require.Equal(t, 0, count)

	// Marking again should be a no-op (read_at IS NULL guard)
	require.NoError(t, testStore.MarkNotificationRead(testCtx, n.ID))
}

func TestMarkAllNotificationsRead(t *testing.T) {
	userID := seedUser(t)

	for i := 0; i < 5; i++ {
		n := &business.Notification{
			ID:     business.NewIDString(),
			UserID: userID,
			Title:  "Bulk read",
			Body:   "Body",
			Type:   "info",
		}
		require.NoError(t, testStore.CreateNotification(testCtx, n))
	}

	count, err := testStore.GetUnreadCount(testCtx, userID)
	require.NoError(t, err)
	require.Equal(t, 5, count)

	require.NoError(t, testStore.MarkAllNotificationsRead(testCtx, userID))

	count, err = testStore.GetUnreadCount(testCtx, userID)
	require.NoError(t, err)
	require.Equal(t, 0, count)
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
	require.NoError(t, testStore.CreateNotification(testCtx, n))

	require.NoError(t, testStore.DeleteNotification(testCtx, n.ID))

	notifs, _, err := testStore.ListNotifications(testCtx, userID, 10, "")
	require.NoError(t, err)
	for _, notif := range notifs {
		require.NotEqual(t, n.ID, notif.ID, "deleted notification should not appear")
	}
}
