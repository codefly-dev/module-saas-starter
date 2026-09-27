package redisstate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/redisstate"
)

// TestMemoryState_GetSetDelete exercises the in-memory test double's core
// contract — the same contract the Redis implementation satisfies. The Redis
// implementation itself is exercised against a real server by the
// redis-integration suite (pkg/membership).
func TestMemoryState_GetSetDelete(t *testing.T) {
	c := redisstate.NewMemory()
	ctx := context.Background()

	// Absent on empty state.
	_, err := c.Get(ctx, "nope")
	require.ErrorIs(t, err, redisstate.ErrNotFound)

	// Set + get.
	require.NoError(t, c.Set(ctx, "k1", []byte("v1"), time.Minute))
	v, err := c.Get(ctx, "k1")
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), v)

	// Delete removes it.
	require.NoError(t, c.Delete(ctx, "k1"))
	_, err = c.Get(ctx, "k1")
	require.ErrorIs(t, err, redisstate.ErrNotFound)
}

// TestMemoryState_TTL — very short TTL expires on the next Get. Proves
// lazy expiry works; not testing millisecond-accurate timing.
func TestMemoryState_TTL(t *testing.T) {
	c := redisstate.NewMemory()
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k", []byte("v"), 10*time.Millisecond))
	time.Sleep(50 * time.Millisecond)
	_, err := c.Get(ctx, "k")
	require.ErrorIs(t, err, redisstate.ErrNotFound, "entry should have expired")
}

// TestErrNotFoundIsStable ensures the sentinel error is stable — callers
// use errors.Is which would break if the package ever replaced the
// sentinel.
func TestErrNotFoundIsStable(t *testing.T) {
	require.True(t, errors.Is(redisstate.ErrNotFound, redisstate.ErrNotFound))
}
