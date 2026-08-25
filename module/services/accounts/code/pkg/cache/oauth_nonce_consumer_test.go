package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/cache"
)

func TestOAuthNonceConsumer_FirstUseThenReplay(t *testing.T) {
	ctx := context.Background()
	c := cache.NewOAuthNonceConsumer(cache.NewMemory())

	first, err := c.Consume(ctx, "nonce-1", time.Minute)
	require.NoError(t, err)
	require.True(t, first, "the first consume of a nonce is a fresh use")

	replay, err := c.Consume(ctx, "nonce-1", time.Minute)
	require.NoError(t, err)
	require.False(t, replay, "a second consume of the same nonce is a replay")

	// A distinct nonce is independent.
	other, err := c.Consume(ctx, "nonce-2", time.Minute)
	require.NoError(t, err)
	require.True(t, other)
}
