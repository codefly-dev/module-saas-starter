package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// ============================================================================
// GDPR tests — gdpr_requests is RLS-protected (user-scoped); each test runs its
// store ops as the owning user via As(Identity{UserID}). NotFound runs as System
// (no owner to scope to).
// ============================================================================

func TestCreateAndGetGDPRRequest(t *testing.T) {
	userID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		req := &business.GDPRRequest{
			ID:     business.NewIDString(),
			UserID: userID,
			Type:   business.GDPRExport,
			Status: business.GDPRPending,
		}
		require.NoError(t, testStore.CreateGDPRRequest(ctx, req))

		got, err := testStore.GetGDPRRequest(ctx, req.ID)
		require.NoError(t, err)
		require.Equal(t, req.ID, got.ID)
		require.Equal(t, userID, got.UserID)
		require.Equal(t, business.GDPRExport, got.Type)
		require.Equal(t, business.GDPRPending, got.Status)
		require.Empty(t, got.DownloadURL)
		require.Nil(t, got.ExpiresAt)
		require.Empty(t, got.Error)
		require.Nil(t, got.CompletedAt)
		return nil
	}))
}

func TestGetGDPRRequest_NotFound(t *testing.T) {
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		_, err := testStore.GetGDPRRequest(ctx, business.NewIDString())
		require.Error(t, err)
		var storeErr *business.StoreError
		require.ErrorAs(t, err, &storeErr)
		require.Equal(t, business.ErrTypeNotFound, storeErr.StoreErrorType)
		return nil
	}))
}

func TestUpdateGDPRRequest(t *testing.T) {
	userID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		req := &business.GDPRRequest{
			ID:     business.NewIDString(),
			UserID: userID,
			Type:   business.GDPRExport,
			Status: business.GDPRPending,
		}
		require.NoError(t, testStore.CreateGDPRRequest(ctx, req))

		// Transition to completed with a download URL.
		req.Status = business.GDPRCompleted
		req.DownloadURL = "https://storage.example.com/export.zip"
		require.NoError(t, testStore.UpdateGDPRRequest(ctx, req))

		got, err := testStore.GetGDPRRequest(ctx, req.ID)
		require.NoError(t, err)
		require.Equal(t, business.GDPRCompleted, got.Status)
		require.Equal(t, "https://storage.example.com/export.zip", got.DownloadURL)
		return nil
	}))
}

func TestGetUserGDPRRequests(t *testing.T) {
	userID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		for _, rtype := range []business.GDPRRequestType{business.GDPRExport, business.GDPRDeletion} {
			req := &business.GDPRRequest{
				ID:     business.NewIDString(),
				UserID: userID,
				Type:   rtype,
				Status: business.GDPRPending,
			}
			require.NoError(t, testStore.CreateGDPRRequest(ctx, req))
		}

		requests, err := testStore.GetUserGDPRRequests(ctx, userID)
		require.NoError(t, err)
		require.Len(t, requests, 2)
		for _, r := range requests {
			require.Equal(t, userID, r.UserID)
		}
		return nil
	}))
}

func TestGetUserGDPRRequests_Empty(t *testing.T) {
	userID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		requests, err := testStore.GetUserGDPRRequests(ctx, userID)
		require.NoError(t, err)
		require.Empty(t, requests)
		return nil
	}))
}
