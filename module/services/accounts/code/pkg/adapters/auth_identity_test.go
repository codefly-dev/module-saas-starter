package adapters

import (
	"context"
	"testing"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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

// The session-bearing gateway forwards user.id but leaves user.auth.id blank
// (it only carries an API-key id). wool.GRPC().Inject() copies that blank
// metadata value over the stamped identity, so a handler reading UserAuthID()
// directly sees a present-but-empty actor and every platform-role-gated call
// fails closed. requireAuth prefers the canonical user.id and rejects empties,
// which is why actor resolution must route through it.
func TestRequireAuthSurvivesBlankForwardedAuthID(t *testing.T) {
	md := metadata.Pairs(
		string(wool.UserIDKey), "user-3",
		string(wool.UserAuthIDKey), "",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	w := wool.Get(ctx)
	w.GRPC().Inject()
	authID, found := w.UserAuthID()
	require.True(t, found, "blank forwarded auth id is present, not absent")
	require.Empty(t, authID, "a direct UserAuthID() read collapses to an empty actor")

	actorID, err := requireAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, "user-3", actorID)
}
