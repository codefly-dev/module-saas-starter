package infra

import (
	"context"

	"github.com/codefly-dev/core/wool"
	cache "github.com/codefly-dev/interface-cache/go/cache"
	codefly "github.com/codefly-dev/sdk-go"
	rediscache "github.com/codefly-dev/service-redis/cache"
	goredis "github.com/redis/go-redis/v9"
)

// NewRedisClient opens a client on the `cache` dependency (the codefly.dev/redis
// agent) from the connection its `redis` configuration group resolves to. Two
// users share it: the authoritative state in pkg/redisstate, and the membership
// cache's shared layer (MembershipCacheLayer). It does not contact the server.
//
// Returns (nil, nil) when the cache dependency is not wired, which callers treat
// as "run without Redis". A connection that is wired but does not parse is a
// configuration error and is returned.
//
// Which server it reaches: `redis.connection` names the primary, the process
// behind the `write` endpoint. At agent 0.0.94 there are no replicas at all
// (with-read-replicas is ignored and one process serves both aliases); agents
// with real replicas keep `redis.connection` on the primary and carry the
// replicas separately (`redis.read-connection`), which nothing here reads. So
// revocation markers, nonces, counters, and the membership cache's fill leases
// and invalidations all reach the one server that accepts writes.
func NewRedisClient(ctx context.Context) (*goredis.Client, error) {
	w := wool.Get(ctx).In("NewRedisClient")

	connection, err := codefly.For(ctx).Service("cache").Secret("redis", "connection")
	if err != nil {
		w.Debug("cache dep not wired, running without redis", wool.ErrField(err))
		return nil, nil
	}
	options, err := goredis.ParseURL(connection)
	if err != nil {
		return nil, w.Wrapf(err, "cannot parse the cache dependency's redis connection")
	}
	return goredis.NewClient(options), nil
}

// membershipCachePrefix namespaces the membership cache's keys on the shared
// server, apart from redisstate's keys and auth-gateway's.
const membershipCachePrefix = "accounts:membership"

// MembershipCacheLayer is the codefly.dev/cache layer the membership stack
// shares across replicas: the service-redis driver over client.
//
// STOPGAP (#927), not the fix. The fix is cache.Open(ctx,
// codefly.For(ctx).Service("cache")) with the driver blank-imported, reading the
// `cache` configuration group the provider emits. The redis agent this module
// pins (0.0.94) predates that group (service-redis#76), so cache.Open would fail
// with "cache.driver not emitted". Until saas moves to an agent release that
// emits it — releases are held — this builds the driver's layer directly from
// the resolved `redis` connection: the same server, nothing hard-coded. The fix
// changes who opens the client and the key prefix, nothing a reader observes.
func MembershipCacheLayer(client goredis.UniversalClient) cache.Layer {
	return rediscache.New(client, rediscache.WithPrefix(membershipCachePrefix))
}
