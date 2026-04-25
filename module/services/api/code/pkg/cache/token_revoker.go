package cache

import (
	"context"
	"errors"
	"time"
)

// TokenRevoker is a thin adapter from the generic Cache interface to
// the auth.TokenRevoker shape. It stores nothing more than a marker
// byte per revoked jti; presence-with-non-zero-TTL = "revoked".
//
// Sized for the access-token TTL (15 min default), so memory usage is
// bounded by burst-logout volume, not by total user count.
type TokenRevoker struct {
	cache Cache
}

// NewTokenRevoker wraps a Cache so it can act as the access-token
// revocation list. Pass NewMemory() in tests, NewRedis(...) in prod.
func NewTokenRevoker(c Cache) *TokenRevoker {
	return &TokenRevoker{cache: c}
}

const tokenRevokerPrefix = "revoked-jti:"

// Revoke marks jti as revoked for ttl. The marker value is a single
// byte (0x01) — the cache key's existence is the revocation signal,
// the value is just there because Cache.Set requires one.
func (r *TokenRevoker) Revoke(ctx context.Context, jti string, ttl time.Duration) error {
	if jti == "" || ttl <= 0 {
		return nil
	}
	return r.cache.Set(ctx, tokenRevokerPrefix+jti, []byte{1}, ttl)
}

// IsRevoked returns true iff jti has an unexpired revocation marker.
// Errors (Redis down, etc.) are treated as misses — we'd rather honor
// a token that should have been revoked than reject every request
// during a Redis blip. Logout's blast radius is bounded by access TTL
// regardless.
func (r *TokenRevoker) IsRevoked(ctx context.Context, jti string) bool {
	if jti == "" {
		return false
	}
	_, err := r.cache.Get(ctx, tokenRevokerPrefix+jti)
	if err != nil {
		// ErrNotFound is the expected miss path; anything else (Redis
		// unreachable etc.) we silently treat as not-revoked.
		_ = errors.Is(err, ErrNotFound)
		return false
	}
	return true
}
