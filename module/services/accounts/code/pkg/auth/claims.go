// Package auth defines the production auth boundary for the accounts service.
//
// Two paths live here:
//
//  1. Login / signup / refresh / logout — the only endpoints that ever touch
//     provider tokens, run JIT provisioning, or mint our own JWTs.
//  2. Nothing else. Business code stays auth-dumb and receives canonical
//     internal ids via sidecar-forwarded headers.
//
// Dependencies of this package:
//
//   - pkg/auth/{provider}/ — concrete TokenValidator impls (workos, dev)
//   - pkg/auth/pg/         — concrete IdentityResolver on Postgres
//   - pkg/auth/ed25519/    — concrete JWTMinter signing our own access + refresh
//
// pkg/business/* MUST NOT import this package. Ever.
//
// # Unverified email policy
//
// A provider may assert an identity whose email it has not verified. Such a
// login is PERMITTED but RESTRICTED: the user may sign in and create their own
// organization, but may NOT accept an invitation, because invitation
// acceptance authorizes on email equality and an unverified address is not a
// proof of control. The restriction is enforced at both the JIT invite path
// (pkg/auth/pg Resolver) and the business AcceptInvitation call, and surfaces
// as ErrInvitationEmailUnverified so the user is told to verify rather than
// being silently rejected. Claims.EmailVerified carries the provider's fact;
// an absent claim means false.
package auth

import (
	"context"
	"time"
)

// Claims is what a concrete TokenValidator returns after verifying a token at
// the identity provider. Used only at login/signup/refresh time. These are
// the provider's view of the caller — not our internal state.
type Claims struct {
	// Provider names the validator that produced these claims
	// ("workos" | "dev"). Stored in provider_identities.provider.
	Provider string

	// Subject is the stable provider user id (e.g. WorkOS `sub`). Stored in
	// provider_identities.provider_sub. Never leaves the auth package.
	Subject string

	// Email is the primary email for this identity at the provider. Whether
	// the provider actually verified it is carried separately in EmailVerified
	// — never assume this address is verified just because it is present.
	Email string

	// EmailVerified is the provider's own assertion that the caller controls
	// Email. An absent or falsy claim means false: we record the provider's
	// fact, not an assumption. Invitation acceptance authorizes on email
	// equality and therefore requires this to be true.
	EmailVerified bool

	// DisplayName is the provider's human-readable name for this identity,
	// persisted on first provisioning. Empty when the provider asserts no name
	// (most OIDC flows leave it unset). Purely presentational — never an
	// authentication or authorization signal.
	DisplayName string

	// ProviderOrgID is the optional WorkOS organization id (or equivalent).
	// Empty when the provider has no org concept or the user has no org yet.
	ProviderOrgID string

	// ExpiresAt is the provider token's expiry, used only for short-circuit
	// rejection — we never forward this token downstream.
	ExpiresAt time.Time
}

// Valid reports whether a set of Claims is usable for a login/signup call.
// It does NOT re-verify the signature — that's the TokenValidator's job.
func (c *Claims) Valid() error {
	if c == nil {
		return ErrMissingClaims
	}
	if c.Subject == "" {
		return ErrMissingSubject
	}
	if c.Email == "" {
		return ErrMissingEmail
	}
	if !c.ExpiresAt.IsZero() && time.Now().After(c.ExpiresAt) {
		return ErrTokenExpired
	}
	return nil
}

// TokenValidator verifies a token at the identity provider and returns
// Claims. Implementations live in provider-specific subpackages.
//
// Runs once per login/signup/refresh call. NEVER on the request hot path —
// the sidecar validates our own JWT, not provider tokens.
type TokenValidator interface {
	Validate(ctx context.Context, token string) (*Claims, error)
}
