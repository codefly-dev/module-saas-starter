// Package redisstate holds the accounts state that lives in Redis and is
// authoritative there: access-token and session revocation markers, single-use
// OAuth-state nonces, and rate-limit counters. None of it is a cache. There is no
// origin to reload a lost marker from, so losing one un-revokes a token or lets a
// nonce replay; a miss is an answer ("not revoked", "fresh"), never a signal to
// look elsewhere.
//
// That is why it is not in a package called cache, and why it must never move
// onto a codefly.dev/cache stack: a cache contract makes a miss normal, lets
// values be dropped, and degrades errors to the origin. Org membership, which IS
// a copy of the store, is cached by pkg/membership instead.
//
// # Failure contract
//
// Revocation reads (IsRevoked, IsSessionRevoked) and nonce consumption return a
// Redis error to the caller, which fails closed: a revocation must not be
// bypassed during an outage, and an unverifiable nonce is not a fresh one. Rate
// limiting alone soft-fails to allow — it is overload protection, not
// authorization.
//
// # Eviction hazard: the server must run noeviction
//
// A marker that Redis evicts is a revocation that silently stops applying, and a
// nonce counter it evicts is a replay window reopening. So the server holding
// these keys must never evict: maxmemory-policy noeviction (Redis's default), or
// no maxmemory at all. Give it allkeys-lru, volatile-lru or any other eviction
// policy — a routine setting for a cache — and a revoked token passes again. At
// memory exhaustion noeviction refuses writes instead, which surfaces here as an
// error and fails closed.
//
// Today the `cache` service (the codefly.dev/redis agent) runs redis-server with
// no maxmemory, so noeviction applies, and the membership cache shares that
// server under its own key prefix. If that server is ever tuned as a cache
// (an eviction policy, a maxmemory cap chosen for cache hit rate), this state
// must first move to a Redis service of its own that keeps noeviction.
package redisstate

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound means the key holds no state. For a marker that is the
// authoritative answer (not revoked, never consumed), not a miss to reload.
var ErrNotFound = errors.New("redisstate: not found")

// State is the key-value shape the revoker, nonce consumer and rate limiter
// sit on. String-keyed and []byte-valued so the Redis and in-memory
// implementations share one contract.
type State interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
	// Incr atomically increments the integer counter at key by one and,
	// on the first increment that creates the key, arms ttl as its expiry.
	// The increment and the expiry are applied as one atomic step so a
	// concurrent burst can't race a live counter into a missing TTL (which
	// would leak the key forever). Returns the counter's new value.
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
}

// ==================== Redis implementation ====================

type redisState struct {
	client redis.UniversalClient
}

// NewRedis returns State over client. The caller owns client and closes it.
func NewRedis(client redis.UniversalClient) State {
	return &redisState{client: client}
}

func (r *redisState) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	return b, err
}

func (r *redisState) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *redisState) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return r.client.Del(ctx, keys...).Err()
}

// incrExpireScript increments a counter and arms its TTL only on the first
// increment, all inside one Redis round-trip. Doing INCR then a separate
// EXPIRE would let a second caller observe the counter between the two ops
// and skip the expiry, leaking the key; the Lua body runs atomically.
var incrExpireScript = redis.NewScript(`
local v = redis.call("INCR", KEYS[1])
if v == 1 then
	redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return v
`)

func (r *redisState) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return incrExpireScript.Run(ctx, r.client, []string{key}, ttl.Milliseconds()).Int64()
}

// ==================== In-memory implementation ====================

type memoryEntry struct {
	value     []byte
	expiresAt time.Time
}

type memoryState struct {
	mu   sync.RWMutex
	data map[string]memoryEntry
}

// NewMemory returns a process-local State for unit tests. It is never wired in
// a running service: process-local revocation or nonce state would not reach
// the other replicas. No background expiry sweeper (expiry is checked on
// read), and no eviction, which is the property the state requires.
func NewMemory() State {
	return &memoryState{data: make(map[string]memoryEntry)}
}

func (m *memoryState) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	e, ok := m.data[key]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		// Lazy expiry: checking on read is enough for a test double.
		m.mu.Lock()
		delete(m.data, key)
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	return e.value, nil
}

func (m *memoryState) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	exp := time.Time{}
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	m.data[key] = memoryEntry{value: value, expiresAt: exp}
	return nil
}

func (m *memoryState) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[key]
	if ok && !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		ok = false
	}
	var count int64
	exp := time.Time{}
	if ok {
		count, _ = strconv.ParseInt(string(e.value), 10, 64)
		exp = e.expiresAt
	} else if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	count++
	m.data[key] = memoryEntry{value: []byte(strconv.FormatInt(count, 10)), expiresAt: exp}
	return count, nil
}

func (m *memoryState) Delete(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.data, k)
	}
	return nil
}
