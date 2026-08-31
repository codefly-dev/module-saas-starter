package auth

import (
	"context"
	"encoding/json"
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

	// Actor records the delegation chain when a service mints or forwards this
	// token on the end user's behalf (RFC 8693 `act`). UserID/`sub` stays the
	// end user end-to-end; Actor names the immediate caller acting for them,
	// nesting outward through each hop. Nil for a direct interactive session.
	Actor *Actor

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

	// ScopedRoles maps a fine-grained scope (e.g. a module or project key) to
	// the role names granted to the caller within OrgID at that scope. It is
	// resolved from role_assignments rows whose scope IS NOT NULL and is
	// emitted as the compact `sr` access-token claim so downstream services can
	// authorize on per-scope roles without calling back into accounts. Empty
	// when OrgID is zero or the caller holds no scoped assignments.
	ScopedRoles map[string][]string

	// ScopedRolesTruncated is true when the caller holds more than
	// MaxScopedRoleAssignments scoped grants and ScopedRoles carries only the
	// first bounded slice. It is emitted as the `srt` claim so a consumer knows
	// the header is incomplete and must fall back to CheckPermission for an
	// authoritative answer rather than treating an absent grant as a denial.
	ScopedRolesTruncated bool

	// DeviceInfo is bounded, caller-supplied display metadata for per-device
	// session management. It is never used for authorization. IPAddress is
	// trusted transport metadata when the adapter can provide it.
	DeviceInfo map[string]string
	IPAddress  string

	// Email and DisplayName are purely presentational identity, carried into
	// the access token's `email`/`name` claims so a client can render the
	// signed-in person by name or address instead of the raw user id. Neither
	// is ever an authentication or authorization signal. DisplayName is empty
	// when the provider asserted no name.
	Email       string
	DisplayName string
}

// MaxScopedRoleAssignments bounds how many (scope, role) pairs may ride in the
// `sr` claim so the access token stays a predictable size. A caller resolving
// more than this keeps the first bounded slice and has ScopedRolesTruncated
// set, so the truncation is signalled (via the `srt` claim) rather than either
// silently dropping grants or locking the user out of authentication entirely.
const MaxScopedRoleAssignments = 64

// Actor is one link in an on-behalf-of delegation chain (RFC 8693 `act`). It
// identifies a party acting for the token's subject; Act, when set, names the
// party that in turn delegated to this one, so a multi-hop call records every
// intermediary. The JSON tags are the on-the-wire `act` shape, shared by the
// token claim and the sidecar-forwarded X-Act header. The chain is audit
// metadata, not an authorization grant: the callee still authorizes the
// subject, and a service's authority to act is enforced separately (see the
// `may_act` story SVC-4), never implied by appearing here.
//
// Distinct from the `acting`/ActingAsUserID impersonation claim: that names a
// single user an admin is *viewing as*; this names the service (or user) chain
// *acting on behalf of* the subject, and can nest.
type Actor struct {
	// Subject is the acting party's canonical identity — a service's workload
	// identity (its signing `kid`) for a service hop, or a user id for an
	// admin acting on another's behalf.
	Subject string `json:"sub"`
	// Act is the next actor outward: who delegated to Subject. Nil at the head
	// of the chain (the party closest to the original subject).
	Act *Actor `json:"act,omitempty"`
}

// MaxActorChainDepth bounds how many links an on-behalf-of chain may carry so a
// signed token stays a predictable size — mirroring MaxScopedRoleAssignments'
// role in keeping the JWT bounded. It also fails closed against an accidental
// cycle in an in-memory chain, which would otherwise be walked forever.
const MaxActorChainDepth = 10

// ValidateActorChain rejects a delegation chain that is deeper than
// MaxActorChainDepth or has an empty subject at any link. A nil chain (the
// common direct-session case) is valid. Callers mint and verify through this so
// neither an oversized token is produced nor an oversized/blank one is trusted.
func ValidateActorChain(actor *Actor) error {
	for depth := 0; actor != nil; actor = actor.Act {
		depth++
		if depth > MaxActorChainDepth {
			return ErrActorChainTooDeep
		}
		if actor.Subject == "" {
			return ErrActorSubjectMissing
		}
	}
	return nil
}

// MarshalActor encodes a chain as the JSON carried by the X-Act header. A nil
// chain yields the empty string, which callers omit.
func MarshalActor(actor *Actor) (string, error) {
	if actor == nil {
		return "", nil
	}
	encoded, err := json.Marshal(actor)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// ParseActor decodes an X-Act header value back into a chain, returning nil for
// an empty, malformed, or policy-violating (too deep / blank subject) payload.
// A miss is nil rather than an error because a chain absent from context reads
// as "no recorded delegation", never as a denial.
func ParseActor(raw string) *Actor {
	if raw == "" {
		return nil
	}
	var actor Actor
	if err := json.Unmarshal([]byte(raw), &actor); err != nil {
		return nil
	}
	if ValidateActorChain(&actor) != nil {
		return nil
	}
	return &actor
}

// WithActor returns a copy of i whose delegation chain has actor pushed on as
// the immediate caller, nesting any existing Actor beneath it. It is how a
// service records that it is forwarding an existing on-behalf-of token one more
// hop: the subject is unchanged, and the new actor wraps the prior chain.
func (i *Identity) WithActor(actor string) *Identity {
	next := *i
	next.Actor = &Actor{Subject: actor, Act: i.Actor}
	return &next
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
//   - SsoJitIntent authenticates claims from an organization-bound provider and,
//     per that org's provisioning policy, may provision the caller into exactly
//     that org — never creating an organization.
//
// Only Signup, Invite, and SsoJit may create a user; Login never does.
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

// SsoJitIntent authenticates claims asserted by the identity provider bound to
// OrgID (the org's own IdP) and applies that org's provisioning policy to a
// first-seen identity: JIT-provision it into OrgID, gate it behind a pending
// invitation, or reject it. It may create the user and the OrgID membership row
// but never an organization, and it only ever touches OrgID's membership — an
// already-provisioned member resolves as a plain login. OrgID is the canonical
// internal org id, resolved from the provider's asserted org by the login route.
type SsoJitIntent struct{ OrgID uuid.UUID }

func (LoginIntent) isIntent()  {}
func (InviteIntent) isIntent() {}
func (SignupIntent) isIntent() {}
func (SsoJitIntent) isIntent() {}

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
