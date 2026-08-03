package pgauth_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
)

// Reset clears the rows Resolver touches so each test starts from a clean
// state without a full migration reset. Kept narrow on purpose.
func resetAuthTables(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	queries := []string{
		`DELETE FROM sessions`,
		`DELETE FROM invitations`,
		`DELETE FROM platform_admins`,
		`DELETE FROM organization_members`,
		`DELETE FROM organizations`,
		`DELETE FROM user_identities`,
		`DELETE FROM users`,
		`UPDATE bootstrap_state SET bootstrapped_at = NULL WHERE id = 1`,
	}
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		for _, q := range queries {
			if _, err := tx.Exec(ctx, q); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
		return nil
	}))
}

func claims(email, sub string) *auth.Claims {
	c := claimsUnverified(email, sub)
	c.EmailVerified = true
	return c
}

// claimsUnverified builds claims whose provider did not assert a verified
// email — the case a second identity provider makes reachable.
func claimsUnverified(email, sub string) *auth.Claims {
	return &auth.Claims{
		Provider:  "dev",
		Subject:   sub,
		Email:     email,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
}

func userEmailVerified(t *testing.T, userID uuid.UUID) bool {
	t.Helper()
	var verified bool
	scanControlPlane(t, &verified, `SELECT email_verified FROM users WHERE uuid = $1`, userID)
	return verified
}

func identityEmailVerified(t *testing.T, userID uuid.UUID) bool {
	t.Helper()
	var verified bool
	scanControlPlane(t, &verified, `SELECT email_verified FROM user_identities WHERE user_uuid = $1`, userID)
	return verified
}

func claimsWithProviderOrg(email, sub, providerOrgID string) *auth.Claims {
	c := claims(email, sub)
	c.ProviderOrgID = providerOrgID
	return c
}

// seedOrg inserts an organization owned by ownerID, optionally stamped with the
// WorkOS-side organization id (empty leaves sso_organization_id NULL).
func seedOrg(t *testing.T, ownerID uuid.UUID, name, ssoOrgID string) uuid.UUID {
	t.Helper()
	orgID := business.NewID()
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `
			INSERT INTO organizations (id, name, slug, owner_id, sso_organization_id)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''))`,
			orgID, name, fmt.Sprintf("%s-%s", name, orgID), ownerID, ssoOrgID)
		return err
	}))
	return orgID
}

func addMember(t *testing.T, orgID, userID uuid.UUID, role string, joinedAt time.Time) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `
			INSERT INTO organization_members (org_id, user_id, role, joined_at)
			VALUES ($1, $2, $3, $4)`, orgID, userID, role, joinedAt)
		return err
	}))
}

func setDefaultOrg(t *testing.T, userID, orgID uuid.UUID) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `UPDATE users SET default_org_id = $2 WHERE uuid = $1`, userID, orgID)
		return err
	}))
}

// seedInviteOrg creates an inviter and their owned organization, returning
// (inviterID, orgID).
func seedInviteOrg(t *testing.T) (uuid.UUID, uuid.UUID) {
	t.Helper()
	inviterID := seedUser(t)
	orgID := business.NewID()
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		if _, err := tx.Exec(ctx, `
			INSERT INTO organizations (id, name, slug, owner_id)
			VALUES ($1, $2, $3, $4)`,
			orgID, "Invite Org", fmt.Sprintf("invite-org-%s", orgID), inviterID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO organization_members (org_id, user_id, role)
			VALUES ($1, $2, 'owner')`, orgID, inviterID)
		return err
	}))
	return inviterID, orgID
}

// seedInvitation writes an invitation row and returns its plaintext token.
func seedInvitation(t *testing.T, orgID, inviterID uuid.UUID, email, role, status string, expiresAt time.Time) string {
	t.Helper()
	token := business.NewID().String()
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `
			INSERT INTO invitations (id, org_id, inviter_id, email, role, token_hash, status, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			business.NewID(), orgID, inviterID, email, role,
			business.HashInvitationToken(token), status, expiresAt)
		return err
	}))
	return token
}

func TestResolver_Signup_NewUser_Provisioning(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("alice@test.local", "dev-alice"), auth.SignupIntent{})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, id.UserID)
	require.Equal(t, uuid.Nil, id.OrgID, "new user with no signup org has no org")
	require.Equal(t, "", id.OrgRole)
	require.Equal(t, "", id.PlatformRole)
	require.NotEqual(t, uuid.Nil, id.SessionID)

	// Verify rows exist
	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE uuid = $1`, id.UserID)
	require.Equal(t, 1, count)
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM user_identities WHERE user_uuid = $1`, id.UserID)
	require.Equal(t, 1, count)
}

func TestResolver_Login_UnknownIdentity_ReturnsNoAccount(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	_, err := r.Resolve(ctx, claims("stranger@test.local", "dev-stranger"), auth.LoginIntent{})
	require.ErrorIs(t, err, auth.ErrNoAccount)

	// Login must never provision: assert zero rows written.
	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'stranger@test.local'`)
	require.Equal(t, 0, count, "login of an unknown identity must write nothing")
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM user_identities WHERE provider_id = 'dev-stranger'`)
	require.Equal(t, 0, count)
}

func TestResolver_ExistingUser_Idempotent(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	first, err := r.Resolve(ctx, claims("bob@test.local", "dev-bob"), auth.SignupIntent{})
	require.NoError(t, err)

	second, err := r.Resolve(ctx, claims("bob@test.local", "dev-bob"), auth.LoginIntent{})
	require.NoError(t, err)

	require.Equal(t, first.UserID, second.UserID, "same (provider, sub) → same user_id")

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'bob@test.local'`)
	require.Equal(t, 1, count, "no duplicate users created")
}

func TestResolver_LastUsedIsInitializedCoalescedAndRefreshed(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)
	loginClaims := claims("last-used@test.local", "dev-last-used")

	identity, err := r.Resolve(ctx, loginClaims, auth.SignupIntent{})
	require.NoError(t, err)
	initial := identityLastUsed(t, identity.UserID)
	require.WithinDuration(t, time.Now(), initial, 5*time.Second)

	_, err = r.Resolve(ctx, loginClaims, auth.LoginIntent{})
	require.NoError(t, err)
	require.Equal(t, initial, identityLastUsed(t, identity.UserID),
		"immediate repeat authentication must not rewrite the hot identity row")

	stale := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Microsecond)
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx,
			`UPDATE user_identities SET last_used = $2 WHERE user_uuid = $1`,
			identity.UserID,
			stale,
		)
		return err
	}))

	_, err = r.Resolve(ctx, loginClaims, auth.LoginIntent{})
	require.NoError(t, err)
	require.True(t, identityLastUsed(t, identity.UserID).After(stale))
}

func TestResolver_ConcurrentExistingLogin_AllSucceed(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	first, err := r.Resolve(ctx, claims("concurrent@test.local", "dev-concurrent"), auth.SignupIntent{})
	require.NoError(t, err)

	const n = 16
	start := make(chan struct{})
	results := make([]*auth.Identity, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = r.Resolve(
				ctx,
				claims("concurrent@test.local", "dev-concurrent"),
				auth.LoginIntent{},
			)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i], "concurrent authentication %d", i)
		require.NotNil(t, results[i], "concurrent authentication %d", i)
		require.Equal(t, first.UserID, results[i].UserID)
	}
}

func identityLastUsed(t *testing.T, userID uuid.UUID) time.Time {
	t.Helper()
	var lastUsed time.Time
	scanControlPlane(t, &lastUsed,
		`SELECT last_used FROM user_identities WHERE user_uuid = $1`,
		userID,
	)
	return lastUsed
}

func TestResolver_ExistingInactiveUserRejected(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	identity, err := r.Resolve(ctx, claims("suspended@test.local", "dev-suspended"), auth.SignupIntent{})
	require.NoError(t, err)
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `UPDATE users SET status = 'suspended' WHERE uuid = $1`, identity.UserID)
		return err
	}))

	_, err = r.Resolve(ctx, claims("suspended@test.local", "dev-suspended"), auth.LoginIntent{})
	require.ErrorIs(t, err, auth.ErrAccountInactive)
}

func TestResolver_Signup_CreatesOrg(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("carol@test.local", "dev-carol"), auth.SignupIntent{OrganizationName: "Carol's Corp"})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, id.OrgID)
	require.Equal(t, "owner", id.OrgRole)

	// organizations is RLS-protected (Phase 2F). Reading via the
	// raw pool runs as app_tenant with no app.current_org_id set
	// → zero rows. Wrap in WithControlPlane + use the tx from ctx for
	// the assertion read.
	var name, slug string
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with PostgresStore.getQueryExecutor
		return tx.QueryRow(ctx,
			`SELECT name, slug FROM organizations WHERE id = $1`, id.OrgID).Scan(&name, &slug)
	}))
	require.Equal(t, "Carol's Corp", name)
	require.Equal(t, "carol-s-corp", slug)
}

func TestResolver_Signup_NoOrgNameDoesNotCreateOrg(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("dave@test.local", "dev-dave"), auth.SignupIntent{})
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, id.OrgID)
}

func TestResolver_ExistingOrgIsLoaded(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	// Provision user + org
	first, err := r.Resolve(ctx, claims("erin@test.local", "dev-erin"), auth.SignupIntent{OrganizationName: "Erin Inc"})
	require.NoError(t, err)

	// Second login should load the existing org, not create a new one
	second, err := r.Resolve(ctx, claims("erin@test.local", "dev-erin"), auth.LoginIntent{})
	require.NoError(t, err)
	require.Equal(t, first.OrgID, second.OrgID)
	require.Equal(t, "owner", second.OrgRole)
}

func TestResolver_Login_DefaultOrgWins_NotMostRecentMembership(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("multi@test.local", "dev-multi"), auth.SignupIntent{})
	require.NoError(t, err)

	orgA := seedOrg(t, id.UserID, "Org A", "")
	orgB := seedOrg(t, id.UserID, "Org B", "")
	addMember(t, orgA, id.UserID, "owner", time.Now().Add(-48*time.Hour))
	addMember(t, orgB, id.UserID, "member", time.Now()) // most recently joined
	setDefaultOrg(t, id.UserID, orgA)                   // but A is the recorded default

	got, err := r.Resolve(ctx, claims("multi@test.local", "dev-multi"), auth.LoginIntent{})
	require.NoError(t, err)
	require.Equal(t, orgA, got.OrgID, "session lands on default_org_id, not the most recently joined org")
	require.Equal(t, "owner", got.OrgRole)
}

func TestResolver_Login_MultipleMembershipsNoDefault_Orgless(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("ambig@test.local", "dev-ambig"), auth.SignupIntent{})
	require.NoError(t, err)
	orgA := seedOrg(t, id.UserID, "Amb A", "")
	orgB := seedOrg(t, id.UserID, "Amb B", "")
	addMember(t, orgA, id.UserID, "member", time.Now().Add(-time.Hour))
	addMember(t, orgB, id.UserID, "member", time.Now())

	got, err := r.Resolve(ctx, claims("ambig@test.local", "dev-ambig"), auth.LoginIntent{})
	require.NoError(t, err)
	require.True(t, got.Orgless(), "several memberships with no default resolve to an explicit orgless session")
	require.Equal(t, "", got.OrgRole)
}

func TestResolver_Login_SSOProviderOrgWins_OverMoreRecentMembership(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("sso@test.local", "dev-sso"), auth.SignupIntent{})
	require.NoError(t, err)
	ssoOrg := seedOrg(t, id.UserID, "SSO Org", "workos-org-sso")
	otherOrg := seedOrg(t, id.UserID, "Other Org", "")
	addMember(t, ssoOrg, id.UserID, "admin", time.Now().Add(-72*time.Hour))
	addMember(t, otherOrg, id.UserID, "member", time.Now()) // more recent
	setDefaultOrg(t, id.UserID, otherOrg)                   // and even the default points elsewhere

	got, err := r.Resolve(ctx,
		claimsWithProviderOrg("sso@test.local", "dev-sso", "workos-org-sso"),
		auth.LoginIntent{})
	require.NoError(t, err)
	require.Equal(t, ssoOrg, got.OrgID, "the IdP-asserted organization is authoritative for SSO logins")
	require.Equal(t, "admin", got.OrgRole)
}

func TestResolver_Login_SSOProviderOrgNonMember_Rejected(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("outsider@test.local", "dev-outsider"), auth.SignupIntent{})
	require.NoError(t, err)
	memberOrg := seedOrg(t, id.UserID, "Member Org", "")
	addMember(t, memberOrg, id.UserID, "member", time.Now())

	// A different org carries the asserted WorkOS id, but the user is not a member.
	owner := seedUser(t)
	seedOrg(t, owner, "Foreign SSO Org", "workos-org-foreign")

	_, err = r.Resolve(ctx,
		claimsWithProviderOrg("outsider@test.local", "dev-outsider", "workos-org-foreign"),
		auth.LoginIntent{})
	require.ErrorIs(t, err, auth.ErrOrganizationAccessDenied,
		"an asserted org the user does not belong to is rejected, not silently honoured")
}

func TestResolver_Login_SSOProviderOrgUnknown_FallsThroughToDefault(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("firstsso@test.local", "dev-firstsso"), auth.SignupIntent{})
	require.NoError(t, err)
	org := seedOrg(t, id.UserID, "Home Org", "")
	addMember(t, org, id.UserID, "owner", time.Now())
	setDefaultOrg(t, id.UserID, org)

	// The IdP asserts an org id we have never provisioned. There is no tenant
	// to reject the user from, so resolution falls through to their default
	// rather than failing closed — the case that locks a first-seen SSO signup
	// out of its own account.
	got, err := r.Resolve(ctx,
		claimsWithProviderOrg("firstsso@test.local", "dev-firstsso", "workos-org-unprovisioned"),
		auth.LoginIntent{})
	require.NoError(t, err)
	require.Equal(t, org, got.OrgID, "an unknown asserted org falls through to the user's default, not a rejection")
	require.Equal(t, "owner", got.OrgRole)
}

func TestResolver_Login_SingleMembershipNoDefault_ResolvesToIt(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("solo@test.local", "dev-solo"), auth.SignupIntent{})
	require.NoError(t, err)
	org := seedOrg(t, id.UserID, "Solo Org", "")
	addMember(t, org, id.UserID, "owner", time.Now())

	got, err := r.Resolve(ctx, claims("solo@test.local", "dev-solo"), auth.LoginIntent{})
	require.NoError(t, err)
	require.Equal(t, org, got.OrgID, "exactly one membership and no default resolves to that org")
	require.Equal(t, "owner", got.OrgRole)
}

func TestResolver_Login_ZeroMemberships_OrglessSuccess(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	signup, err := r.Resolve(ctx, claims("lonely@test.local", "dev-lonely"), auth.SignupIntent{})
	require.NoError(t, err)
	require.True(t, signup.Orgless())

	got, err := r.Resolve(ctx, claims("lonely@test.local", "dev-lonely"), auth.LoginIntent{})
	require.NoError(t, err)
	require.True(t, got.Orgless(), "a user with no memberships authenticates and reports orgless")
	require.NotEqual(t, uuid.Nil, got.SessionID)
}

func TestResolver_Invite_ProvisionsUserAndMembershipAtomically(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	inviterID, orgID := seedInviteOrg(t)
	token := seedInvitation(t, orgID, inviterID, "invitee@test.local", "admin", "pending", time.Now().Add(24*time.Hour))

	id, err := r.Resolve(ctx, claims("invitee@test.local", "dev-invitee"), auth.InviteIntent{Token: token})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, id.UserID)
	require.Equal(t, orgID, id.OrgID, "invitee lands on the inviting org")
	require.Equal(t, "admin", id.OrgRole, "invitee assumes the invitation's role")

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'invitee@test.local'`)
	require.Equal(t, 1, count, "invitee is provisioned exactly once")
	scanControlPlane(t, &count,
		`SELECT COUNT(*) FROM organization_members WHERE org_id = $1 AND user_id = $2 AND role = 'admin'`,
		orgID, id.UserID)
	require.Equal(t, 1, count, "membership is added in the same transaction")

	var status string
	var acceptedBy uuid.UUID
	scanControlPlane(t, &status, `SELECT status FROM invitations WHERE org_id = $1`, orgID)
	require.Equal(t, "accepted", status)
	scanControlPlane(t, &acceptedBy, `SELECT accepted_by FROM invitations WHERE org_id = $1`, orgID)
	require.Equal(t, id.UserID, acceptedBy)
}

func TestResolver_Invite_EmailMismatchRejectedAndProvisionsNothing(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	inviterID, orgID := seedInviteOrg(t)
	token := seedInvitation(t, orgID, inviterID, "invitee@test.local", "member", "pending", time.Now().Add(24*time.Hour))

	// Authenticate with a different email than the invitation was addressed to.
	_, err := r.Resolve(ctx, claims("attacker@test.local", "dev-attacker"), auth.InviteIntent{Token: token})
	require.ErrorIs(t, err, business.ErrInvitationEmailMismatch)

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'attacker@test.local'`)
	require.Equal(t, 0, count, "a mismatched email must not provision a user")
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM organization_members WHERE org_id = $1`, orgID)
	require.Equal(t, 1, count, "only the inviter remains a member")

	var status string
	scanControlPlane(t, &status, `SELECT status FROM invitations WHERE org_id = $1`, orgID)
	require.Equal(t, "pending", status, "the invitation stays pending")
}

func TestResolver_Invite_UnverifiedEmailRejectedAndProvisionsNothing(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	inviterID, orgID := seedInviteOrg(t)
	token := seedInvitation(t, orgID, inviterID, "invitee@test.local", "admin", "pending", time.Now().Add(24*time.Hour))

	// The email matches the invitation, but the provider did not verify it —
	// acceptance must fail closed so an unverified claim cannot inherit someone
	// else's organization.
	_, err := r.Resolve(ctx, claimsUnverified("invitee@test.local", "dev-invitee"), auth.InviteIntent{Token: token})
	require.ErrorIs(t, err, business.ErrInvitationEmailUnverified)

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'invitee@test.local'`)
	require.Equal(t, 0, count, "an unverified email must not provision a user")
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM organization_members WHERE org_id = $1`, orgID)
	require.Equal(t, 1, count, "only the inviter remains a member")

	var status string
	scanControlPlane(t, &status, `SELECT status FROM invitations WHERE org_id = $1`, orgID)
	require.Equal(t, "pending", status, "the invitation stays pending")
}

func TestResolver_Invite_VerifiedEmailIsAccepted(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	inviterID, orgID := seedInviteOrg(t)
	token := seedInvitation(t, orgID, inviterID, "invitee@test.local", "admin", "pending", time.Now().Add(24*time.Hour))

	id, err := r.Resolve(ctx, claims("invitee@test.local", "dev-invitee"), auth.InviteIntent{Token: token})
	require.NoError(t, err)
	require.Equal(t, orgID, id.OrgID)
	require.Equal(t, "admin", id.OrgRole)
	require.True(t, userEmailVerified(t, id.UserID))
	require.True(t, identityEmailVerified(t, id.UserID))
}

func TestResolver_EmailVerifiedPersistsAndSurvivesRelogin(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	// An unverified signup persists the provider's actual assertion, not a
	// hardcoded true.
	unverified, err := r.Resolve(ctx, claimsUnverified("unverified@test.local", "dev-unverified"), auth.SignupIntent{})
	require.NoError(t, err)
	require.False(t, userEmailVerified(t, unverified.UserID))
	require.False(t, identityEmailVerified(t, unverified.UserID))

	// A verified signup persists true on both tables and keeps it across a
	// later login.
	verified, err := r.Resolve(ctx, claims("verified@test.local", "dev-verified"), auth.SignupIntent{})
	require.NoError(t, err)
	require.True(t, userEmailVerified(t, verified.UserID))
	require.True(t, identityEmailVerified(t, verified.UserID))

	_, err = r.Resolve(ctx, claims("verified@test.local", "dev-verified"), auth.LoginIntent{})
	require.NoError(t, err)
	require.True(t, userEmailVerified(t, verified.UserID), "verification survives re-login")
	require.True(t, identityEmailVerified(t, verified.UserID))
}

func TestResolver_VerifiedUserNotDowngradedByLaterUnverifiedLogin(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	verified, err := r.Resolve(ctx, claims("stable@test.local", "dev-stable"), auth.SignupIntent{})
	require.NoError(t, err)
	require.True(t, userEmailVerified(t, verified.UserID))

	// A later login from the same identity whose provider omits the claim must
	// not clear the recorded verification.
	_, err = r.Resolve(ctx, claimsUnverified("stable@test.local", "dev-stable"), auth.LoginIntent{})
	require.NoError(t, err)
	require.True(t, userEmailVerified(t, verified.UserID), "an omitted claim must not downgrade a verified user")
	require.True(t, identityEmailVerified(t, verified.UserID))
}

func TestResolver_Invite_ExistingMemberRoleUpgraded(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	inviterID, orgID := seedInviteOrg(t)

	// The invitee already exists and is a plain member of the inviting org.
	existing, err := r.Resolve(ctx, claims("promote@test.local", "dev-promote"), auth.SignupIntent{})
	require.NoError(t, err)
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, e := tx.Exec(ctx,
			`INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'member')`,
			orgID, existing.UserID)
		return e
	}))

	token := seedInvitation(t, orgID, inviterID, "promote@test.local", "admin", "pending", time.Now().Add(24*time.Hour))

	id, err := r.Resolve(ctx, claims("promote@test.local", "dev-promote"), auth.InviteIntent{Token: token})
	require.NoError(t, err)
	require.Equal(t, existing.UserID, id.UserID)
	require.Equal(t, orgID, id.OrgID)
	require.Equal(t, "admin", id.OrgRole, "accepting an admin invitation upgrades the returned role")

	var dbRole string
	scanControlPlane(t, &dbRole,
		`SELECT role FROM organization_members WHERE org_id = $1 AND user_id = $2`, orgID, existing.UserID)
	require.Equal(t, "admin", dbRole,
		"the persisted membership role must match the accepted invitation, not stay stale")
}

func TestResolver_Invite_IdempotentReacceptReportsCurrentRole(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	inviterID, orgID := seedInviteOrg(t)
	token := seedInvitation(t, orgID, inviterID, "current@test.local", "member", "pending", time.Now().Add(24*time.Hour))

	joined, err := r.Resolve(ctx, claims("current@test.local", "dev-current"), auth.InviteIntent{Token: token})
	require.NoError(t, err)
	require.Equal(t, "member", joined.OrgRole)

	// Promote the member out of band after acceptance.
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, e := tx.Exec(ctx,
			`UPDATE organization_members SET role = 'owner' WHERE org_id = $1 AND user_id = $2`,
			orgID, joined.UserID)
		return e
	}))

	again, err := r.Resolve(ctx, claims("current@test.local", "dev-current"), auth.InviteIntent{Token: token})
	require.NoError(t, err)
	require.Equal(t, "owner", again.OrgRole,
		"idempotent re-acceptance reports the current membership role, not the invitation's")
}

func TestResolver_Invite_NonPendingFailsClosed(t *testing.T) {
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	cases := []struct {
		name      string
		status    string
		expiresAt time.Time
		wantErr   error
	}{
		{"expired", "pending", time.Now().Add(-time.Hour), business.ErrInvitationExpired},
		{"revoked", "revoked", time.Now().Add(24 * time.Hour), business.ErrInvitationUnavailable},
		{"accepted", "accepted", time.Now().Add(24 * time.Hour), business.ErrInvitationUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAuthTables(t)
			inviterID, orgID := seedInviteOrg(t)
			token := seedInvitation(t, orgID, inviterID, "invitee@test.local", "member", tc.status, tc.expiresAt)

			_, err := r.Resolve(ctx, claims("invitee@test.local", "dev-invitee"), auth.InviteIntent{Token: token})
			require.ErrorIs(t, err, tc.wantErr)

			var count int
			scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'invitee@test.local'`)
			require.Equal(t, 0, count, "a closed invitation must not provision a user")
		})
	}
}

func TestResolver_Invite_UnknownTokenFailsClosed(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	_, err := r.Resolve(ctx, claims("invitee@test.local", "dev-invitee"), auth.InviteIntent{Token: "not-a-real-token"})
	require.ErrorIs(t, err, business.ErrInvitationUnavailable)
}

func TestResolver_Invite_ExistingUserJoinsAndReacceptIsIdempotent(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	// The invitee already has an account (e.g. member of another org).
	existing, err := r.Resolve(ctx, claims("member@test.local", "dev-member"), auth.SignupIntent{})
	require.NoError(t, err)

	inviterID, orgID := seedInviteOrg(t)
	token := seedInvitation(t, orgID, inviterID, "member@test.local", "member", "pending", time.Now().Add(24*time.Hour))

	joined, err := r.Resolve(ctx, claims("member@test.local", "dev-member"), auth.InviteIntent{Token: token})
	require.NoError(t, err)
	require.Equal(t, existing.UserID, joined.UserID, "no second user is provisioned")
	require.Equal(t, orgID, joined.OrgID)

	// Re-authenticating with the redeemed token is idempotent.
	again, err := r.Resolve(ctx, claims("member@test.local", "dev-member"), auth.InviteIntent{Token: token})
	require.NoError(t, err)
	require.Equal(t, existing.UserID, again.UserID)
	require.Equal(t, orgID, again.OrgID)

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM organization_members WHERE org_id = $1 AND user_id = $2`, orgID, existing.UserID)
	require.Equal(t, 1, count, "membership is added exactly once")
}

func TestResolver_Bootstrap_FirstMatchGetsSuperAdmin(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()

	t.Setenv(pgauth.BootstrapAdminEmailEnv, "boss@test.local")

	r := pgauth.NewResolver(testStore)
	id, err := r.Resolve(ctx, claims("boss@test.local", "dev-boss"), auth.SignupIntent{})
	require.NoError(t, err)
	require.Equal(t, "super_admin", id.PlatformRole)

	// bootstrap_state should be stamped
	var stamped *time.Time
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT bootstrapped_at FROM bootstrap_state WHERE id = 1`).Scan(&stamped))
	require.NotNil(t, stamped)

	// platform_admins row exists
	var role string
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT platform_role::text FROM platform_admins WHERE user_id = $1`, id.UserID).Scan(&role))
	require.Equal(t, "super_admin", role)
}

func TestResolver_Bootstrap_SelfDisarms(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()

	t.Setenv(pgauth.BootstrapAdminEmailEnv, "first@test.local")
	r := pgauth.NewResolver(testStore)

	// First call grants
	id1, err := r.Resolve(ctx, claims("first@test.local", "dev-first"), auth.SignupIntent{})
	require.NoError(t, err)
	require.Equal(t, "super_admin", id1.PlatformRole)

	// Change env to simulate someone else's email matching later — shouldn't matter
	t.Setenv(pgauth.BootstrapAdminEmailEnv, "second@test.local")

	// Second call for another matching email must NOT grant
	id2, err := r.Resolve(ctx, claims("second@test.local", "dev-second"), auth.SignupIntent{})
	require.NoError(t, err)
	require.Equal(t, "", id2.PlatformRole)

	// And the original super_admin is still super_admin on re-login
	id1Again, err := r.Resolve(ctx, claims("first@test.local", "dev-first"), auth.LoginIntent{})
	require.NoError(t, err)
	require.Equal(t, "super_admin", id1Again.PlatformRole)
	require.Equal(t, id1.UserID, id1Again.UserID)
}

func TestResolver_Bootstrap_NoEnvNoGrant(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()

	os.Unsetenv(pgauth.BootstrapAdminEmailEnv)
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("anyone@test.local", "dev-anyone"), auth.SignupIntent{})
	require.NoError(t, err)
	require.Equal(t, "", id.PlatformRole)
}

func TestResolver_Bootstrap_CaseInsensitiveEmail(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()

	t.Setenv(pgauth.BootstrapAdminEmailEnv, "  Boss@Test.LOCAL  ")
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("BOSS@test.local", "dev-boss-caps"), auth.SignupIntent{})
	require.NoError(t, err)
	require.Equal(t, "super_admin", id.PlatformRole,
		"bootstrap email match must be case-insensitive and whitespace-tolerant")
}

func TestResolver_ConcurrentFirstSignup_OneUser(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	// Fire 10 concurrent first signups for the same identity — must converge
	// on a single user row without deadlocking or duplicating rows.
	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]uuid.UUID, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			id, err := r.Resolve(ctx, claims("race@test.local", "dev-race"), auth.SignupIntent{})
			if id != nil {
				results[i] = id.UserID
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "concurrent first authentication %d", i)
		require.NotEqual(t, uuid.Nil, results[i])
		require.Equal(t, results[0], results[i], "all requests must resolve the same user")
	}

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'race@test.local'`)
	require.Equal(t, 1, count, "concurrent first signups must produce exactly one user")
}

func TestResolver_InvalidClaims_Rejected(t *testing.T) {
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	_, err := r.Resolve(ctx, &auth.Claims{Provider: "dev"}, auth.LoginIntent{})
	require.Error(t, err)

	_, err = r.Resolve(ctx, &auth.Claims{Provider: "dev", Subject: "x"}, auth.LoginIntent{})
	require.Error(t, err) // missing email

	_, err = r.Resolve(ctx, nil, auth.LoginIntent{})
	require.Error(t, err)
}

// sanity: compile-time interface assertion
var _ auth.IdentityResolver = (*pgauth.Resolver)(nil)

// helper to generate unique suffix so concurrent packages don't clash
var _ = fmt.Sprintf
