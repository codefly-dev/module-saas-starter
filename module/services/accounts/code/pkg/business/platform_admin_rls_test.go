package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestPlatformAdminSearchUsersEntersControlPlaneScope(t *testing.T) {
	clearData(t)
	ctx := testCtx

	adminID, _ := mustUserAndOrg(
		t,
		ctx,
		"admin-platform-search@rls-test.com",
		"admin-platform-search",
		"Admin Search Org",
	)
	targetID, _ := mustUserAndOrg(
		t,
		ctx,
		"target-platform-search@rls-test.com",
		"target-platform-search",
		"Target Search Org",
	)
	require.NoError(t, testStore.GrantPlatformRole(ctx, adminID, "super_admin", adminID))

	unscoped, _, err := testStore.SearchUsers(
		context.Background(),
		"target-platform-search",
		50,
		"",
	)
	require.NoError(t, err)
	require.Empty(t, unscoped, "the users table must remain fail-closed without a scope")

	response, err := testService.SearchUsers(ctx, adminID, &gen.SearchUsersRequest{
		Query:    "target-platform-search",
		PageSize: 50,
	})
	require.NoError(t, err)
	require.Len(t, response.Users, 1)
	require.Equal(t, targetID, response.Users[0].Uuid)
}

func TestPlatformAdminSuspendUserEntersControlPlaneScope(t *testing.T) {
	clearData(t)
	ctx := testCtx

	adminID, _ := mustUserAndOrg(
		t,
		ctx,
		"admin-platform-suspend@rls-test.com",
		"admin-platform-suspend",
		"Admin Suspend Org",
	)
	targetID, _ := mustUserAndOrg(
		t,
		ctx,
		"target-platform-suspend@rls-test.com",
		"target-platform-suspend",
		"Target Suspend Org",
	)
	require.NoError(t, testStore.GrantPlatformRole(ctx, adminID, "super_admin", adminID))

	require.NoError(t, testService.SuspendUser(ctx, adminID, &gen.SuspendUserRequest{
		UserId: targetID,
		Reason: "test",
	}))

	var target *gen.User
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		target, err = testStore.GetUser(ctx, targetID)
		return err
	}))
	require.NotNil(t, target)
	require.Equal(t, gen.UserStatus_USER_STATUS_SUSPENDED, target.Status)
}
