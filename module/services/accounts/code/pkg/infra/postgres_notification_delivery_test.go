//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func TestNotificationCursorPreservesEqualTimestampsAndFiltersBeforeLimit(t *testing.T) {
	user := seedUser(t)
	org := seedOrg(t, user)
	otherOrg := seedOrg(t, user)
	require.NoError(t, testStore.WithUserTx(testCtx, user, func(ctx context.Context) error {
		for i := 0; i < 105; i++ {
			tenant := otherOrg
			if i < 3 {
				tenant = org
			}
			n := &business.Notification{ID: business.NewIDString(), UserID: user, OrgID: tenant, Title: "Source updated", Body: "Ready", Type: "info"}
			require.NoError(t, testStore.CreateNotification(ctx, n))
			if i == 0 {
				require.NoError(t, testStore.MarkNotificationRead(ctx, n.ID))
			}
		}
		return nil
	}))
	require.NoError(t, testStore.WithUserTx(testCtx, user, func(ctx context.Context) error {
		filter := business.NotificationFilter{OrgID: org, UnreadOnly: true}
		first, cursor, err := testStore.ListNotifications(ctx, user, 1, "", filter)
		require.NoError(t, err)
		require.Len(t, first, 1)
		require.Equal(t, org, first[0].OrgID)
		require.NotEmpty(t, cursor)
		second, next, err := testStore.ListNotifications(ctx, user, 1, cursor, filter)
		require.NoError(t, err)
		require.Len(t, second, 1)
		require.NotEqual(t, first[0].ID, second[0].ID)
		require.Equal(t, first[0].CreatedAt, second[0].CreatedAt)
		require.Empty(t, next)
		seen := map[string]bool{}
		cursor = ""
		for {
			page, next, err := testStore.ListNotifications(ctx, user, 10, cursor)
			require.NoError(t, err)
			for _, n := range page {
				require.False(t, seen[n.ID])
				seen[n.ID] = true
			}
			if next == "" {
				break
			}
			cursor = next
		}
		require.Len(t, seen, 105)
		_, _, err = testStore.ListNotifications(ctx, user, 10, "not-a-cursor")
		require.ErrorIs(t, err, business.ErrInvalidNotificationPageToken)
		return nil
	}))
}

func TestDatasourceFileExtensionsPersistWithoutChangingCredentials(t *testing.T) {
	user := seedUser(t)
	org := seedOrg(t, user)
	legacyID := seedDatasourceSource(t, org)
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		legacy, err := testStore.GetDatasourceSource(ctx, org, legacyID)
		require.NoError(t, err)
		require.Empty(t, legacy.FileExtensions)
		source := *legacy
		source.ID = business.NewIDString()
		source.FileExtensions = []string{".md", ".mdx"}
		source.Paths = []string{"docs"}
		source.Branch = "main"
		require.NoError(t, testStore.InsertDatasourceSource(ctx, &source))
		saved, err := testStore.GetDatasourceSource(ctx, org, source.ID)
		require.NoError(t, err)
		require.Equal(t, source.FileExtensions, saved.FileExtensions)
		require.Equal(t, source.Paths, saved.Paths)
		require.Equal(t, source.Branch, saved.Branch)
		require.Equal(t, legacy.CredentialSecretRef, saved.CredentialSecretRef)
		require.Equal(t, legacy.BoundaryNodeID, saved.BoundaryNodeID)
		old, err := testStore.GetDatasourceSource(ctx, org, legacyID)
		require.NoError(t, err)
		require.Empty(t, old.FileExtensions)
		return nil
	}))
}
