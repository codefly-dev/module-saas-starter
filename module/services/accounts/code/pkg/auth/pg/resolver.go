package pgauth

import (
	"context"
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
}

// BootstrapStore is the narrow pre-auth database capability. It deliberately
// exposes neither a raw pool nor connection credentials; the composition root
// owns the audited role transition and bounds the capability to one
// serializable transaction callback.
type BootstrapStore interface {
	WithAuthBootstrapTx(context.Context, func(context.Context, pgx.Tx) error) error
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
		orgID, orgRole, err = r.loadOrg(ctx, tx, userID)
	case auth.SignupIntent:
		if !found {
			userID, err = r.provisionIdentity(ctx, tx, c)
			if err != nil {
				return nil, err
			}
		}
		orgID, orgRole, err = r.ensureOrg(ctx, tx, userID, intent.OrganizationName)
	case auth.InviteIntent:
		userID, orgID, orgRole, err = r.resolveInvite(ctx, tx, c, userID, found, intent.Token)
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

// loadOrg returns the user's active org membership, or (Nil, "", nil) when they
// belong to none.
//
// Most-recent membership wins. A user can be in multiple orgs (e.g. their
// auto-created "Personal" org from RegisterUser PLUS shared orgs they were
// added to later); ASC by joined_at would always pick Personal and lock the
// session into the wrong tenant. DESC reflects "currently active" semantics —
// the last context the user was added to. A future iteration can replace this
// with an explicit users.default_org_id column / org switcher.
func (r *Resolver) loadOrg(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (uuid.UUID, string, error) {
	var orgID uuid.UUID
	var orgRole string
	err := tx.QueryRow(ctx, `
		SELECT org_id, role
		FROM organization_members
		WHERE user_id = $1
		ORDER BY joined_at DESC
		LIMIT 1`,
		userID,
	).Scan(&orgID, &orgRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", nil
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("pgauth: load org membership: %w", err)
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

// ensureOrg loads the user's active org, or provisions a new org on signup when
// an organization name is supplied and the user has none. A user with no org
// and no orgName is a legitimate orgless state — the frontend will ask them to
// create one via the normal CreateOrganization RPC.
func (r *Resolver) ensureOrg(
	ctx context.Context,
	tx pgx.Tx,
	userID uuid.UUID,
	orgName string,
) (uuid.UUID, string, error) {
	orgID, orgRole, err := r.loadOrg(ctx, tx, userID)
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

	// Mirror AddOrgMember: the accepted invitation's role is authoritative, so
	// upsert it on conflict rather than leaving an existing membership's role
	// behind. Otherwise the role returned below (and minted into the JWT) could
	// disagree with the persisted membership.
	if _, err := tx.Exec(ctx, `
		INSERT INTO organization_members (org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = $3`,
		orgID, userID, role,
	); err != nil {
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("pgauth: add invited member: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE invitations
		   SET status = 'accepted', accepted_at = NOW(), accepted_by = $2
		 WHERE id = $1 AND status = 'pending'`,
		invID, userID,
	); err != nil {
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("pgauth: accept invitation: %w", err)
	}

	return userID, orgID, role, nil
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
