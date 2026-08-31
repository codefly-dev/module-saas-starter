package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// revocationKeyPrefix mirrors accounts' cache.TokenRevoker key layout
// ("revoked-jti:<jti>"). The sidecar and accounts share one Redis keyspace,
// so this MUST stay byte-for-byte identical to the writer's prefix — accounts
// writes a jti marker on logout, the sidecar reads it here.
const revocationKeyPrefix = "revoked-jti:"

// sessionRevocationKeyPrefix mirrors accounts' cache.TokenRevoker
// ("revoked-session:<sid>"). Admin session-kill writes one marker per revoked
// session row; it invalidates every access token carrying that `sid` claim
// without the killer ever holding the victim's bearer. MUST stay byte-for-byte
// identical to the accounts writer.
const sessionRevocationKeyPrefix = "revoked-session:"

// defaultRevocationCacheTTL bounds how long a revocation answer is served from
// the local cache before the sidecar re-consults Redis. It is the documented
// worst-case window between a token being revoked and every sidecar replica
// rejecting it. Kept short so "kill this session now" is near-immediate.
const defaultRevocationCacheTTL = 3 * time.Second

// revocationCacheMaxEntries caps the local cache so a burst of distinct jtis
// during a Redis outage can't grow it without bound; on reaching the cap the
// next write sweeps expired entries first, then hard-evicts the oldest batch if
// the sweep frees nothing.
const revocationCacheMaxEntries = 50000

// revocationCacheEvictBatch is how many soonest-to-expire entries are dropped
// when the cache is at capacity and every entry is still live (a sweep frees
// nothing). Evicting a batch rather than one keeps the amortised cost of the
// oldest-N scan low under a sustained burst of distinct keys.
const revocationCacheEvictBatch = revocationCacheMaxEntries / 10

// revoker answers "is this access-token jti revoked?" on the gateway hot path.
// Revoked returns a non-nil error only when the backing store could not be
// consulted (network / timeout); the caller turns that into the configured
// fail-open or fail-closed decision. A nil error with revoked=false is an
// authoritative "not revoked".
//
// Forget drops any locally cached answer for jti so the next Revoked call
// consults the store. The sidecar calls it when it authorizes a logout
// request: that request would otherwise cache "not revoked" for the very
// token accounts is about to revoke, shielding an immediate replay for a
// full cache window.
type revoker interface {
	Revoked(ctx context.Context, jti string) (bool, error)
	RevokedSession(ctx context.Context, sessionID string) (bool, error)
	Forget(jti string)
}

// noopRevoker is the dev / no-Redis fallback: nothing is ever revoked. Mirrors
// accounts' auth.NoopTokenRevoker so a local stack without Redis behaves
// identically on both the sidecar and direct-to-accounts paths.
type noopRevoker struct{}

func (noopRevoker) Revoked(context.Context, string) (bool, error)        { return false, nil }
func (noopRevoker) RevokedSession(context.Context, string) (bool, error) { return false, nil }

func (noopRevoker) Forget(string) {}

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
	return s.markerPresent(ctx, revocationKeyPrefix+jti)
}

// revokedSession reports whether the session id has an unexpired
// revoked-session marker. Same store-failure contract as revoked.
func (s *redisRevocationStore) revokedSession(ctx context.Context, sessionID string) (bool, error) {
	return s.markerPresent(ctx, sessionRevocationKeyPrefix+sessionID)
}

func (s *redisRevocationStore) markerPresent(ctx context.Context, key string) (bool, error) {
	switch err := s.client.Get(ctx, key).Err(); {
	case err == nil:
		return true, nil
	case errors.Is(err, redis.Nil):
		return false, nil
	default:
		return false, fmt.Errorf("revocation store get: %w", err)
	}
}

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
	lookup        revocationLookup
	sessionLookup revocationLookup
	ttl           time.Duration
	now           func() time.Time

	// entries is keyed by the FULL redis key (prefix included) so the jti and
	// session keyspaces share one bounded cache without colliding.
	mu      sync.Mutex
	entries map[string]revocationCacheEntry
}

func newCachedRevoker(lookup, sessionLookup revocationLookup, ttl time.Duration) *cachedRevoker {
	return &cachedRevoker{
		lookup:        lookup,
		sessionLookup: sessionLookup,
		ttl:           ttl,
		now:           time.Now,
		entries:       make(map[string]revocationCacheEntry),
	}
}

func (c *cachedRevoker) Revoked(ctx context.Context, jti string) (bool, error) {
	if jti == "" {
		return false, nil
	}
	return c.answer(ctx, revocationKeyPrefix+jti, jti, c.lookup)
}

func (c *cachedRevoker) RevokedSession(ctx context.Context, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	return c.answer(ctx, sessionRevocationKeyPrefix+sessionID, sessionID, c.sessionLookup)
}

// answer serves cacheKey from the short-TTL cache, falling back to lookup(arg)
// and caching the result. Store errors are never cached — they propagate so
// the caller fails closed.
func (c *cachedRevoker) answer(ctx context.Context, cacheKey, arg string, lookup revocationLookup) (bool, error) {
	if revoked, ok := c.cached(cacheKey); ok {
		return revoked, nil
	}
	revoked, err := lookup(ctx, arg)
	if err != nil {
		return false, err
	}
	c.store(cacheKey, revoked)
	return revoked, nil
}

func (c *cachedRevoker) Forget(jti string) {
	c.mu.Lock()
	delete(c.entries, revocationKeyPrefix+jti)
	c.mu.Unlock()
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
		freed := 0
		for key, entry := range c.entries {
			if !now.Before(entry.expiresAt) {
				delete(c.entries, key)
				freed++
			}
		}
		// A burst of distinct still-live keys frees nothing; hard-evict the
		// soonest-to-expire batch so the cache stays bounded. Dropping a
		// not-revoked entry only costs a store round-trip on its next lookup;
		// dropping a revoked one is recovered the same way (Redis still holds
		// the marker), so eviction never turns a revoked token not-revoked.
		if freed == 0 {
			c.evictOldest(revocationCacheEvictBatch)
		}
	}
	c.entries[jti] = revocationCacheEntry{revoked: revoked, expiresAt: now.Add(c.ttl)}
}

// evictOldest removes the n entries with the earliest expiry. All live entries
// share one TTL, so earliest-expiry is oldest-inserted.
func (c *cachedRevoker) evictOldest(n int) {
	if n <= 0 {
		return
	}
	type keyExpiry struct {
		key     string
		expires time.Time
	}
	ordered := make([]keyExpiry, 0, len(c.entries))
	for key, entry := range c.entries {
		ordered = append(ordered, keyExpiry{key, entry.expiresAt})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].expires.Before(ordered[j].expires) })
	if n > len(ordered) {
		n = len(ordered)
	}
	for _, item := range ordered[:n] {
		delete(c.entries, item.key)
	}
}

// newRevoker builds the revoker wired into the sidecar. No Redis configured
// falls back to noopRevoker (dev parity, loudly logged); a Redis URL that
// doesn't parse is a config error, not a reason to silently run without
// revocation — the caller must treat it as fatal.
func newRevoker(redisURL string, ttl time.Duration) (revoker, error) {
	if strings.TrimSpace(redisURL) == "" {
		log.Printf("auth-gateway: no Redis configured — access-token revocation disabled (dev parity with accounts NoopTokenRevoker)")
		return noopRevoker{}, nil
	}
	store, err := newRedisRevocationStore(redisURL)
	if err != nil {
		return nil, fmt.Errorf("revocation Redis URL: %w", err)
	}
	log.Printf("auth-gateway: access-token revocation enabled (Redis-backed, %s local cache)", ttl)
	return newCachedRevoker(store.revoked, store.revokedSession, ttl), nil
}

// revocationCacheTTL resolves the local-cache window from workspace config,
// defaulting to defaultRevocationCacheTTL. Nonsensical values are config
// errors the caller must treat as fatal.
func revocationCacheTTL() (time.Duration, error) {
	raw := strings.TrimSpace(workspaceEnv("security", "SIDECAR_REVOCATION_CACHE_TTL"))
	if raw == "" {
		return defaultRevocationCacheTTL, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl < 0 || ttl > 30*time.Second {
		return 0, fmt.Errorf("SIDECAR_REVOCATION_CACHE_TTL must be a duration between 0 and 30s, got %q", raw)
	}
	return ttl, nil
}

// revocationFailsOpen reports the failure mode on a store error. Default is
// fail-closed (deny): a HIGH-severity revocation must not be bypassed by a
// Redis blip. Operators can trade strictness for availability by setting
// SIDECAR_REVOCATION_FAIL_OPEN=true — the choice is explicit, never silent.
func revocationFailsOpen() bool {
	return strings.EqualFold(strings.TrimSpace(workspaceEnv("security", "SIDECAR_REVOCATION_FAIL_OPEN")), "true")
}
