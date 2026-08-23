package auth

import (
	"context"

	"github.com/google/uuid"
)

// TokenPair is the output of a successful login/signup/refresh.
//
// AccessToken is a short-lived (3 min) signed JWT carrying the Identity as
// claims. Clients send it on every request and the sidecar validates it.
//
// RefreshToken is a long-lived (7 days) opaque token whose hash is stored in
// sessions.refresh_token_hash. Clients send it only to /auth/refresh. Each
// refresh rotates the token — the previous refresh becomes invalid, and
// reuse triggers family revocation.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
}

// JWTMinter owns the creation and verification of our own access + refresh
// tokens. One implementation backed by an Ed25519 keypair loaded from Vault
// (pkg/auth/ed25519). Any second implementation is a test fake.
//
// The sidecar uses a separate, simpler validator (just signature + exp check)
// and does NOT depend on JWTMinter — this interface lives in the backend and
// deals with session state, key rotation, and replay protection.
type JWTMinter interface {
	// Mint issues a fresh access+refresh pair for an Identity, inserting the
	// refresh hash into sessions inside the provided context.
	//
	// On refresh rotation, the previous refresh's sessions row is marked
	// revoked and a new row inserted with the same family_id.
	Mint(ctx context.Context, identity *Identity) (*TokenPair, error)

	// VerifyAccess parses and validates a previously-minted access token,
	// returning the embedded Identity (UserID, OrgID, OrgRole, PlatformRole,
	// SessionID, ActingAsUserID). Used by the in-process Connect auth
	// interceptor when there is no upstream sidecar to translate the
	// `Authorization: Bearer …` header into the X-User-Id / X-Org-Id /
	// X-Roles headers that callerID() reads.
	VerifyAccess(tokenString string) (*Identity, error)

	// VerifyRefresh accepts an opaque refresh token, looks it up in sessions
	// by hash (constant-time compare), enforces not-revoked/not-expired, and
	// rotates the token: revokes the submitted refresh and mints a new
	// access+refresh pair in the same family. The returned TokenPair is
	// what /auth/refresh hands back to the client.
	//
	// If the submitted token hashes to a row consumed by a prior rotation, the
	// store commits revocation of every active session for that user before
	// ErrRefreshReuse is returned — strict OWASP refresh-token reuse handling.
	// Tokens revoked by logout or an authorization mutation return
	// ErrRefreshRevoked without being misclassified as an attacker replay.
	VerifyRefresh(ctx context.Context, refreshToken string) (*TokenPair, error)

	// SwitchOrganization issues a fresh access token for a current membership
	// while preserving the refresh token, session row, device family, and both
	// lifetime boundaries. userID and sessionID must come from verified request
	// identity, never directly from the request body.
	SwitchOrganization(ctx context.Context, userID, sessionID, organizationID uuid.UUID) (string, error)

	// Revoke marks all sessions in a family as revoked. Called by /auth/logout.
	Revoke(ctx context.Context, refreshToken string) error

	// RevokeAccess adds the given access token's jti to the revocation
	// list with TTL = remaining lifetime. Pairs with Revoke() on logout
	// to invalidate BOTH halves of the pair — without this, the old
	// access token stays valid until natural expiry (3 min default).
	// No-op when the token cannot be parsed (already invalid).
	RevokeAccess(ctx context.Context, accessToken string) error

	// JWKS returns the public portion of the signing key as a JSON Web Key
	// Set for external tooling. The sidecar loads its key from Vault
	// directly; this endpoint is non-authoritative.
	JWKS() (string, error)
}
