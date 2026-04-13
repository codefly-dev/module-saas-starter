package auth

import (
	"context"

	"github.com/google/uuid"
)

// Identity is the resolved internal state for a session. It is the shape of
// the claims embedded in our own JWT and the shape of the headers forwarded
// by the sidecar to downstream services.
//
// Every field here is canonical and internal: no provider ids leak through.
type Identity struct {
	// UserID is the canonical internal user id (uuid v7). Maps 1:N to
	// provider_identities rows: a single user may have multiple provider
	// identities linked.
	UserID uuid.UUID

	// OrgID is the active org for this session. Zero value means the user
	// has no active org (signup flow before org creation).
	OrgID uuid.UUID

	// OrgRole is the user's role inside OrgID: "owner" | "admin" | "member".
	// Empty when OrgID is zero.
	OrgRole string

	// PlatformRole is the user's global role across the platform:
	// "super_admin" | "billing" | "support" | "". Independent of OrgID.
	PlatformRole string

	// SessionID is the uuid v7 of the sessions row backing this identity.
	// Sidecar forwards it as X-Session-ID for audit correlation.
	SessionID uuid.UUID

	// ActingAsUserID is non-zero only during active impersonation. When set,
	// UserID is the real actor and ActingAsUserID is the user being viewed.
	// Business logic authorises against ActingAsUserID; audit logs against
	// UserID.
	ActingAsUserID uuid.UUID
}

// IdentityResolver translates provider Claims into an internal Identity.
// Performs JIT user provisioning on first-seen (provider, subject) pairs,
// loads session/org/role state, runs the first-super-admin bootstrap check,
// and inserts the backing sessions row — all inside a single transaction.
//
// Runs once per login/signup. NEVER on the request hot path.
type IdentityResolver interface {
	// Resolve is called at login. orgNameOnSignup is empty for /auth/login
	// and non-empty for /auth/signup when a brand-new user is creating
	// their first org.
	Resolve(ctx context.Context, c *Claims, orgNameOnSignup string) (*Identity, error)
}
