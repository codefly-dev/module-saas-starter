package auth

import (
	"errors"
	"fmt"
)

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
	ErrTokenExpired            = errors.New("auth: token expired")
	ErrTokenSignature          = errors.New("auth: token signature invalid")
	ErrTokenMalformed          = errors.New("auth: token malformed")
	ErrTokenWrongIssuer        = errors.New("auth: token issuer mismatch")
	ErrTokenWrongAudience      = errors.New("auth: token audience mismatch")
	ErrTokenAlgForbidden       = errors.New("auth: token alg not allowed")
	ErrTokenReplay             = errors.New("auth: token jti already used")
	ErrTokenRevoked            = errors.New("auth: token revoked")
	ErrDevelopmentAuthDisabled = errors.New("auth: development authentication disabled")
	ErrInvalidOAuthRequest     = errors.New("auth: invalid oauth request")

	// Identity resolution
	ErrUnknownIdentity  = errors.New("auth: identity not found")
	ErrNoAccount        = errors.New("auth: no account for this identity")
	ErrAccountInactive  = errors.New("auth: account is not active")
	ErrSignupNotAllowed = errors.New("auth: signup is not permitted for this identity")
	ErrOrgRequired      = errors.New("auth: org required for this operation")
	ErrBootstrapClaimed = errors.New("auth: bootstrap already claimed")

	// Refresh rotation
	ErrRefreshRevoked           = errors.New("auth: refresh token revoked")
	ErrRefreshReuse             = errors.New("auth: refresh token reuse detected")
	ErrSessionUnavailable       = errors.New("auth: device session unavailable")
	ErrOrganizationAccessDenied = errors.New("auth: organization membership required")
)

const (
	RefreshRejectionHashMismatch           = "refresh_hash_mismatch"
	RefreshRejectionMFAReauthentication    = "mfa_reauthentication_required"
	RefreshRejectionUserNotActive          = "user_not_active"
	RefreshRejectionOrganizationMembership = "organization_membership_revoked"
	RefreshRejectionAbsoluteLifetime       = "session_absolute_lifetime"
	RefreshRejectionIdleTimeout            = "session_idle_timeout"
)

type refreshRejection struct {
	reason string
}

func (e *refreshRejection) Error() string {
	return fmt.Sprintf("%s: %s", ErrRefreshRevoked, e.reason)
}

func (e *refreshRejection) Unwrap() error { return ErrRefreshRevoked }

// RejectRefresh marks a refresh-policy failure as terminal. SessionStore
// implementations commit revocation of the affected family before returning
// ErrRefreshRevoked to the caller.
func RejectRefresh(reason string) error {
	if reason == "" {
		reason = "refresh_policy_rejected"
	}
	return &refreshRejection{reason: reason}
}

// RefreshRejectionReason lets SessionStore implementations distinguish a
// terminal policy decision from an operational callback failure, which must
// roll back without consuming the token.
func RefreshRejectionReason(err error) (string, bool) {
	var rejection *refreshRejection
	if !errors.As(err, &rejection) {
		return "", false
	}
	return rejection.reason, true
}
