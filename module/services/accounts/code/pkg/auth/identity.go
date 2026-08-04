package auth

import (
	"context"
	"time"

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

	// OrgID is the active org for this session. Zero value is the explicit
	// orgless state (see Orgless): the resolver found no single organization to
	// place this session in. Never a guess — an orgless identity still
	// authenticates and mints a token.
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

	// MFASatisfied is the legacy compatibility bit used by older consumers.
	// New policy code uses AssuranceLevel + MFAVerifiedAt instead. It is true
	// when this session has cleared the MFA gate
	// (either the user never enrolled MFA, or they completed a TOTP /
	// backup-code challenge during this login). Sensitive operations
	// reject sessions where this is false via requireMFA(ctx).
	MFASatisfied bool

	// AuthenticationMethods is projected into the standard JWT `amr` claim.
	// AuthenticatedAt becomes `auth_time`; AssuranceLevel becomes `acr`.
	// MFAVerifiedAt is deliberately separate so refresh rotation cannot make
	// an old second-factor ceremony look like a recent step-up.
	AuthenticationMethods []string
	AuthenticatedAt       time.Time
	AssuranceLevel        string
	MFAVerifiedAt         time.Time

	// DeviceInfo is bounded, caller-supplied display metadata for per-device
	// session management. It is never used for authorization. IPAddress is
	// trusted transport metadata when the adapter can provide it.
	DeviceInfo map[string]string
	IPAddress  string
}

// Orgless reports whether this identity resolved to no active organization. It
// is a first-class session state, not an error: a user who belongs to no org —
// or to several with no recorded preference and no SSO assertion — authenticates
// successfully and the frontend renders an organization chooser rather than
// guessing a tenant. A resolution *failure* returns an error and no Identity;
// an orgless *success* returns this.
func (i *Identity) Orgless() bool { return i.OrgID == uuid.Nil }

// Intent names why an authenticated caller is being resolved. It is a sealed
// set: only this package defines the variants, so a resolver can switch over
// them exhaustively.
//
//   - LoginIntent  authenticates an identity that must already exist.
//   - InviteIntent authenticates against a pending invitation, provisioning the
//     invitee if needed and binding them to the invitation's organization.
//   - SignupIntent provisions a first-seen identity and optionally its first org.
//
// Only Signup and Invite may create a user; Login never does.
type Intent interface{ isIntent() }

// LoginIntent resolves an identity that must already exist. A resolver returns
// ErrNoAccount when no identity backs the claims.
type LoginIntent struct{}

// InviteIntent resolves an identity against a pending invitation. Token is the
// plaintext invitation credential delivered to the invitee.
type InviteIntent struct{ Token string }

// SignupIntent provisions a first-seen identity. OrganizationName, when
// non-empty, also creates the caller's first organization with them as owner.
type SignupIntent struct{ OrganizationName string }

func (LoginIntent) isIntent()  {}
func (InviteIntent) isIntent() {}
func (SignupIntent) isIntent() {}

// IdentityResolver translates provider Claims into an internal Identity.
// Provisioning is gated by Intent: SignupIntent and InviteIntent may create a
// user on a first-seen (provider, subject) pair, LoginIntent never does. The
// resolver also loads session/org/role state and runs the first-super-admin
// bootstrap check — all inside a single transaction.
//
// Runs once per login/signup. NEVER on the request hot path.
type IdentityResolver interface {
	Resolve(ctx context.Context, c *Claims, intent Intent) (*Identity, error)
}
