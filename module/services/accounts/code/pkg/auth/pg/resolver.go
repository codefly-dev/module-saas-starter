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
	"github.com/jackc/pgx/v5/pgxpool"

	"accounts/pkg/auth"
	"accounts/pkg/business"
)

// BootstrapAdminEmailEnv is the env var read by Resolver to promote the
// first matching identity to super_admin. Empty disables the feature.
const BootstrapAdminEmailEnv = "BOOTSTRAP_ADMIN_EMAIL"

// Resolver is the production IdentityResolver backed by Postgres.
//
// On every login/signup it:
//  1. Looks up user_identities by (provider, subject).
//  2. If found, loads user_id, primary org, org_role, platform_role.
//  3. If not found, JIT-provisions a users row + user_identities row.
//  4. For signup (orgNameOnSignup != ""), creates an organizations row and
//     org membership as owner if the user doesn't already belong to one.
//  5. Runs the bootstrap check: if BOOTSTRAP_ADMIN_EMAIL matches (case
//     insensitive) and bootstrap_state has no bootstrapped_at, grants
//     super_admin and stamps bootstrap_state. Self-disarms forever.
//  6. Returns an Identity with canonical internal ids ready to be minted
//     into a JWT.
//
// All of the above runs inside a single serializable transaction so
// concurrent first-logins of the same identity converge on a single user row.
type Resolver struct {
	pool *pgxpool.Pool
}

func NewResolver(pool *pgxpool.Pool) *Resolver {
	return &Resolver{pool: pool}
}

// Resolve implements auth.IdentityResolver.
func (r *Resolver) Resolve(ctx context.Context, c *auth.Claims, orgNameOnSignup string) (*auth.Identity, error) {
	if err := c.Valid(); err != nil {
		return nil, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, fmt.Errorf("pgauth: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback on early return is best-effort

	// Auth-flow tx: at this moment we're pre-tenant — the pool's
	// BeforeAcquire put us as app_tenant, but we don't yet have an
	// org context. INSERT INTO organization_members (the JIT-org
	// path below) would fail RLS WITH CHECK. SET LOCAL ROLE NONE
	// elevates to session_user (the codefly superuser) for this tx
	// only; auto-unwinds on commit/rollback.
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE NONE"); err != nil {
		return nil, fmt.Errorf("pgauth: elevate role: %w", err)
	}

	identity, err := r.resolveInTx(ctx, tx, c, orgNameOnSignup)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("pgauth: commit tx: %w", err)
	}
	return identity, nil
}

func (r *Resolver) resolveInTx(
	ctx context.Context,
	tx pgx.Tx,
	c *auth.Claims,
	orgNameOnSignup string,
) (*auth.Identity, error) {
	// 1. Lookup existing (provider, sub) → user_id
	userID, isNew, err := r.upsertIdentity(ctx, tx, c)
	if err != nil {
		return nil, err
	}

	// 2. Make sure the user has an org — either existing, or created now
	//    for signup flows.
	orgID, orgRole, err := r.ensureOrg(ctx, tx, userID, orgNameOnSignup, isNew)
	if err != nil {
		return nil, err
	}

	// 3. Bootstrap admin check — runs for every login but only grants
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

// upsertIdentity finds or creates a user for the given provider claims.
// Returns (userID, wasNewlyProvisioned, error).
func (r *Resolver) upsertIdentity(ctx context.Context, tx pgx.Tx, c *auth.Claims) (uuid.UUID, bool, error) {
	var userID uuid.UUID

	err := tx.QueryRow(ctx, `
		SELECT u.uuid
		FROM users u
		JOIN user_identities ui ON ui.user_uuid = u.uuid
		WHERE ui.provider = $1 AND ui.provider_id = $2`,
		c.Provider, c.Subject,
	).Scan(&userID)

	if err == nil {
		// Existing identity — touch last_used and return.
		_, err = tx.Exec(ctx, `
			UPDATE user_identities
			   SET last_used = NOW()
			 WHERE provider = $1 AND provider_id = $2`,
			c.Provider, c.Subject)
		if err != nil {
			return uuid.Nil, false, fmt.Errorf("pgauth: update last_used: %w", err)
		}
		return userID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("pgauth: lookup identity: %w", err)
	}

	// JIT provision: create users + user_identities rows atomically.
	userID = business.NewID()
	identityID := business.NewID()
	email := strings.ToLower(c.Email)

	_, err = tx.Exec(ctx, `
		INSERT INTO users (uuid, primary_email, status, email_verified)
		VALUES ($1, $2, 'active', true)`,
		userID, email,
	)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("pgauth: insert user: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO user_identities (
			uuid, user_uuid, provider, provider_id,
			provider_email, email_verified
		) VALUES ($1, $2, $3, $4, $5, true)`,
		identityID, userID, c.Provider, c.Subject, email,
	)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("pgauth: insert identity: %w", err)
	}

	return userID, true, nil
}

// ensureOrg either loads the user's existing org membership or provisions a
// new org on signup. If the user has no org and no orgNameOnSignup was
// provided, returns zero values — the caller may still issue a token (the
// Identity will have OrgID = Nil, OrgRole = "") so the user can subsequently
// create an org through the normal API.
func (r *Resolver) ensureOrg(
	ctx context.Context,
	tx pgx.Tx,
	userID uuid.UUID,
	orgNameOnSignup string,
	isNewUser bool,
) (uuid.UUID, string, error) {
	var orgID uuid.UUID
	var orgRole string

	// Most-recent membership wins. A user can be in multiple orgs (e.g.
	// their auto-created "Personal" org from RegisterUser PLUS shared
	// orgs they were added to later); ASC by joined_at would always
	// pick Personal and lock the session into the wrong tenant.
	// DESC reflects "currently active" semantics — the last context the
	// user was added to. A future iteration can replace this with an
	// explicit users.default_org_id column / org switcher.
	err := tx.QueryRow(ctx, `
		SELECT org_id, role
		FROM organization_members
		WHERE user_id = $1
		ORDER BY joined_at DESC
		LIMIT 1`,
		userID,
	).Scan(&orgID, &orgRole)

	if err == nil {
		return orgID, orgRole, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", fmt.Errorf("pgauth: load org membership: %w", err)
	}

	// No org. Create one if this is a signup flow with an explicit org name.
	if orgNameOnSignup == "" {
		if isNewUser {
			// Fine — new user, no org yet. The frontend will ask them to
			// create one via the normal CreateOrganization RPC.
			return uuid.Nil, "", nil
		}
		// Existing user with no org — unusual but not an error.
		return uuid.Nil, "", nil
	}

	orgID = business.NewID()
	slug := slugify(orgNameOnSignup)

	_, err = tx.Exec(ctx, `
		INSERT INTO organizations (id, name, slug, owner_id)
		VALUES ($1, $2, $3, $4)`,
		orgID, orgNameOnSignup, slug, userID,
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
	target := strings.ToLower(strings.TrimSpace(os.Getenv(BootstrapAdminEmailEnv)))
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
