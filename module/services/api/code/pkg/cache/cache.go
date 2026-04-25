// Package cache provides a small typed caching layer on top of Redis.
//
// What lives in here:
//   - A narrow Cache interface (Get / Set / Delete / DeletePrefix) that's
//     easy to fake in tests and easy to implement on top of any KV store.
//   - A Redis-backed implementation wired from the cache service's
//     connection string (exposed by codefly.dev/redis).
//   - An in-memory fallback used when Redis is unavailable (tests, or
//     if the cache service is commented out of deps) so the app stays
//     functional — cache is for speed, not correctness.
//   - Typed caches for the hot paths (org membership, user lookup) that
//     encode the key format and TTL in one place so callers don't
//     redo cache-key plumbing every time.
//
// Design rules:
//   - Cache failures NEVER bubble up. Every Get that errors is treated
//     as a miss; every Set failure is logged and swallowed. The DB is
//     always the source of truth.
//   - Invalidation is explicit and synchronous with the mutation that
//     caused it — if we wait on eventual consistency, tests flake.
//   - Keys are namespaced by type ("orgmember:<org>:<user>") so
//     DeletePrefix can wipe a whole type without touching others.
package cache

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound signals a cache miss. Callers should treat it as "no data"
// and hit the source of truth, never as a real error.
var ErrNotFound = errors.New("cache: not found")

// Cache is the shape every typed cache sits on top of. Deliberately
// string-keyed and []byte-valued so the Redis and in-memory impls can
// share the same contract; typed wrappers above (OrgMembershipCache
// etc.) marshal their own structures.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

// ==================== Redis implementation ====================

type redisCache struct {
	client *redis.Client
}

// NewRedis builds a Cache backed by go-redis against the given connection
// URL (expected shape: "redis://[:password@]host:port"). Returns the
// cache AND a close func so callers can defer shutdown.
func NewRedis(url string) (Cache, func() error, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, nil, err
	}
	client := redis.NewClient(opt)
	return &redisCache{client: client}, client.Close, nil
}

func (r *redisCache) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	return b, err
}

func (r *redisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *redisCache) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return r.client.Del(ctx, keys...).Err()
}

// ==================== In-memory fallback ====================

type memoryEntry struct {
	value     []byte
	expiresAt time.Time
}

type memoryCache struct {
	mu   sync.RWMutex
	data map[string]memoryEntry
}

// NewMemory returns a process-local Cache. Used as a fallback when Redis
// is unavailable, and in unit tests that don't want a real Redis running.
// The implementation is intentionally minimal — no background expiry
// sweeper (expiry checked on Get), no LRU eviction (unbounded in size).
// Don't use for very large working sets.
func NewMemory() Cache {
	return &memoryCache{data: make(map[string]memoryEntry)}
}

func (m *memoryCache) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	e, ok := m.data[key]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		// Lazy expiry — real cache libs use a sweeper but for our scale
		// checking on read is enough.
		m.mu.Lock()
		delete(m.data, key)
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	return e.value, nil
}

func (m *memoryCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	exp := time.Time{}
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	m.data[key] = memoryEntry{value: value, expiresAt: exp}
	return nil
}

func (m *memoryCache) Delete(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.data, k)
	}
	return nil
}
