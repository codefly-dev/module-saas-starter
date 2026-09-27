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
// accounts declares `cache` in its service.codefly.yaml, so a connection that
// is missing, unreadable or malformed is a configuration error and is
// returned: running on without it would silently drop token revocation,
// cross-replica nonces and rate limiting.
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
		return nil, w.Wrapf(err, "the cache dependency's redis connection is unavailable (accounts declares cache in service.codefly.yaml and cannot run without it)")
	}
	if connection == "" {
		return nil, w.NewError("the cache dependency's redis connection is empty (accounts declares cache in service.codefly.yaml and cannot run without it)")
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

// LegacyMembershipKeyEvictor deletes the key the previous release's
// OrgMembershipCache kept for (org, user): "t:<org>:u:<user>:orgmember". During
// a rollout, replicas on the previous release still read that key; deleting it
// on every invalidation lets a membership change served by a new replica evict
// their copies too. The other direction has no shim: a change served by an old
// replica deletes only this key, so new replicas can serve their copy for up to
// the membership stack's shared TTL (30s) until the rollout completes.
//
// TODO(#927): remove in the first deploy-counter release after the one that
// ships this (v0.0.79 is the latest tag today: if this ships in v0.0.80,
// remove it in v0.0.81). By then no replica reads the old key.
func LegacyMembershipKeyEvictor(client goredis.UniversalClient) func(ctx context.Context, orgID, userID string) error {
	return func(ctx context.Context, orgID, userID string) error {
		return client.Del(ctx, "t:"+orgID+":u:"+userID+":orgmember").Err()
	}
}
