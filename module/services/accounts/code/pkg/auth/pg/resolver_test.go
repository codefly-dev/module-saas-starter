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
)

// Reset clears the rows Resolver touches so each test starts from a clean
// state without a full migration reset. Kept narrow on purpose.
func resetAuthTables(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	queries := []string{
		`DELETE FROM sessions`,
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
	return &auth.Claims{
		Provider:  "dev",
		Subject:   sub,
		Email:     email,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
}

func TestResolver_NewUser_JITProvisioning(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("alice@test.local", "dev-alice"), "")
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

func TestResolver_ExistingUser_Idempotent(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	first, err := r.Resolve(ctx, claims("bob@test.local", "dev-bob"), "")
	require.NoError(t, err)

	second, err := r.Resolve(ctx, claims("bob@test.local", "dev-bob"), "")
	require.NoError(t, err)

	require.Equal(t, first.UserID, second.UserID, "same (provider, sub) → same user_id")

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'bob@test.local'`)
	require.Equal(t, 1, count, "no duplicate users created")
}

func TestResolver_ExistingInactiveUserRejected(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	identity, err := r.Resolve(ctx, claims("suspended@test.local", "dev-suspended"), "")
	require.NoError(t, err)
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `UPDATE users SET status = 'suspended' WHERE uuid = $1`, identity.UserID)
		return err
	}))

	_, err = r.Resolve(ctx, claims("suspended@test.local", "dev-suspended"), "")
	require.ErrorIs(t, err, auth.ErrAccountInactive)
}

func TestResolver_Signup_CreatesOrg(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("carol@test.local", "dev-carol"), "Carol's Corp")
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

	id, err := r.Resolve(ctx, claims("dave@test.local", "dev-dave"), "")
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, id.OrgID)
}

func TestResolver_ExistingOrgIsLoaded(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	// Provision user + org
	first, err := r.Resolve(ctx, claims("erin@test.local", "dev-erin"), "Erin Inc")
	require.NoError(t, err)

	// Second login should load the existing org, not create a new one
	second, err := r.Resolve(ctx, claims("erin@test.local", "dev-erin"), "")
	require.NoError(t, err)
	require.Equal(t, first.OrgID, second.OrgID)
	require.Equal(t, "owner", second.OrgRole)
}

func TestResolver_Bootstrap_FirstMatchGetsSuperAdmin(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()

	t.Setenv(pgauth.BootstrapAdminEmailEnv, "boss@test.local")

	r := pgauth.NewResolver(testStore)
	id, err := r.Resolve(ctx, claims("boss@test.local", "dev-boss"), "")
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
	id1, err := r.Resolve(ctx, claims("first@test.local", "dev-first"), "")
	require.NoError(t, err)
	require.Equal(t, "super_admin", id1.PlatformRole)

	// Change env to simulate someone else's email matching later — shouldn't matter
	t.Setenv(pgauth.BootstrapAdminEmailEnv, "second@test.local")

	// Second call for another matching email must NOT grant
	id2, err := r.Resolve(ctx, claims("second@test.local", "dev-second"), "")
	require.NoError(t, err)
	require.Equal(t, "", id2.PlatformRole)

	// And the original super_admin is still super_admin on re-login
	id1Again, err := r.Resolve(ctx, claims("first@test.local", "dev-first"), "")
	require.NoError(t, err)
	require.Equal(t, "super_admin", id1Again.PlatformRole)
	require.Equal(t, id1.UserID, id1Again.UserID)
}

func TestResolver_Bootstrap_NoEnvNoGrant(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()

	os.Unsetenv(pgauth.BootstrapAdminEmailEnv)
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("anyone@test.local", "dev-anyone"), "")
	require.NoError(t, err)
	require.Equal(t, "", id.PlatformRole)
}

func TestResolver_Bootstrap_CaseInsensitiveEmail(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()

	t.Setenv(pgauth.BootstrapAdminEmailEnv, "  Boss@Test.LOCAL  ")
	r := pgauth.NewResolver(testStore)

	id, err := r.Resolve(ctx, claims("BOSS@test.local", "dev-boss-caps"), "")
	require.NoError(t, err)
	require.Equal(t, "super_admin", id.PlatformRole,
		"bootstrap email match must be case-insensitive and whitespace-tolerant")
}

func TestResolver_ConcurrentFirstLogin_OneUser(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	// Fire 10 concurrent first logins for the same identity — must converge
	// on a single user row without deadlocking or duplicating rows.
	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]uuid.UUID, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			id, err := r.Resolve(ctx, claims("race@test.local", "dev-race"), "")
			if id != nil {
				results[i] = id.UserID
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()

	// With SERIALIZABLE isolation, some concurrent transactions may retry
	// or conflict. We accept a small number of retryable failures but need
	// at least one success and zero duplicate users in the end.
	successes := 0
	for i, err := range errs {
		if err == nil {
			successes++
			require.NotEqual(t, uuid.Nil, results[i])
		}
	}
	require.Greater(t, successes, 0, "at least one concurrent resolve must succeed")

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'race@test.local'`)
	require.Equal(t, 1, count, "concurrent first logins must produce exactly one user")
}

func TestResolver_InvalidClaims_Rejected(t *testing.T) {
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	_, err := r.Resolve(ctx, &auth.Claims{Provider: "dev"}, "")
	require.Error(t, err)

	_, err = r.Resolve(ctx, &auth.Claims{Provider: "dev", Subject: "x"}, "")
	require.Error(t, err) // missing email

	_, err = r.Resolve(ctx, nil, "")
	require.Error(t, err)
}

// sanity: compile-time interface assertion
var _ auth.IdentityResolver = (*pgauth.Resolver)(nil)

// helper to generate unique suffix so concurrent packages don't clash
var _ = fmt.Sprintf
