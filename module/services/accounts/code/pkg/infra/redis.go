package infra

import (
	"context"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/codefly-dev/core/wool"

	"accounts/pkg/cache"
)

// NewRedisCache constructs a Cache backed by the codefly.dev/redis agent
// bound as the `cache` dependency. Reads the connection URL from the
// standard codefly config surface (same pattern as NewPostgresStore).
//
// Returns (nil, nil, nil) — NOT an error — when the cache dependency
// is not wired. Callers treat nil cache as "run without caching" which
// is the correct fallback for dev setups that don't need Redis.
func NewRedisCache(ctx context.Context) (cache.Cache, func() error, error) {
	w := wool.Get(ctx).In("NewRedisCache")

	connection, err := codefly.For(ctx).Service("cache").Secret("redis", "connection")
	if err != nil {
		// Service("cache") not declared as a dep — no cache, graceful
		// fallback. Don't error out; the app runs fine without caching.
		w.Debug("cache dep not wired, caching disabled", wool.ErrField(err))
		return nil, nil, nil
	}

	c, closeFn, err := cache.NewRedis(connection)
	if err != nil {
		return nil, nil, w.Wrapf(err, "cannot build redis cache")
	}
	return c, closeFn, nil
}
