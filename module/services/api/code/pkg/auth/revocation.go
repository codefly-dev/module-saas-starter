package auth

import (
	"context"
	"time"
)

// TokenRevoker is a fast, eventually-consistent revocation list keyed
// by JWT jti. Used to invalidate individual access tokens before their
// natural expiry — the OWASP refresh-rotation pattern only invalidates
// the refresh chain, leaving any previously-issued access token usable
// for up to AccessTokenTTL after logout. With this interface, Logout()
// adds the current access token's jti to the list with TTL = (exp - now)
// and VerifyAccess consults the list before returning.
//
// Storage: Redis is the production backing (cache.RedisCache wrapped
// in a thin adapter); a no-op or in-memory implementation is fine for
// dev. ALL methods MUST be safe for concurrent use.
//
// Failure mode: Revoke errors are logged and SWALLOWED. Better to leak
// a token for the remainder of its TTL than to break logout when Redis
// is briefly unavailable — the access token still expires naturally.
// IsRevoked errors return false (fail-open, matching Cache get-miss
// semantics throughout the codebase).
type TokenRevoker interface {
	// Revoke marks jti as revoked for ttl seconds. ttl SHOULD equal the
	// token's remaining lifetime; longer wastes memory, shorter creates
	// a window where the revoked token still works.
	Revoke(ctx context.Context, jti string, ttl time.Duration) error

	// IsRevoked returns true when jti is in the revocation list.
	// Implementations MAY return false on backing-store errors —
	// callers must treat the answer as advisory.
	IsRevoked(ctx context.Context, jti string) bool
}

// NoopTokenRevoker is the fallback when no Redis is configured — accepts
// revoke calls (returns nil) and never reports anything as revoked.
// VerifyAccess falls back to TTL-only revocation in this mode, which
// is the pre-2026-04-25 behaviour.
type NoopTokenRevoker struct{}

func (NoopTokenRevoker) Revoke(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (NoopTokenRevoker) IsRevoked(_ context.Context, _ string) bool {
	return false
}
