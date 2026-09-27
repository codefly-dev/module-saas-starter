package redisstate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/redisstate"
)

func TestTokenRevoker_RoundTrip(t *testing.T) {
	ctx := context.Background()
	r := redisstate.NewTokenRevoker(redisstate.NewMemory())

	revoked, err := r.IsRevoked(ctx, "jti-1")
	require.NoError(t, err)
	require.False(t, revoked, "unknown jti is not revoked")

	require.NoError(t, r.Revoke(ctx, "jti-1", time.Minute))

	revoked, err = r.IsRevoked(ctx, "jti-1")
	require.NoError(t, err)
	require.True(t, revoked)

	revoked, err = r.IsRevoked(ctx, "")
	require.NoError(t, err)
	require.False(t, revoked, "empty jti is never revoked")
}

func TestTokenRevoker_SessionRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := redisstate.NewTokenRevoker(redisstate.NewMemory())

	revoked, err := r.IsSessionRevoked(ctx, "sid-1")
	require.NoError(t, err)
	require.False(t, revoked, "unknown session is not revoked")

	require.NoError(t, r.RevokeSession(ctx, "sid-1", time.Minute))

	revoked, err = r.IsSessionRevoked(ctx, "sid-1")
	require.NoError(t, err)
	require.True(t, revoked)

	// The jti and session keyspaces must not collide: a revoked session must
	// not read back as a revoked jti of the same string, or vice versa.
	revoked, err = r.IsRevoked(ctx, "sid-1")
	require.NoError(t, err)
	require.False(t, revoked, "a session marker must not satisfy a jti check")

	revoked, err = r.IsSessionRevoked(ctx, "")
	require.NoError(t, err)
	require.False(t, revoked, "empty session id is never revoked")
}

func TestTokenRevoker_SessionFailsClosedOnStoreError(t *testing.T) {
	boom := errors.New("redis unreachable")
	r := redisstate.NewTokenRevoker(boomState{err: boom})

	revoked, err := r.IsSessionRevoked(context.Background(), "sid-1")
	require.Error(t, err, "a backing-store error must be surfaced, not swallowed")
	require.ErrorIs(t, err, boom)
	require.False(t, revoked)
}

// boomState is a State whose reads always fail with a non-miss error, standing
// in for a Redis outage.
type boomState struct{ err error }

func (b boomState) Get(context.Context, string) ([]byte, error) { return nil, b.err }
func (boomState) Set(context.Context, string, []byte, time.Duration) error {
	return nil
}
func (boomState) Delete(context.Context, ...string) error                    { return nil }
func (boomState) Incr(context.Context, string, time.Duration) (int64, error) { return 0, nil }

func TestTokenRevoker_FailsClosedOnStoreError(t *testing.T) {
	boom := errors.New("redis unreachable")
	r := redisstate.NewTokenRevoker(boomState{err: boom})

	revoked, err := r.IsRevoked(context.Background(), "jti-1")
	require.Error(t, err, "a backing-store error must be surfaced, not swallowed")
	require.ErrorIs(t, err, boom)
	require.False(t, revoked)
}
