package infra_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"api/pkg/business"
)

// ============================================================================
// GDPR tests
// ============================================================================

func TestCreateAndGetGDPRRequest(t *testing.T) {
	userID := seedUser(t)

	req := &business.GDPRRequest{
		ID:     business.NewIDString(),
		UserID: userID,
		Type:   business.GDPRExport,
		Status: business.GDPRPending,
	}
	err := testStore.CreateGDPRRequest(testCtx, req)
	require.NoError(t, err)

	got, err := testStore.GetGDPRRequest(testCtx, req.ID)
	require.NoError(t, err)
	require.Equal(t, req.ID, got.ID)
	require.Equal(t, userID, got.UserID)
	require.Equal(t, business.GDPRExport, got.Type)
	require.Equal(t, business.GDPRPending, got.Status)
	require.Empty(t, got.DownloadURL)
	require.Nil(t, got.ExpiresAt)
	require.Empty(t, got.Error)
	require.Nil(t, got.CompletedAt)
}

func TestGetGDPRRequest_NotFound(t *testing.T) {
	_, err := testStore.GetGDPRRequest(testCtx, business.NewIDString())
	require.Error(t, err)
	var storeErr *business.StoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, business.ErrTypeNotFound, storeErr.StoreErrorType)
}

func TestUpdateGDPRRequest(t *testing.T) {
	userID := seedUser(t)

	req := &business.GDPRRequest{
		ID:     business.NewIDString(),
		UserID: userID,
		Type:   business.GDPRExport,
		Status: business.GDPRPending,
	}
	require.NoError(t, testStore.CreateGDPRRequest(testCtx, req))

	// Transition to completed with a download URL.
	req.Status = business.GDPRCompleted
	req.DownloadURL = "https://storage.example.com/export.zip"
	err := testStore.UpdateGDPRRequest(testCtx, req)
	require.NoError(t, err)

	got, err := testStore.GetGDPRRequest(testCtx, req.ID)
	require.NoError(t, err)
	require.Equal(t, business.GDPRCompleted, got.Status)
	require.Equal(t, "https://storage.example.com/export.zip", got.DownloadURL)
}

func TestGetUserGDPRRequests(t *testing.T) {
	userID := seedUser(t)

	for _, rtype := range []business.GDPRRequestType{business.GDPRExport, business.GDPRDeletion} {
		req := &business.GDPRRequest{
			ID:     business.NewIDString(),
			UserID: userID,
			Type:   rtype,
			Status: business.GDPRPending,
		}
		require.NoError(t, testStore.CreateGDPRRequest(testCtx, req))
	}

	requests, err := testStore.GetUserGDPRRequests(testCtx, userID)
	require.NoError(t, err)
	require.Len(t, requests, 2)

	// All should belong to the same user.
	for _, r := range requests {
		require.Equal(t, userID, r.UserID)
	}
}

func TestGetUserGDPRRequests_Empty(t *testing.T) {
	userID := seedUser(t)

	requests, err := testStore.GetUserGDPRRequests(testCtx, userID)
	require.NoError(t, err)
	require.Empty(t, requests)
}
