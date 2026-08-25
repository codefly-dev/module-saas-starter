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
// IsRevoked, by contrast, fails CLOSED: it reports the backing-store error
// so the caller can deny rather than admit a possibly-revoked token, matching
// the sidecar's revocation check (a HIGH-severity revocation must not be
// bypassed by a Redis blip). A get-miss is (false, nil), not an error.
type TokenRevoker interface {
	// Revoke marks jti as revoked for ttl seconds. ttl SHOULD equal the
	// token's remaining lifetime; longer wastes memory, shorter creates
	// a window where the revoked token still works.
	Revoke(ctx context.Context, jti string, ttl time.Duration) error

	// IsRevoked reports whether jti is in the revocation list. A non-nil
	// error means the backing store could not be consulted; the caller MUST
	// treat that as fail-closed (deny), never as "not revoked".
	IsRevoked(ctx context.Context, jti string) (bool, error)

	// RevokeSession marks every access token carrying this session id (the
	// `sid` claim) as revoked for ttl. Admin session-kill uses it to invalidate
	// a victim's outstanding access token without possessing the token itself:
	// revoke-by-jti needs the client to forward its bearer, revoke-by-session
	// does not. ttl SHOULD equal the access-token TTL. Same swallow-on-error
	// contract as Revoke.
	RevokeSession(ctx context.Context, sessionID string, ttl time.Duration) error

	// IsSessionRevoked reports whether sessionID is in the session-revocation
	// list. Same fail-closed contract as IsRevoked.
	IsSessionRevoked(ctx context.Context, sessionID string) (bool, error)
}

// NoopTokenRevoker is the fallback when no Redis is configured — accepts
// revoke calls (returns nil) and never reports anything as revoked.
// VerifyAccess falls back to TTL-only revocation in this mode, which
// is the pre-2026-04-25 behaviour.
type NoopTokenRevoker struct{}

func (NoopTokenRevoker) Revoke(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (NoopTokenRevoker) IsRevoked(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (NoopTokenRevoker) RevokeSession(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (NoopTokenRevoker) IsSessionRevoked(_ context.Context, _ string) (bool, error) {
	return false, nil
}
