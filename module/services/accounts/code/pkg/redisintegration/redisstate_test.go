//go:build !pure

package redisintegration_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra"
	"accounts/pkg/redisstate"
)

// Revocation, nonces and rate limits are state, not cache: the behaviour below
// is what they had on pkg/cache, now proven on the real server.

func stateClient(t *testing.T) *goredis.Client {
	t.Helper()
	client, err := infra.NewRedisClient(testCtx)
	require.NoError(t, err)
	require.NotNil(t, client, "the cache dependency must be wired in this suite")
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// closedClient is a client on the real server that has been closed: every
// command fails the way an unreachable Redis does, with a non-miss error.
func closedClient(t *testing.T) *goredis.Client {
	t.Helper()
	client, err := infra.NewRedisClient(testCtx)
	require.NoError(t, err)
	require.NoError(t, client.Close())
	return client
}

// The eviction hazard, pinned: a marker the server evicts is a revocation that
// stops applying. The server this state lives on must never evict.
func TestStateServerNeverEvicts(t *testing.T) {
	policy, err := stateClient(t).ConfigGet(testCtx, "maxmemory-policy").Result()
	require.NoError(t, err)
	require.Equal(t, "noeviction", policy["maxmemory-policy"],
		"revocation markers and nonces must not be evictable; see pkg/redisstate")
}

func TestRevocationRoundTripsAcrossInstances(t *testing.T) {
	writer := redisstate.NewTokenRevoker(redisstate.NewRedis(stateClient(t)))
	reader := redisstate.NewTokenRevoker(redisstate.NewRedis(stateClient(t)))
	jti, sid := business.NewIDString(), business.NewIDString()

	revoked, err := reader.IsRevoked(testCtx, jti)
	require.NoError(t, err)
	require.False(t, revoked, "an absent marker is an authoritative not-revoked")

	require.NoError(t, writer.Revoke(testCtx, jti, time.Minute))
	require.NoError(t, writer.RevokeSession(testCtx, sid, time.Minute))

	revoked, err = reader.IsRevoked(testCtx, jti)
	require.NoError(t, err)
	require.True(t, revoked, "a revocation on one instance reaches another")
	revoked, err = reader.IsSessionRevoked(testCtx, sid)
	require.NoError(t, err)
	require.True(t, revoked)
	revoked, err = reader.IsRevoked(testCtx, sid)
	require.NoError(t, err)
	require.False(t, revoked, "session and jti markers do not collide")
}

func TestRevocationMarkerExpiresWithItsTTL(t *testing.T) {
	r := redisstate.NewTokenRevoker(redisstate.NewRedis(stateClient(t)))
	jti := business.NewIDString()
	require.NoError(t, r.Revoke(testCtx, jti, 200*time.Millisecond))
	require.Eventually(t, func() bool {
		revoked, err := r.IsRevoked(testCtx, jti)
		return err == nil && !revoked
	}, 3*time.Second, 50*time.Millisecond)
}

func TestRevocationFailsClosedOnARedisError(t *testing.T) {
	r := redisstate.NewTokenRevoker(redisstate.NewRedis(closedClient(t)))

	revoked, err := r.IsRevoked(testCtx, business.NewIDString())
	require.Error(t, err, "an unreachable Redis must surface, so the caller fails closed")
	require.False(t, revoked)
	revoked, err = r.IsSessionRevoked(testCtx, business.NewIDString())
	require.Error(t, err)
	require.False(t, revoked)
}

func TestNonceIsFreshExactlyOnceAcrossInstances(t *testing.T) {
	consumers := []*redisstate.OAuthNonceConsumer{
		redisstate.NewOAuthNonceConsumer(redisstate.NewRedis(stateClient(t))),
		redisstate.NewOAuthNonceConsumer(redisstate.NewRedis(stateClient(t))),
	}
	nonce := business.NewIDString()

	var fresh atomic.Int64
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			ok, err := consumers[i%2].Consume(testCtx, nonce, time.Minute)
			assert.NoError(t, err)
			if ok {
				fresh.Add(1)
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, 1, fresh.Load(), "one callback wins; every other is a replay")

	// The counter carries its TTL, so a consumed nonce does not live forever.
	client := stateClient(t)
	ttl, err := client.PTTL(testCtx, "oauth-nonce:"+nonce).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Duration(0))
	require.LessOrEqual(t, ttl, time.Minute)
}

func TestNonceFailsClosedOnARedisError(t *testing.T) {
	c := redisstate.NewOAuthNonceConsumer(redisstate.NewRedis(closedClient(t)))
	ok, err := c.Consume(testCtx, business.NewIDString(), time.Minute)
	require.Error(t, err)
	require.False(t, ok, "an unverifiable nonce is not a fresh one")
}

func TestRateLimitBudgetIsSharedAcrossInstances(t *testing.T) {
	limiters := []*redisstate.RateLimiter{
		redisstate.NewRateLimiter(redisstate.NewRedis(stateClient(t))),
		redisstate.NewRateLimiter(redisstate.NewRedis(stateClient(t))),
	}
	key := business.NewIDString()
	const limit = 5

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			// An hour's window: a burst cannot straddle two buckets.
			ok, _, _, err := limiters[i%2].Allow(testCtx, key, limit, time.Hour)
			assert.NoError(t, err)
			if ok {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, limit, allowed.Load(), "the budget is one counter, not one per instance")
}

func TestRateLimitSoftFailsOpenOnARedisError(t *testing.T) {
	rl := redisstate.NewRateLimiter(redisstate.NewRedis(closedClient(t)))
	for range 3 {
		ok, remaining, _, err := rl.Allow(testCtx, business.NewIDString(), 1, time.Minute)
		require.NoError(t, err)
		require.True(t, ok, "rate limiting is overload protection, not authorization")
		require.Equal(t, 1, remaining)
	}
}
