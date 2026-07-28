package business_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func TestIncompletePrivacyWorkflowsFailBeforeCreatingRequests(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(
		t,
		testCtx,
		"privacy-unavailable@test.com",
		"privacy-unavailable",
		"Privacy unavailable",
	)

	_, err := testService.RequestExport(testCtx, userID)
	require.ErrorIs(t, err, business.ErrPrivacyWorkflowUnavailable)
	_, err = testService.RequestDeletion(testCtx, userID)
	require.ErrorIs(t, err, business.ErrPrivacyWorkflowUnavailable)

	var requests []*business.GDPRRequest
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		var listErr error
		requests, listErr = business.GDPRStore(testStore).GetUserGDPRRequests(ctx, userID)
		return listErr
	}))
	require.Empty(t, requests)
}
