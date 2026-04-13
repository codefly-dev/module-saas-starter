package auth

import "errors"

// Sentinel errors returned by TokenValidator, IdentityResolver, and JWTMinter
// implementations. Callers should use errors.Is for equality checks.
//
// Never leak these strings back to end users verbatim — they exist for
// logging/audit and for the backend's own branching. Produce a generic
// "invalid credentials" response at the HTTP boundary.
var (
	// Claims hygiene
	ErrMissingClaims  = errors.New("auth: missing claims")
	ErrMissingSubject = errors.New("auth: missing subject")
	ErrMissingEmail   = errors.New("auth: missing email")

	// Token validation
	ErrTokenExpired       = errors.New("auth: token expired")
	ErrTokenSignature     = errors.New("auth: token signature invalid")
	ErrTokenMalformed     = errors.New("auth: token malformed")
	ErrTokenWrongIssuer   = errors.New("auth: token issuer mismatch")
	ErrTokenWrongAudience = errors.New("auth: token audience mismatch")
	ErrTokenAlgForbidden  = errors.New("auth: token alg not allowed")
	ErrTokenReplay        = errors.New("auth: token jti already used")

	// Identity resolution
	ErrUnknownIdentity  = errors.New("auth: identity not found")
	ErrOrgRequired      = errors.New("auth: org required for this operation")
	ErrBootstrapClaimed = errors.New("auth: bootstrap already claimed")

	// Refresh rotation
	ErrRefreshRevoked = errors.New("auth: refresh token revoked")
	ErrRefreshReuse   = errors.New("auth: refresh token reuse detected")
)
