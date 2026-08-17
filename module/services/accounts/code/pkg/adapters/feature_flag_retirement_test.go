package adapters

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type retiredFeatureFlagStore struct {
	business.Store
	platformRole string
	roleLookups  int
}

func (store *retiredFeatureFlagStore) GetPlatformRole(context.Context, string) (string, error) {
	store.roleLookups++
	return store.platformRole, nil
}

func TestUpsertFeatureFlagCompatibilityShimFailsClosed(t *testing.T) {
	previous := service
	t.Cleanup(func() { service = previous })

	store := &retiredFeatureFlagStore{platformRole: "super_admin"}
	configured, err := business.NewService(store)
	require.NoError(t, err)
	WithService(configured)

	ctx := context.WithValue(context.Background(), wool.UserIDKey, "platform-user")
	response, err := new(PlatformAdminServer).UpsertFeatureFlag(ctx, &gen.UpsertFeatureFlagRequest{Name: "legacy-flag"})
	require.Nil(t, response)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.ErrorContains(t, err, "read-only")
	require.Equal(t, 1, store.roleLookups)
}

func TestUpsertFeatureFlagCompatibilityShimPreservesAuthorization(t *testing.T) {
	previous := service
	t.Cleanup(func() { service = previous })

	store := &retiredFeatureFlagStore{platformRole: "support"}
	configured, err := business.NewService(store)
	require.NoError(t, err)
	WithService(configured)

	ctx := context.WithValue(context.Background(), wool.UserIDKey, "platform-user")
	_, err = new(PlatformAdminServer).UpsertFeatureFlag(ctx, &gen.UpsertFeatureFlagRequest{Name: "legacy-flag"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Equal(t, 1, store.roleLookups)

	_, err = new(PlatformAdminServer).UpsertFeatureFlag(context.Background(), &gen.UpsertFeatureFlagRequest{Name: "legacy-flag"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Equal(t, 1, store.roleLookups, "anonymous callers must not reach role lookup")
}
