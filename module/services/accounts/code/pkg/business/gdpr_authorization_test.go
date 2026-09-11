//go:build !pure

package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

func TestGDPRStatusIsBoundToSubjectAndRequestType(t *testing.T) {
	clearData(t)
	ctx := testCtx

	userA, _ := mustUserAndOrg(t, ctx, "alice-gdpr-authz@test.com", "alice-gdpr-authz", "GDPR A")
	userB, _ := mustUserAndOrg(t, ctx, "bob-gdpr-authz@test.com", "bob-gdpr-authz", "GDPR B")
	store := business.GDPRStore(testStore)

	newRequest := func(userID string, kind business.GDPRRequestType) *business.GDPRRequest {
		return &business.GDPRRequest{
			ID:     business.NewIDString(),
			UserID: userID,
			Type:   kind,
			Status: business.GDPRPending,
			// A pending request must name the job that owns it.
			JobID: business.NewIDString(),
		}
	}
	exportA := newRequest(userA, business.GDPRExport)
	deletionA := newRequest(userA, business.GDPRDeletion)
	exportB := newRequest(userB, business.GDPRExport)
	for _, request := range []*business.GDPRRequest{exportA, deletionA, exportB} {
		require.NoError(t, testStore.As(business.Identity{UserID: request.UserID}).Within(ctx, func(scoped context.Context) error {
			return store.CreateGDPRRequest(scoped, request)
		}))
	}

	got, err := testService.GetExportStatus(ctx, userA, exportA.ID)
	require.NoError(t, err)
	require.Equal(t, exportA.ID, got.ID)

	got, err = testService.GetDeletionStatus(ctx, userA, deletionA.ID)
	require.NoError(t, err)
	require.Equal(t, deletionA.ID, got.ID)

	// Exact UUID substitution across users is hidden by user-scoped RLS.
	_, err = testService.GetExportStatus(ctx, userA, exportB.ID)
	require.Error(t, err)

	// An export endpoint cannot be used to retrieve a deletion request (or vice
	// versa), even for the correct owner.
	_, err = testService.GetExportStatus(ctx, userA, deletionA.ID)
	require.Error(t, err)
	_, err = testService.GetDeletionStatus(ctx, userA, exportA.ID)
	require.Error(t, err)
}
