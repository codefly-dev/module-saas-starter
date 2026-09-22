//go:build !pure

package business_test

import (
	"accounts/pkg/business"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestPlatformAdminSessionsOptionalUserFilter(t *testing.T) {
	clearData(t)
	ctx := testCtx
	adminID, _ := mustUserAndOrg(t, ctx, "admin-sessions@example.com", "admin-sessions", "Acme")
	otherID, _ := mustUserAndOrg(t, ctx, "member-sessions@example.com", "member-sessions", "ExampleCorp")
	require.NoError(t, testStore.GrantPlatformRole(ctx, adminID, "super_admin", adminID))
	for _, userID := range []string{adminID, otherID} {
		require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
			return testStore.CreateSession(ctx, &business.Session{
				ID: uuid.NewString(), UserID: userID, FamilyID: uuid.NewString(),
				RefreshTokenHash: uuid.NewString(), IPAddress: "127.0.0.1",
				DeviceInfo: map[string]string{"browser": "Example Browser"},
				ExpiresAt:  time.Now().Add(time.Hour),
			})
		}))
	}
	all, err := testService.ListActiveSessions(ctx, adminID, &gen.ListActiveSessionsRequest{})
	require.NoError(t, err)
	require.Len(t, all.Sessions, 2)
	filtered, err := testService.ListActiveSessions(ctx, adminID, &gen.ListActiveSessionsRequest{UserId: otherID})
	require.NoError(t, err)
	require.Len(t, filtered.Sessions, 1)
	require.Equal(t, otherID, filtered.Sessions[0].UserId)
	_, err = testService.ListActiveSessions(ctx, otherID, &gen.ListActiveSessionsRequest{})
	require.Error(t, err, "a regular user cannot enumerate sessions")
	unscoped, err := testStore.ListActiveSessions(context.Background(), "", 100)
	require.NoError(t, err)
	require.Empty(t, unscoped, "omitting the user filter must not bypass RLS")
}

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
	require.NotNil(t, response.Users[0].CreatedAt)
	require.NotNil(t, response.Users[0].UpdatedAt)

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		_, err := testStore.UpdateUser(ctx, targetID, map[string]any{
			"profile": map[string]string{"name": "Jane Doe", "first_name": "Jane", "last_name": "Doe"},
		})
		return err
	}))
	for _, query := range []string{"jane doe", "JANE", "Doe"} {
		result, err := testService.SearchUsers(ctx, adminID, &gen.SearchUsersRequest{Query: query})
		require.NoError(t, err)
		require.Len(t, result.Users, 1)
		require.Equal(t, targetID, result.Users[0].Uuid)
	}
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

// sessions.user_id is a uuid column, so an unparseable id is a failed cast
// inside Postgres rather than an empty page of results — and the failure is
// swallowed by the surrounding transaction, so the caller gets "commit
// unexpectedly resulted in rollback" with nothing naming the bad input. The
// assertion is on that naming: an error alone does not distinguish the guard
// from the cast, since both fail.
func TestListActiveSessionsRejectsAnUnparseableUserID(t *testing.T) {
	clearData(t)
	adminID, _ := mustUserAndOrg(t, testCtx,
		"sessions-guard@example.com", "sessions-guard", "Sessions Guard Org")
	grantPlatformRole(t, adminID, "support", adminID)

	for _, userID := range []string{" ", "not-a-uuid"} {
		_, err := testService.ListActiveSessions(testCtx, adminID,
			&gen.ListActiveSessionsRequest{UserId: userID, PageSize: 10})
		require.ErrorContains(t, err, "invalid user id",
			"user id %q must be refused by name, not as an opaque rollback", userID)
	}

	// The guard rejects only bad input: a real id still lists.
	_, err := testService.ListActiveSessions(testCtx, adminID,
		&gen.ListActiveSessionsRequest{UserId: adminID, PageSize: 10})
	require.NoError(t, err)
}
