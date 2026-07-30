package adapters

import (
	"context"
	"testing"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRequireAuthAcceptsVerifiedGatewayUserID(t *testing.T) {
	ctx := context.WithValue(context.Background(), wool.UserIDKey, "user-1")
	actorID, err := requireAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, "user-1", actorID)
}

func TestRequireAuthRejectsEmptyIdentityValues(t *testing.T) {
	ctx := context.WithValue(context.Background(), wool.UserIDKey, "")
	ctx = context.WithValue(ctx, wool.UserAuthIDKey, "")
	_, err := requireAuth(ctx)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestRequireAuthRetainsDirectJWTCompatibility(t *testing.T) {
	ctx := context.WithValue(context.Background(), wool.UserAuthIDKey, "user-2")
	actorID, err := requireAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, "user-2", actorID)
}
