package business_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/gen"
)

// These tests exercise the BOOTSTRAP_ADMIN_EMAIL flow through the
// real service.Authenticate → pg.Resolver chain. The resolver tests
// in pkg/auth/pg already cover the lower-level invariants; these
// assertions prove the end-to-end integration: a login through the
// HTTP-facing Authenticate method produces a super_admin token when
// the email matches, and doesn't when it doesn't.

// wipeBootstrapState ensures each test starts fresh.
func wipeBootstrapState(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	_, err := testStore.Pool().Exec(ctx, `UPDATE bootstrap_state SET bootstrapped_at = NULL WHERE id = 1`)
	require.NoError(t, err)
	_, err = testStore.Pool().Exec(ctx, `TRUNCATE TABLE platform_admins`)
	require.NoError(t, err)
}

func TestBootstrap_FirstMatchingLoginGrantsSuperAdmin(t *testing.T) {
	clearData(t)
	wipeBootstrapState(t)
	t.Setenv(pgauth.BootstrapAdminEmailEnv, "boss@acme.com")

	resp, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider:      "google",
		ProviderId:    "google-boss",
		ProviderEmail: "boss@acme.com",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)

	// Verify bootstrap_state is stamped
	var stamped *string
	err = testStore.Pool().QueryRow(testCtx,
		`SELECT bootstrapped_at::text FROM bootstrap_state WHERE id = 1`,
	).Scan(&stamped)
	require.NoError(t, err)
	require.NotNil(t, stamped, "bootstrap_state must be stamped after first match")

	// Verify platform_admins row exists for the new user. The user_identities
	// subquery is RLS-protected, so read under WithBypass.
	var role string
	require.NoError(t, testStore.WithBypass(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		return tx.QueryRow(ctx, `
			SELECT platform_role::text FROM platform_admins
			WHERE user_id = (SELECT user_uuid FROM user_identities WHERE provider = 'google' AND provider_id = 'google-boss')`,
		).Scan(&role)
	}))
	require.Equal(t, "super_admin", role)
}

func TestBootstrap_WrongEmailGetsNoPlatformRole(t *testing.T) {
	clearData(t)
	wipeBootstrapState(t)
	t.Setenv(pgauth.BootstrapAdminEmailEnv, "boss@acme.com")

	resp, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider:      "google",
		ProviderId:    "google-rando",
		ProviderEmail: "rando@acme.com",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)

	// Verify no platform_admins row for this user
	var count int
	err = testStore.Pool().QueryRow(testCtx, `
		SELECT COUNT(*) FROM platform_admins
		WHERE user_id = (SELECT user_uuid FROM user_identities WHERE provider = 'google' AND provider_id = 'google-rando')`,
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 0, count)

	// bootstrap_state should still be un-stamped
	var stamped *string
	err = testStore.Pool().QueryRow(testCtx,
		`SELECT bootstrapped_at::text FROM bootstrap_state WHERE id = 1`).Scan(&stamped)
	require.NoError(t, err)
	require.Nil(t, stamped, "non-matching email must not stamp bootstrap_state")
}

func TestBootstrap_SecondMatchingLoginIsNotPromoted(t *testing.T) {
	clearData(t)
	wipeBootstrapState(t)
	t.Setenv(pgauth.BootstrapAdminEmailEnv, "boss@acme.com")

	// First matching login grants.
	_, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "google", ProviderId: "google-boss-1", ProviderEmail: "boss@acme.com",
	})
	require.NoError(t, err)

	// Change env so a DIFFERENT email now matches the bootstrap slot —
	// the second login should NOT be granted because the slot is already
	// claimed.
	t.Setenv(pgauth.BootstrapAdminEmailEnv, "new-boss@acme.com")

	_, err = testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "google", ProviderId: "google-new-boss", ProviderEmail: "new-boss@acme.com",
	})
	require.NoError(t, err)

	// Only one platform_admins row exists (the first user).
	var count int
	err = testStore.Pool().QueryRow(testCtx, `
		SELECT COUNT(*) FROM platform_admins WHERE platform_role = 'super_admin'`,
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "bootstrap slot claimed — second matching login must not be promoted")
}
