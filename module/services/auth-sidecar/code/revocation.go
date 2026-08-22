package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// revocationKeyPrefix mirrors accounts' cache.TokenRevoker key layout
// ("revoked-jti:<jti>"). The sidecar and accounts share one Redis keyspace,
// so this MUST stay byte-for-byte identical to the writer's prefix — accounts
// writes the marker on logout / admin session-kill, the sidecar reads it here.
const revocationKeyPrefix = "revoked-jti:"

// defaultRevocationCacheTTL bounds how long a revocation answer is served from
// the local cache before the sidecar re-consults Redis. It is the documented
// worst-case window between a token being revoked and every sidecar replica
// rejecting it. Kept short so "kill this session now" is near-immediate.
const defaultRevocationCacheTTL = 3 * time.Second

// revocationCacheMaxEntries caps the local cache so a burst of distinct jtis
// during a Redis outage can't grow it without bound; on reaching the cap the
// next write sweeps expired entries first.
const revocationCacheMaxEntries = 50000

// revoker answers "is this access-token jti revoked?" on the gateway hot path.
// Revoked returns a non-nil error only when the backing store could not be
// consulted (network / timeout); the caller turns that into the configured
// fail-open or fail-closed decision. A nil error with revoked=false is an
// authoritative "not revoked".
type revoker interface {
	Revoked(ctx context.Context, jti string) (bool, error)
}

// noopRevoker is the dev / no-Redis fallback: nothing is ever revoked. Mirrors
// accounts' auth.NoopTokenRevoker so a local stack without Redis behaves
// identically on both the sidecar and direct-to-accounts paths.
type noopRevoker struct{}

func (noopRevoker) Revoked(context.Context, string) (bool, error) { return false, nil }

// redisRevocationStore reads the shared revocation set. It reuses the sidecar's
// pooled/timeout-bounded Redis option builder so a hung Redis surfaces as a
// bounded error (ReadTimeout) rather than stalling the request.
type redisRevocationStore struct {
	client *redis.Client
}

func newRedisRevocationStore(rawURL string) (*redisRevocationStore, error) {
	options, err := redisClientOptions(rawURL)
	if err != nil {
		return nil, err
	}
	// go-redis connects lazily and reconnects on demand, so a Redis that is
	// down at boot but comes up later starts working without a restart.
	return &redisRevocationStore{client: redis.NewClient(options)}, nil
}

// revoked reports whether the jti has an unexpired revocation marker. redis.Nil
// (key absent) is an authoritative not-revoked; any other error is a store
// failure the caller must decide on — never silently swallowed here.
func (s *redisRevocationStore) revoked(ctx context.Context, jti string) (bool, error) {
	switch err := s.client.Get(ctx, revocationKeyPrefix+jti).Err(); {
	case err == nil:
		return true, nil
	case errors.Is(err, redis.Nil):
		return false, nil
	default:
		return false, fmt.Errorf("revocation store get: %w", err)
	}
}

func (s *redisRevocationStore) Close() error { return s.client.Close() }

// revocationLookup is the store-facing half of cachedRevoker, split out so
// tests can drive the cache with an in-memory or failing lookup.
type revocationLookup func(ctx context.Context, jti string) (bool, error)

type revocationCacheEntry struct {
	revoked   bool
	expiresAt time.Time
}

// cachedRevoker fronts a store with a short-TTL in-memory cache. A browser
// session replays the same access token (same jti) across many requests, so
// caching collapses the per-request Redis round-trip into at most one lookup
// per jti per TTL window. Both revoked and not-revoked answers are cached; the
// not-revoked cache is the source of the documented ≤TTL revocation window.
// Store errors are NOT cached — they propagate so the caller fails closed.
type cachedRevoker struct {
	lookup revocationLookup
	ttl    time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]revocationCacheEntry
}

func newCachedRevoker(lookup revocationLookup, ttl time.Duration) *cachedRevoker {
	return &cachedRevoker{
		lookup:  lookup,
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[string]revocationCacheEntry),
	}
}

func (c *cachedRevoker) Revoked(ctx context.Context, jti string) (bool, error) {
	if jti == "" {
		return false, nil
	}
	if revoked, ok := c.cached(jti); ok {
		return revoked, nil
	}
	revoked, err := c.lookup(ctx, jti)
	if err != nil {
		return false, err
	}
	c.store(jti, revoked)
	return revoked, nil
}

func (c *cachedRevoker) cached(jti string) (revoked bool, ok bool) {
	if c.ttl <= 0 {
		return false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[jti]
	if !found || !c.now().Before(entry.expiresAt) {
		return false, false
	}
	return entry.revoked, true
}

func (c *cachedRevoker) store(jti string, revoked bool) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.entries) >= revocationCacheMaxEntries {
		for key, entry := range c.entries {
			if !now.Before(entry.expiresAt) {
				delete(c.entries, key)
			}
		}
	}
	c.entries[jti] = revocationCacheEntry{revoked: revoked, expiresAt: now.Add(c.ttl)}
}

// newRevoker builds the revoker wired into the sidecar. With no Redis it falls
// back to noopRevoker (dev parity); with Redis it fronts the shared revocation
// set with the short-TTL cache.
func newRevoker(redisURL string, ttl time.Duration) revoker {
	if strings.TrimSpace(redisURL) == "" {
		log.Printf("auth-sidecar: no Redis configured — access-token revocation disabled (dev parity with accounts NoopTokenRevoker)")
		return noopRevoker{}
	}
	store, err := newRedisRevocationStore(redisURL)
	if err != nil {
		log.Printf("auth-sidecar: WARNING invalid Redis URL for revocation: %v — revocation disabled", err)
		return noopRevoker{}
	}
	log.Printf("auth-sidecar: access-token revocation enabled (Redis-backed, %s local cache)", ttl)
	return newCachedRevoker(store.revoked, ttl)
}

// revocationCacheTTL resolves the local-cache window from workspace config,
// defaulting to defaultRevocationCacheTTL and rejecting nonsensical values.
func revocationCacheTTL() time.Duration {
	raw := strings.TrimSpace(workspaceEnv("security", "SIDECAR_REVOCATION_CACHE_TTL"))
	if raw == "" {
		return defaultRevocationCacheTTL
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl < 0 || ttl > 30*time.Second {
		log.Printf("auth-sidecar: invalid SIDECAR_REVOCATION_CACHE_TTL %q — using %s", raw, defaultRevocationCacheTTL)
		return defaultRevocationCacheTTL
	}
	return ttl
}

// revocationFailsOpen reports the failure mode on a store error. Default is
// fail-closed (deny): a HIGH-severity revocation must not be bypassed by a
// Redis blip. Operators can trade strictness for availability by setting
// SIDECAR_REVOCATION_FAIL_OPEN=true — the choice is explicit, never silent.
func revocationFailsOpen() bool {
	return strings.EqualFold(strings.TrimSpace(workspaceEnv("security", "SIDECAR_REVOCATION_FAIL_OPEN")), "true")
}
