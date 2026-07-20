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

	exportA := &business.GDPRRequest{ID: business.NewIDString(), UserID: userA, Type: business.GDPRExport, Status: business.GDPRPending}
	deletionA := &business.GDPRRequest{ID: business.NewIDString(), UserID: userA, Type: business.GDPRDeletion, Status: business.GDPRPending}
	exportB := &business.GDPRRequest{ID: business.NewIDString(), UserID: userB, Type: business.GDPRExport, Status: business.GDPRPending}
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
