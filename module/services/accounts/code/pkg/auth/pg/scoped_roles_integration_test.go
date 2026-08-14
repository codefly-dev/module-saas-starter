package pgauth_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"
	pgauth "accounts/pkg/auth/pg"
)

// These tests cover the scoped-role claim path end to end against Postgres:
// role_assignments.scope resolves into RefreshAuthorization on refresh and
// org-switch, an assignment change revokes live sessions via the migration 88
// trigger, and an over-large grant set fails loudly instead of truncating.

func builtinRoleID(t *testing.T, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	scanControlPlane(t, &id,
		`SELECT id FROM roles WHERE name = $1 AND built_in = true AND org_id IS NULL`, name)
	return id
}

func assignScopedRole(t *testing.T, userID, orgID, roleID uuid.UUID, scope string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
			INSERT INTO role_assignments (subject_id, subject_kind, role_id, org_id, scope)
			VALUES ($1, 'principal', $2, $3, $4)`, userID, roleID, orgID, scope)
		return err
	}))
}

func newMinterOverStore(store auth.SessionStore) *ed25519minter.Minter {
	_, priv, _ := ed25519minter.GenerateKey()
	return ed25519minter.New(ed25519minter.Config{
		Issuer:   "saas-starter-test",
		Audience: "saas-starter-test",
	}, priv, store)
}

func mintIdentitySession(t *testing.T, minter *ed25519minter.Minter, userID, orgID uuid.UUID) *auth.TokenPair {
	t.Helper()
	pair, err := minter.Mint(context.Background(), &auth.Identity{
		UserID: userID,
		OrgID:  orgID,
	})
	require.NoError(t, err)
	return pair
}

// TestScopedRoles_RefreshCarriesScopedRoles proves an access token minted for a
// user with a scoped assignment carries it (resolve -> `sr` claim -> verify).
func TestScopedRoles_RefreshCarriesScopedRoles(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()

	userID := seedUser(t)
	orgID := seedOrganizationMembership(t, userID, "member", time.Now())
	assignScopedRole(t, userID, orgID, builtinRoleID(t, "viewer"), "module-a")
	assignScopedRole(t, userID, orgID, builtinRoleID(t, "editor"), "module-a")
	assignScopedRole(t, userID, orgID, builtinRoleID(t, "admin"), "module-b")

	store := pgauth.NewSessionStore(testStore)
	minter := newMinterOverStore(store)
	pair := mintIdentitySession(t, minter, userID, orgID)

	rotated, err := minter.VerifyRefresh(ctx, pair.RefreshToken)
	require.NoError(t, err)
	identity, err := minter.VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)

	require.Equal(t, map[string][]string{
		"module-a": {"editor", "viewer"},
		"module-b": {"admin"},
	}, identity.ScopedRoles)
}

// TestScopedRoles_OrgSwitchReresolvesTargetOrg proves org-switch re-resolves
// scoped roles for the target org, not the source org.
func TestScopedRoles_OrgSwitchReresolvesTargetOrg(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()

	userID := seedUser(t)
	orgA := seedOrganizationMembership(t, userID, "member", time.Now())
	orgB := seedOrganizationMembership(t, userID, "member", time.Now())
	assignScopedRole(t, userID, orgA, builtinRoleID(t, "viewer"), "module-a")
	assignScopedRole(t, userID, orgB, builtinRoleID(t, "admin"), "module-b")

	store := pgauth.NewSessionStore(testStore)
	minter := newMinterOverStore(store)
	mintIdentitySession(t, minter, userID, orgA)

	sessionID := onlyActiveSessionID(t, userID)
	accessToken, err := minter.SwitchOrganization(ctx, userID, sessionID, orgB)
	require.NoError(t, err)

	identity, err := minter.VerifyAccess(accessToken)
	require.NoError(t, err)
	require.Equal(t, orgB, identity.OrgID)
	require.Equal(t, map[string][]string{"module-b": {"admin"}}, identity.ScopedRoles)
}

// TestScopedRoles_AssignmentRevokesSessions proves migration 88's trigger
// revokes a live session when a scoped assignment changes, and that a
// re-minted token then reflects the new grant.
func TestScopedRoles_AssignmentRevokesSessions(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()

	userID := seedUser(t)
	orgID := seedOrganizationMembership(t, userID, "member", time.Now())

	store := pgauth.NewSessionStore(testStore)
	minter := newMinterOverStore(store)
	pair := mintIdentitySession(t, minter, userID, orgID)

	// Granting a scoped role must revoke the live session.
	assignScopedRole(t, userID, orgID, builtinRoleID(t, "viewer"), "module-a")

	var reason string
	scanControlPlane(t, &reason,
		`SELECT revoked_reason FROM sessions WHERE user_id = $1 AND revoked_at IS NOT NULL`, userID)
	require.Equal(t, "scoped_role_changed", reason)

	// The stale refresh token is dead — the client must re-authenticate.
	_, err := minter.VerifyRefresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshRevoked)

	// A fresh token reflects the new scoped grant.
	fresh := mintIdentitySession(t, minter, userID, orgID)
	rotated, err := minter.VerifyRefresh(ctx, fresh.RefreshToken)
	require.NoError(t, err)
	identity, err := minter.VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"module-a": {"viewer"}}, identity.ScopedRoles)
}

// TestScopedRoles_ExceedingLimitFailsLoudly proves the claim-size bound: a user
// with more than auth.MaxScopedRoleAssignments scoped grants is rejected with a
// defined error rather than a silently truncated, under-authorized token.
func TestScopedRoles_ExceedingLimitFailsLoudly(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()

	userID := seedUser(t)
	orgID := seedOrganizationMembership(t, userID, "member", time.Now())
	roleID := builtinRoleID(t, "viewer")
	for i := 0; i <= auth.MaxScopedRoleAssignments; i++ {
		assignScopedRole(t, userID, orgID, roleID, fmt.Sprintf("module-%03d", i))
	}

	store := pgauth.NewSessionStore(testStore)
	minter := newMinterOverStore(store)
	pair := mintIdentitySession(t, minter, userID, orgID)

	_, err := minter.VerifyRefresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, auth.ErrScopedRolesExceedLimit)
}

func onlyActiveSessionID(t *testing.T, userID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	scanControlPlane(t, &id,
		`SELECT id FROM sessions WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return id
}
