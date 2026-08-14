package pgauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"accounts/pkg/auth"
	"accounts/pkg/business"
)

// BootstrapAdminEmailEnv is the compatibility environment key used only when
// runtime composition has not supplied an explicit bootstrap email. Codefly
// composition calls SetBootstrapAdminEmail and remains authoritative.
const BootstrapAdminEmailEnv = "BOOTSTRAP_ADMIN_EMAIL"

// Resolver is the production IdentityResolver backed by Postgres.
//
// On every authentication it:
//  1. Looks up user_identities by (provider, subject) without writing.
//  2. Branches on Intent:
//     - Login rejects an absent identity with ErrNoAccount.
//     - Signup provisions the identity and optionally its first org.
//     - Invite validates a pending invitation, provisions the invitee if
//     needed, and joins them to the inviting org — all atomically.
//  3. Loads the active org membership (Login) or the provisioned one.
//  4. Runs the bootstrap check: if BOOTSTRAP_ADMIN_EMAIL matches (case
//     insensitive) and bootstrap_state has no bootstrapped_at, grants
//     super_admin and stamps bootstrap_state. Self-disarms forever.
//  5. Returns an Identity with canonical internal ids ready to be minted
//     into a JWT.
//
// All of the above runs inside a single serializable transaction so
// concurrent first authentications of the same identity converge on a single
// user row.
type Resolver struct {
	bootstrap           BootstrapStore
	bootstrapAdminEmail *string
	signupMode          auth.SignupMode
}

// BootstrapStore is the narrow pre-auth database capability. It deliberately
// exposes neither a raw pool nor connection credentials; the composition root
// owns the audited role transition and bounds the capability to one
// transaction callback. WithAuthLookupTx is the read-only sibling used to route
// the intent before the writing resolution transaction opens.
type BootstrapStore interface {
	WithAuthBootstrapTx(context.Context, func(context.Context, pgx.Tx) error) error
	WithAuthLookupTx(context.Context, func(context.Context, pgx.Tx) error) error
}

func NewResolver(bootstrap BootstrapStore) *Resolver {
	return &Resolver{bootstrap: bootstrap}
}

// SetBootstrapAdminEmail makes runtime composition authoritative for the
// one-time bootstrap identity. Tests and legacy standalone callers that do not
// call this setter retain the environment-based compatibility path.
func (r *Resolver) SetBootstrapAdminEmail(email string) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	r.bootstrapAdminEmail = &normalized
}

// SetSignupMode selects the access policy applied to first-seen identities. The
// zero value (open) is the default, so a Resolver constructed without this
// setter provisions every authenticated identity as before.
func (r *Resolver) SetSignupMode(mode auth.SignupMode) {
	r.signupMode = mode
}

// ResolveOrgProvisioning maps a provider-asserted organization id to our
// canonical org id and reports whether that org carries a JIT provisioning
// policy. The login route calls it to decide whether an org-bound login must be
// resolved as an SsoJitIntent; a provider with no asserted org, an org we have
// never provisioned, or an org with no policy configured all return
// hasPolicy=false so the route keeps its existing intent selection.
//
// The lookup is a read-only pre-authentication transaction — the authoritative
// policy is re-read inside the resolution transaction, so this only gates intent
// selection and never authorizes a write.
func (r *Resolver) ResolveOrgProvisioning(ctx context.Context, providerOrgID string) (uuid.UUID, bool, error) {
	if r.bootstrap == nil {
		return uuid.Nil, false, errors.New("pgauth: bootstrap store is required")
	}
	if providerOrgID == "" {
		return uuid.Nil, false, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var orgID uuid.UUID
	var mode *string
	err := r.bootstrap.WithAuthLookupTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id, sso_provision_mode
			FROM organizations
			WHERE sso_organization_id = $1`,
			providerOrgID,
		).Scan(&orgID, &mode)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("pgauth: resolve org provisioning: %w", err)
	}
	if mode == nil || *mode == "" {
		return orgID, false, nil
	}
	return orgID, true, nil
}

// Resolve implements auth.IdentityResolver.
func (r *Resolver) Resolve(ctx context.Context, c *auth.Claims, intent auth.Intent) (*auth.Identity, error) {
	if err := c.Valid(); err != nil {
		return nil, err
	}

	if r.bootstrap == nil {
		return nil, errors.New("pgauth: bootstrap store is required")
	}

	// Authentication is an interactive boundary. Bound the complete database
	// operation, including pool acquisition, so a stale dependency becomes a
	// retryable service failure instead of an unbounded login request.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var identity *auth.Identity
	err := r.bootstrap.WithAuthBootstrapTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		identity, err = r.resolveInTx(ctx, tx, c, intent)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("pgauth: bootstrap transaction: %w", err)
	}
	return identity, nil
}

func (r *Resolver) resolveInTx(
	ctx context.Context,
	tx pgx.Tx,
	c *auth.Claims,
	intent auth.Intent,
) (*auth.Identity, error) {
	// 1. Look up the existing (provider, sub) → user_id without writing.
	userID, found, err := r.findIdentity(ctx, tx, c)
	if err != nil {
		return nil, err
	}
	if found {
		if err := r.touchIdentityLastUsed(ctx, tx, c); err != nil {
			return nil, err
		}
	}

	// 2. Branch on intent to decide provisioning and org membership.
	var orgID uuid.UUID
	var orgRole string
	switch intent := intent.(type) {
	case auth.LoginIntent:
		if !found {
			return nil, auth.ErrNoAccount
		}
		orgID, orgRole, err = r.selectOrg(ctx, tx, userID, c.ProviderOrgID)
	case auth.SignupIntent:
		userID, orgID, orgRole, err = r.resolveSignup(ctx, tx, c, userID, found, intent.OrganizationName)
	case auth.InviteIntent:
		userID, orgID, orgRole, err = r.resolveInvite(ctx, tx, c, userID, found, intent.Token)
	case auth.SsoJitIntent:
		userID, orgID, orgRole, err = r.resolveSsoJit(ctx, tx, c, userID, found, intent.OrgID)
	default:
		return nil, fmt.Errorf("pgauth: unsupported intent %T", intent)
	}
	if err != nil {
		return nil, err
	}

	// 3. Bootstrap admin check — runs for every authentication but only grants
	//    platform role once per deployment.
	platformRole, err := r.bootstrapOrLoadPlatformRole(ctx, tx, userID, c.Email)
	if err != nil {
		return nil, err
	}

	return &auth.Identity{
		UserID:       userID,
		OrgID:        orgID,
		OrgRole:      orgRole,
		PlatformRole: platformRole,
		SessionID:    business.NewID(),
	}, nil
}

// findIdentity resolves an existing user for the given provider claims without
// writing. Returns (userID, found, error); a found identity whose user is not
// active yields ErrAccountInactive.
func (r *Resolver) findIdentity(ctx context.Context, tx pgx.Tx, c *auth.Claims) (uuid.UUID, bool, error) {
	var userID uuid.UUID
	var userStatus string
	err := tx.QueryRow(ctx, `
		SELECT u.uuid, u.status::text
		FROM users u
		JOIN user_identities ui ON ui.user_uuid = u.uuid
		WHERE ui.provider = $1 AND ui.provider_id = $2`,
		c.Provider, c.Subject,
	).Scan(&userID, &userStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("pgauth: lookup identity: %w", err)
	}
	if userStatus != "active" {
		return uuid.Nil, false, auth.ErrAccountInactive
	}
	return userID, true, nil
}

// touchIdentityLastUsed coalesces the operational recency write for an existing
// identity. It lives outside findIdentity so identity lookup stays read-only.
// The value is operational recency, not a per-request audit log; one-minute
// precision is sufficient, while the serializable transaction retry remains the
// safety net at the refresh boundary.
func (r *Resolver) touchIdentityLastUsed(ctx context.Context, tx pgx.Tx, c *auth.Claims) error {
	_, err := tx.Exec(ctx, `
		UPDATE user_identities
		   SET last_used = NOW()
		 WHERE provider = $1 AND provider_id = $2
		   AND (last_used IS NULL OR last_used < NOW() - INTERVAL '1 minute')`,
		c.Provider, c.Subject)
	if err != nil {
		return fmt.Errorf("pgauth: update last_used: %w", err)
	}
	return nil
}

// provisionIdentity creates the users + user_identities rows for a first-seen
// identity. Reachable only from Signup and Invite intents; Login never writes.
func (r *Resolver) provisionIdentity(ctx context.Context, tx pgx.Tx, c *auth.Claims) (uuid.UUID, error) {
	userID := business.NewID()
	identityID := business.NewID()
	email := strings.ToLower(c.Email)

	_, err := tx.Exec(ctx, `
		INSERT INTO users (uuid, primary_email, status, email_verified)
		VALUES ($1, $2, 'active', $3)`,
		userID, email, c.EmailVerified,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("pgauth: insert user: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO user_identities (
			uuid, user_uuid, provider, provider_id,
			provider_email, email_verified, last_used
		) VALUES ($1, $2, $3, $4, $5, $6, NOW())`,
		identityID, userID, c.Provider, c.Subject, email, c.EmailVerified,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("pgauth: insert identity: %w", err)
	}

	return userID, nil
}

// selectOrg deterministically resolves the session organization for a user, in
// strict priority order:
//
//  1. The SSO-asserted organization. When the provider names an org
//     (claims.ProviderOrgID) that we recognise — i.e. an organizations row
//     carries that sso_organization_id — it is authoritative and the user MUST
//     hold a membership there: an asserted org they do not belong to is rejected
//     with ErrOrganizationAccessDenied, never silently downgraded to a
//     heuristic. An org the provider asserts but we have never provisioned is a
//     different case: there is no tenant to reject them from, so resolution
//     falls through to the rules below. This keeps a first-seen SSO signup —
//     whose IdP org has no counterpart in our system yet — from being locked out
//     of its own account.
//  2. The user's persisted default_org_id, when it still names an active
//     membership.
//  3. The sole membership, when the user belongs to exactly one org.
//  4. Otherwise none: (Nil, "", nil), the explicit orgless state
//     (auth.Identity.Orgless) the frontend resolves by asking the user to pick
//     or create an org.
func (r *Resolver) selectOrg(ctx context.Context, tx pgx.Tx, userID uuid.UUID, providerOrgID string) (uuid.UUID, string, error) {
	if providerOrgID != "" {
		// sso_organization_id is UNIQUE (partial index, migration 86), so this
		// resolves to a single tenant. LEFT JOIN so an existing tenant with no
		// membership row still returns the org (with a NULL role), separating
		// "the org exists but this user is not a member" — which must fail
		// closed — from "we have never provisioned this IdP org", where there
		// are no rows at all and resolution falls through to the user's own
		// default.
		var orgID uuid.UUID
		var orgRole *string
		err := tx.QueryRow(ctx, `
			SELECT o.id, m.role
			FROM organizations o
			LEFT JOIN organization_members m ON m.org_id = o.id AND m.user_id = $1
			WHERE o.sso_organization_id = $2`,
			userID, providerOrgID,
		).Scan(&orgID, &orgRole)
		if err == nil {
			if orgRole == nil {
				return uuid.Nil, "", auth.ErrOrganizationAccessDenied
			}
			return orgID, *orgRole, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", fmt.Errorf("pgauth: resolve provider organization: %w", err)
		}
		// Fall through: the asserted org is unknown to us.
	}

	var orgID uuid.UUID
	var orgRole string
	err := tx.QueryRow(ctx, `
		SELECT m.org_id, m.role
		FROM users u
		JOIN organization_members m ON m.org_id = u.default_org_id AND m.user_id = u.uuid
		WHERE u.uuid = $1`,
		userID,
	).Scan(&orgID, &orgRole)
	if err == nil {
		return orgID, orgRole, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", fmt.Errorf("pgauth: load default organization: %w", err)
	}

	// Exactly one membership is unambiguous. The count subquery is a per-row
	// constant, so it yields the single row when the user has one membership and
	// no rows for zero or several — both of which are the orgless state.
	err = tx.QueryRow(ctx, `
		SELECT org_id, role
		FROM organization_members
		WHERE user_id = $1
		  AND (SELECT COUNT(*) FROM organization_members WHERE user_id = $1) = 1`,
		userID,
	).Scan(&orgID, &orgRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", nil
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("pgauth: load sole membership: %w", err)
	}
	return orgID, orgRole, nil
}

// memberRole returns the user's role in a specific org, or fallback when they
// hold no membership row there.
func (r *Resolver) memberRole(ctx context.Context, tx pgx.Tx, orgID, userID uuid.UUID, fallback string) (string, error) {
	var role string
	err := tx.QueryRow(ctx, `
		SELECT role FROM organization_members WHERE org_id = $1 AND user_id = $2`,
		orgID, userID,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return "", fmt.Errorf("pgauth: load member role: %w", err)
	}
	return role, nil
}

// ensureOrg resolves the user's session org deterministically (see selectOrg),
// or provisions a new org on signup when an organization name is supplied and
// the resolution came back orgless. A user with no org and no orgName is a
// legitimate orgless state — the frontend will ask them to create one via the
// normal CreateOrganization RPC.
func (r *Resolver) ensureOrg(
	ctx context.Context,
	tx pgx.Tx,
	userID uuid.UUID,
	providerOrgID string,
	orgName string,
) (uuid.UUID, string, error) {
	orgID, orgRole, err := r.selectOrg(ctx, tx, userID, providerOrgID)
	if err != nil {
		return uuid.Nil, "", err
	}
	if orgID != uuid.Nil {
		return orgID, orgRole, nil
	}
	if orgName == "" {
		return uuid.Nil, "", nil
	}

	orgID = business.NewID()
	slug := slugify(orgName)

	_, err = tx.Exec(ctx, `
		INSERT INTO organizations (id, name, slug, owner_id)
		VALUES ($1, $2, $3, $4)`,
		orgID, orgName, slug, userID,
	)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("pgauth: create org on signup: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO organization_members (org_id, user_id, role)
		VALUES ($1, $2, 'owner')`,
		orgID, userID,
	)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("pgauth: create org membership: %w", err)
	}

	return orgID, "owner", nil
}

// resolveSignup provisions and situates a first-seen identity according to the
// configured SignupMode. An already-existing identity (found) is never gated:
// it resolves like a login through the signup path, so gating never locks out
// established users. Only genuine provisioning of a new identity is gated.
func (r *Resolver) resolveSignup(
	ctx context.Context,
	tx pgx.Tx,
	c *auth.Claims,
	userID uuid.UUID,
	found bool,
	orgName string,
) (uuid.UUID, uuid.UUID, string, error) {
	if !found {
		switch r.signupMode {
		case auth.SignupModeInvite:
			return r.provisionInvitedSignup(ctx, tx, c)
		case auth.SignupModeWaitlist:
			if err := r.requireApprovedWaitlist(ctx, tx, c); err != nil {
				return uuid.Nil, uuid.Nil, "", err
			}
		}
		var err error
		userID, err = r.provisionIdentity(ctx, tx, c)
		if err != nil {
			return uuid.Nil, uuid.Nil, "", err
		}
	}

	orgID, orgRole, err := r.ensureOrg(ctx, tx, userID, c.ProviderOrgID, orgName)
	return userID, orgID, orgRole, err
}

// provisionInvitedSignup authorises a first-seen identity under invite mode.
// Signup is permitted only when a pending, unexpired invitation matches the
// verified email; the invitee is then provisioned and bound to the inviting org,
// exactly as accepting the invitation by token would. A stranger with no
// invitation is rejected before any row is written.
func (r *Resolver) provisionInvitedSignup(
	ctx context.Context,
	tx pgx.Tx,
	c *auth.Claims,
) (uuid.UUID, uuid.UUID, string, error) {
	// Binding an invitation authorises on email equality, so the address must be
	// one the provider verified — otherwise an unverified claim could inherit an
	// organization addressed to an email it has not proven it controls. Checked
	// before the lookup so an unverified caller cannot probe which emails have a
	// pending invitation; the response is indistinguishable from "not invited".
	if !c.EmailVerified {
		return uuid.Nil, uuid.Nil, "", auth.ErrSignupNotAllowed
	}

	var invID, orgID uuid.UUID
	var role string
	// The expiry predicate lives in SQL so a newer but expired invitation cannot
	// shadow an older still-valid one: only unexpired invitations are candidates,
	// and the most recent of those wins. CURRENT_TIMESTAMP also compares against
	// the database clock rather than this process's, matching the rest of the
	// invitation queries.
	err := tx.QueryRow(ctx, `
		SELECT id, org_id, role
		FROM invitations
		WHERE LOWER(email) = LOWER($1) AND status = 'pending'
		  AND expires_at > CURRENT_TIMESTAMP
		ORDER BY created_at DESC
		LIMIT 1
		FOR UPDATE`,
		c.Email,
	).Scan(&invID, &orgID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, "", auth.ErrSignupNotAllowed
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("pgauth: load signup invitation: %w", err)
	}

	userID, err := r.provisionIdentity(ctx, tx, c)
	if err != nil {
		return uuid.Nil, uuid.Nil, "", err
	}
	if err := r.acceptInvitation(ctx, tx, invID, orgID, userID, role); err != nil {
		return uuid.Nil, uuid.Nil, "", err
	}
	return userID, orgID, role, nil
}

// requireApprovedWaitlist authorises a first-seen identity under waitlist mode.
// Signup is permitted only when a waitlist entry for the verified email has been
// approved or invited; pending, rejected, and absent entries are rejected.
//
// It takes the whole Claims rather than a bare email because the authorisation
// turns on email equality: the verification fact must travel with the address,
// so an unverified claim cannot match a waitlist entry it has not proven it owns.
func (r *Resolver) requireApprovedWaitlist(ctx context.Context, tx pgx.Tx, c *auth.Claims) error {
	// Fail closed before the lookup so an unverified caller cannot probe which
	// emails hold an approved waitlist entry.
	if !c.EmailVerified {
		return auth.ErrSignupNotAllowed
	}

	var state string
	err := tx.QueryRow(ctx, `
		SELECT state FROM waitlist_entries
		WHERE normalized_email = LOWER(BTRIM($1))`,
		c.Email,
	).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrSignupNotAllowed
	}
	if err != nil {
		return fmt.Errorf("pgauth: load waitlist state: %w", err)
	}
	if state != "approved" && state != "invited" {
		return auth.ErrSignupNotAllowed
	}
	return nil
}

// acceptInvitation joins userID to the invitation's org with the invitation's
// role and marks the invitation accepted. The membership upsert mirrors
// AddOrgMember: the accepted invitation's role is authoritative, so it wins over
// any pre-existing membership rather than leaving a stale role behind.
func (r *Resolver) acceptInvitation(
	ctx context.Context,
	tx pgx.Tx,
	invID, orgID, userID uuid.UUID,
	role string,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO organization_members (org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = $3`,
		orgID, userID, role,
	); err != nil {
		return fmt.Errorf("pgauth: add invited member: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE invitations
		   SET status = 'accepted', accepted_at = NOW(), accepted_by = $2
		 WHERE id = $1 AND status = 'pending'`,
		invID, userID,
	); err != nil {
		return fmt.Errorf("pgauth: accept invitation: %w", err)
	}
	return nil
}

// resolveInvite authenticates against a pending invitation. Ordering is
// security-relevant: the invitation must be located, be pending and unexpired,
// and match the authenticated email BEFORE any user row is provisioned. Only
// then is the invitee joined to the inviting org and the invitation marked
// accepted — all inside the caller's serializable transaction.
//
// Re-authenticating with a token this same user already redeemed is idempotent.
func (r *Resolver) resolveInvite(
	ctx context.Context,
	tx pgx.Tx,
	c *auth.Claims,
	existingUserID uuid.UUID,
	found bool,
	token string,
) (uuid.UUID, uuid.UUID, string, error) {
	if token == "" {
		return uuid.Nil, uuid.Nil, "", business.ErrInvitationUnavailable
	}

	var invID, orgID uuid.UUID
	var invEmail, role, status string
	var expiresAt time.Time
	var acceptedBy *uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id, org_id, email, role, status, expires_at, accepted_by
		FROM invitations
		WHERE token_hash = $1
		FOR UPDATE`,
		business.HashInvitationToken(token),
	).Scan(&invID, &orgID, &invEmail, &role, &status, &expiresAt, &acceptedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, "", business.ErrInvitationUnavailable
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("pgauth: load invitation: %w", err)
	}

	// Idempotent re-acceptance by the same user returns their membership. Report
	// their current role in the org, which may have changed since acceptance —
	// not the (possibly stale) role the invitation carried.
	if found && status == "accepted" && acceptedBy != nil && *acceptedBy == existingUserID {
		effectiveRole, err := r.memberRole(ctx, tx, orgID, existingUserID, role)
		if err != nil {
			return uuid.Nil, uuid.Nil, "", err
		}
		return existingUserID, orgID, effectiveRole, nil
	}
	if status != "pending" {
		return uuid.Nil, uuid.Nil, "", business.ErrInvitationUnavailable
	}
	if time.Now().After(expiresAt) {
		return uuid.Nil, uuid.Nil, "", business.ErrInvitationExpired
	}
	if !strings.EqualFold(c.Email, invEmail) {
		return uuid.Nil, uuid.Nil, "", business.ErrInvitationEmailMismatch
	}
	// Email equality is only trustworthy when the provider verified the
	// address. An unverified identity may sign in but cannot claim membership
	// addressed to an email it has not proven it controls.
	if !c.EmailVerified {
		return uuid.Nil, uuid.Nil, "", business.ErrInvitationEmailUnverified
	}

	userID := existingUserID
	if !found {
		userID, err = r.provisionIdentity(ctx, tx, c)
		if err != nil {
			return uuid.Nil, uuid.Nil, "", err
		}
	}

	if err := r.acceptInvitation(ctx, tx, invID, orgID, userID, role); err != nil {
		return uuid.Nil, uuid.Nil, "", err
	}

	return userID, orgID, role, nil
}

// ssoProvisioning is an org's JIT provisioning policy, read from the
// organizations row inside the resolution transaction.
type ssoProvisioning struct {
	mode           string
	defaultRole    string
	allowedDomains []string
}

// resolveSsoJit resolves a login through the organization-bound provider for
// orgID. An identity already holding a membership in orgID short-circuits to a
// plain login regardless of the provisioning mode; every other case applies the
// org's policy, which may provision the identity into orgID (jit / invite-only)
// or reject it (disabled, or a policy precondition that fails). It only ever
// writes orgID's membership and never creates an organization.
//
// A known identity that is NOT a member (e.g. removed from the org on our side
// while still present in the customer's IdP) is treated like a first-seen one
// and re-provisioned under the policy. This is deliberate: for an org that
// brings its own IdP the IdP is the source of truth for membership, so
// deprovisioning must happen there — a purely local removal does not stick
// against a still-valid IdP assertion.
func (r *Resolver) resolveSsoJit(
	ctx context.Context,
	tx pgx.Tx,
	c *auth.Claims,
	userID uuid.UUID,
	found bool,
	orgID uuid.UUID,
) (uuid.UUID, uuid.UUID, string, error) {
	if orgID == uuid.Nil {
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("pgauth: sso jit intent without org")
	}

	if found {
		role, isMember, err := r.orgMembership(ctx, tx, orgID, userID)
		if err != nil {
			return uuid.Nil, uuid.Nil, "", err
		}
		if isMember {
			return userID, orgID, role, nil
		}
	}

	policy, err := r.loadSsoProvisioning(ctx, tx, orgID)
	if err != nil {
		return uuid.Nil, uuid.Nil, "", err
	}

	switch policy.mode {
	case "jit":
		return r.provisionSsoJit(ctx, tx, c, userID, found, orgID, policy)
	case "invite-only":
		return r.provisionSsoInvite(ctx, tx, c, userID, found, orgID)
	case "disabled":
		return uuid.Nil, uuid.Nil, "", auth.ErrSsoProvisioningDisabled
	default:
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("pgauth: org %s has no sso provisioning policy", orgID)
	}
}

// provisionSsoJit JIT-provisions a first-seen identity into orgID. The provider
// verified the email (the enterprise IdP asserts it) — an unverified claim is
// refused before the domain check so it cannot probe which domains are allowed,
// mirroring the invitation email-verification rule. The email domain must match
// the org's allowlist; a non-matching email is rejected with a distinct error
// and nothing is written.
//
// An empty allowlist is a misconfiguration, not a policy that admits everyone:
// it is reported with a distinct org-level error so an operator who enabled jit
// mode but never named a trusted domain sees why every login is rejected,
// instead of each user getting the generic "domain not allowed".
func (r *Resolver) provisionSsoJit(
	ctx context.Context,
	tx pgx.Tx,
	c *auth.Claims,
	userID uuid.UUID,
	found bool,
	orgID uuid.UUID,
	policy ssoProvisioning,
) (uuid.UUID, uuid.UUID, string, error) {
	if len(policy.allowedDomains) == 0 {
		return uuid.Nil, uuid.Nil, "", auth.ErrSsoProvisioningMisconfigured
	}
	if !c.EmailVerified {
		return uuid.Nil, uuid.Nil, "", auth.ErrSignupNotAllowed
	}
	if !emailDomainAllowed(c.Email, policy.allowedDomains) {
		return uuid.Nil, uuid.Nil, "", auth.ErrSsoEmailDomainNotAllowed
	}

	if !found {
		var err error
		userID, err = r.provisionIdentity(ctx, tx, c)
		if err != nil {
			return uuid.Nil, uuid.Nil, "", err
		}
	}

	if err := r.joinOrg(ctx, tx, orgID, userID, policy.defaultRole); err != nil {
		return uuid.Nil, uuid.Nil, "", err
	}
	if err := r.emitSsoJitAudit(ctx, tx, c, userID, orgID); err != nil {
		return uuid.Nil, uuid.Nil, "", err
	}
	return userID, orgID, policy.defaultRole, nil
}

// provisionSsoInvite authorises a first-seen identity under invite-only mode.
// Provisioning is permitted only when a pending, unexpired invitation for orgID
// matches the verified email; the invitee is provisioned, bound to orgID with
// the invitation's role, and the invitation consumed. A stranger with no
// invitation is rejected before any row is written.
func (r *Resolver) provisionSsoInvite(
	ctx context.Context,
	tx pgx.Tx,
	c *auth.Claims,
	userID uuid.UUID,
	found bool,
	orgID uuid.UUID,
) (uuid.UUID, uuid.UUID, string, error) {
	if !c.EmailVerified {
		return uuid.Nil, uuid.Nil, "", auth.ErrSignupNotAllowed
	}

	var invID uuid.UUID
	var role string
	err := tx.QueryRow(ctx, `
		SELECT id, role
		FROM invitations
		WHERE org_id = $1 AND LOWER(email) = LOWER($2) AND status = 'pending'
		  AND expires_at > CURRENT_TIMESTAMP
		ORDER BY created_at DESC
		LIMIT 1
		FOR UPDATE`,
		orgID, c.Email,
	).Scan(&invID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, "", auth.ErrSignupNotAllowed
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("pgauth: load sso invitation: %w", err)
	}

	if !found {
		userID, err = r.provisionIdentity(ctx, tx, c)
		if err != nil {
			return uuid.Nil, uuid.Nil, "", err
		}
	}
	if err := r.acceptInvitation(ctx, tx, invID, orgID, userID, role); err != nil {
		return uuid.Nil, uuid.Nil, "", err
	}
	if err := r.emitSsoJitAudit(ctx, tx, c, userID, orgID); err != nil {
		return uuid.Nil, uuid.Nil, "", err
	}
	return userID, orgID, role, nil
}

// orgMembership returns the user's role in orgID and whether a membership row
// exists there.
func (r *Resolver) orgMembership(ctx context.Context, tx pgx.Tx, orgID, userID uuid.UUID) (string, bool, error) {
	var role string
	err := tx.QueryRow(ctx, `
		SELECT role FROM organization_members WHERE org_id = $1 AND user_id = $2`,
		orgID, userID,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("pgauth: load sso membership: %w", err)
	}
	return role, true, nil
}

// joinOrg inserts the membership for a JIT-provisioned identity. A membership
// that already exists (a concurrent first login won the race) is left as-is:
// the caller only reaches this after finding no membership, so the conflict is
// benign and its role is authoritative.
func (r *Resolver) joinOrg(ctx context.Context, tx pgx.Tx, orgID, userID uuid.UUID, role string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO organization_members (org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO NOTHING`,
		orgID, userID, role,
	)
	if err != nil {
		return fmt.Errorf("pgauth: add sso jit member: %w", err)
	}
	return nil
}

// emitSsoJitAudit records a JIT provisioning event inside the resolution
// transaction, so the audit trail commits atomically with the membership it
// describes. The provider and org travel in the event for enterprise-tenant
// forensics.
func (r *Resolver) emitSsoJitAudit(ctx context.Context, tx pgx.Tx, c *auth.Claims, userID, orgID uuid.UUID) error {
	metadata, err := json.Marshal(map[string]string{"provider": c.Provider})
	if err != nil {
		return fmt.Errorf("pgauth: marshal sso jit audit metadata: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_events (
			id, actor_id, actor_type, action, resource, resource_id, org_id, metadata
		) VALUES ($1, $2, 'user', 'auth.sso_jit_provisioned', 'organization', $3, $4, $5::jsonb)`,
		business.NewID(), userID, orgID.String(), orgID, string(metadata),
	)
	if err != nil {
		return fmt.Errorf("pgauth: insert sso jit audit event: %w", err)
	}
	return nil
}

// loadSsoProvisioning reads the org's JIT provisioning policy inside the
// resolution transaction, so the mode a write commits under is the mode read
// under the same serializable snapshot.
func (r *Resolver) loadSsoProvisioning(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (ssoProvisioning, error) {
	var p ssoProvisioning
	var mode *string
	err := tx.QueryRow(ctx, `
		SELECT sso_provision_mode, sso_default_role, sso_allowed_email_domains
		FROM organizations
		WHERE id = $1`,
		orgID,
	).Scan(&mode, &p.defaultRole, &p.allowedDomains)
	if errors.Is(err, pgx.ErrNoRows) {
		return ssoProvisioning{}, auth.ErrOrganizationAccessDenied
	}
	if err != nil {
		return ssoProvisioning{}, fmt.Errorf("pgauth: load sso provisioning policy: %w", err)
	}
	if mode != nil {
		p.mode = *mode
	}
	return p, nil
}

// emailDomainAllowed reports whether the email's domain is in the org's
// allowlist. It fails closed: an email without a parseable domain, or an empty
// allowlist, matches nothing — an org enabling JIT provisioning must name the
// domains it trusts.
func emailDomainAllowed(email string, allowed []string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range allowed {
		if strings.ToLower(strings.TrimSpace(d)) == domain {
			return true
		}
	}
	return false
}

// bootstrapOrLoadPlatformRole checks BOOTSTRAP_ADMIN_EMAIL against the current
// claims email. If it matches and bootstrap_state has not been claimed yet,
// inserts a platform_admins row granting super_admin and stamps
// bootstrap_state. Idempotent: after the first successful call, subsequent
// logins of the same email just load the existing platform role.
func (r *Resolver) bootstrapOrLoadPlatformRole(
	ctx context.Context,
	tx pgx.Tx,
	userID uuid.UUID,
	email string,
) (string, error) {
	// Fast path: load any existing platform role for this user.
	var role string
	err := tx.QueryRow(ctx, `
		SELECT platform_role::text
		FROM platform_admins
		WHERE user_id = $1`,
		userID,
	).Scan(&role)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("pgauth: load platform role: %w", err)
	}
	if role != "" {
		return role, nil
	}

	// Check bootstrap eligibility.
	target := ""
	if r.bootstrapAdminEmail != nil {
		target = *r.bootstrapAdminEmail
	} else {
		target = strings.ToLower(strings.TrimSpace(os.Getenv(BootstrapAdminEmailEnv)))
	}
	if target == "" || target != strings.ToLower(strings.TrimSpace(email)) {
		return "", nil
	}

	// Claim the bootstrap slot atomically.
	var bootstrappedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT bootstrapped_at FROM bootstrap_state WHERE id = 1 FOR UPDATE`,
	).Scan(&bootstrappedAt)
	if err != nil {
		return "", fmt.Errorf("pgauth: load bootstrap state: %w", err)
	}
	if bootstrappedAt != nil {
		// Already claimed by a previous login. This caller does not become
		// super_admin — they get the empty role, same as everyone else.
		return "", nil
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO platform_admins (user_id, platform_role, granted_by, granted_at)
		VALUES ($1, 'super_admin', $1, NOW())`,
		userID,
	)
	if err != nil {
		return "", fmt.Errorf("pgauth: grant bootstrap super_admin: %w", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE bootstrap_state SET bootstrapped_at = NOW() WHERE id = 1`,
	)
	if err != nil {
		return "", fmt.Errorf("pgauth: stamp bootstrap state: %w", err)
	}

	return "super_admin", nil
}

// slugify produces a URL-safe slug from a display name. Minimal
// implementation — lowercases, replaces non-alphanumerics with '-',
// collapses runs. Good enough for a starter.
func slugify(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	lastDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if s == "" {
		return business.NewID().String()
	}
	return s
}

// Compile-time assertion.
var _ auth.IdentityResolver = (*Resolver)(nil)
