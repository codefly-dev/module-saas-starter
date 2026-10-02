//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/internal/testdb"
	"accounts/pkg/infra"
)

// The scoped reader is the one served pool whose login holds no application
// role: the Postgres agent provisions it with SELECT grants of its own. So the
// "privilege no declared role holds" comparison the request and control-plane
// logins are judged by says nothing here, and the reader is judged against what
// a read-only capability legitimately holds instead.
//
// These run against the actually-provisioned store, so they are the check that
// the policy admits the real profile. The exhaustive matrix of widenings is in
// qualification/scopedpools, on a disposable native PostgreSQL.

// readerConnection is the capability the scoped reader authenticates with.
func readerConnection(t *testing.T) string { return storeSecret(t, "read-only-connection") }

// openWithReaderLogin opens the production store with reader as its read-only
// capability and the suite's own request and control-plane capabilities.
func openWithReaderLogin(t *testing.T, reader string) (*infra.PostgresStore, error) {
	t.Helper()
	store, err := infra.NewPostgresStoreWithCapabilities(testCtx, reader,
		storeSecret(t, "read-write-connection"), storeSecret(t, "control-plane-connection"))
	if err == nil {
		t.Cleanup(store.Close)
	}
	return store, err
}

// The valid-profile positive control. The reader the Postgres agent actually
// provisions holds nothing beyond a read-only capability, so the judgement
// admits the real deployment shape rather than only refusing bad ones. It reads
// this through the same query startup refuses a reader by.
//
// What it must NOT assert is the absence of everything: the provisioned reader
// legitimately holds CONNECT on the database, USAGE on public, SELECT on the
// application relations, a default privilege granting it SELECT on tables
// created later, and — through PUBLIC, like every role in the cluster,
// app_tenant included — EXECUTE on the ordinary functions and SELECT on the
// readable system catalog. A policy that called any of those excess would
// refuse a correctly provisioned reader at startup.
func TestProvisionedReaderHoldsNothingBeyondAReadOnlyCapability(t *testing.T) {
	conn, err := pgx.Connect(testCtx, readerConnection(t))
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // closing a test fixture connection

	authority, err := infra.InspectReaderAuthority(testCtx, conn)
	require.NoError(t, err)
	require.Empty(t, infra.BeyondReadOnly(authority),
		"the provisioned read-only login %q must hold nothing beyond a read-only capability", authority.Login)
	require.Equal(t, authority.Login, authority.CurrentRole,
		"the scoped reader selects no role: the store declares none for it")
	require.Len(t, authority.Reachable, 1, "the reader reaches only itself")
	require.Equal(t, authority.Login, authority.Reachable[0].Name)
	for _, held := range []struct {
		attribute bool
		name      string
	}{
		{authority.Reachable[0].Superuser, "SUPERUSER"},
		{authority.Reachable[0].BypassRLS, "BYPASSRLS"},
		{authority.Reachable[0].CreateRole, "CREATEROLE"},
		{authority.Reachable[0].CreateDB, "CREATEDB"},
		{authority.Reachable[0].Replication, "REPLICATION"},
		{authority.Reachable[0].OwnsRelation, "ownership of a relation"},
		{authority.Reachable[0].OwnsSchema, "ownership of a schema"},
		{authority.Reachable[0].OwnsFunction, "ownership of a function"},
		{authority.Reachable[0].OwnsDatabase, "ownership of the database"},
	} {
		require.Falsef(t, held.attribute, "the provisioned reader must not hold %s", held.name)
	}

	// And the judgement startup applies agrees.
	require.NoError(t, infra.RequireReaderLoginAuthority(testCtx, conn))
}

// A reader that can execute one of the store's own SECURITY DEFINER operations
// holds that function's owner's authority, and 22 of the 30 are owned by
// app_control_plane, whose policies span every tenant. The baseline revokes
// PUBLIC's EXECUTE on all of them and grants the reader none; startup refuses a
// reader that has been granted one.
func TestStartupRefusesAReaderThatCanExecuteAControlPlaneDefiner(t *testing.T) {
	const login = "accounts_test_definer_reader"
	reader, err := testdb.CreateLogin(testCtx, login)
	require.NoError(t, err)
	_, err = openWithReaderLogin(t, reader)
	require.NoError(t, err, "the fixture reader is read-only before the grant")

	// Resolved from the catalog rather than named, so a changed signature cannot
	// silently turn this into a test of nothing.
	var signature string
	require.NoError(t, testPool.QueryRow(testCtx, `
		SELECT proc.oid::regprocedure::text
		FROM pg_catalog.pg_proc proc
		JOIN pg_catalog.pg_namespace namespace ON namespace.oid = proc.pronamespace
		WHERE proc.prosecdef AND namespace.nspname = 'public'
		  AND pg_catalog.pg_get_userbyid(proc.proowner) = $1
		ORDER BY 1 LIMIT 1`, infra.ControlPlaneDatabaseRole).Scan(&signature))
	require.NotEmpty(t, signature)

	asStoreOwner(t, "GRANT EXECUTE ON FUNCTION "+signature+" TO "+login)
	t.Cleanup(func() { asStoreOwner(t, "REVOKE EXECUTE ON FUNCTION "+signature+" FROM "+login) })

	_, err = openWithReaderLogin(t, reader)
	require.ErrorContains(t, err, "reaches beyond a read-only capability")
	require.ErrorContains(t, err, "SECURITY DEFINER function")
	require.ErrorContains(t, err, "which runs as "+infra.ControlPlaneDatabaseRole)
}

// The widenings that matter most against the real schema: an attribute that
// skips row-level security outright, a role the reader could become, ownership,
// and a write on a table the tenant policies filter.
func TestStartupRefusesAWidenedReaderCapability(t *testing.T) {
	const login = "accounts_test_widened_reader"
	const schema = "accounts_test_reader_schema"
	reader, err := testdb.CreateLogin(testCtx, login)
	require.NoError(t, err)
	_, err = openWithReaderLogin(t, reader)
	require.NoError(t, err, "the fixture reader is read-only before any widening")

	for name, tc := range map[string]struct {
		widen, restore, want string
	}{
		"bypasses row-level security": {
			"ALTER ROLE " + login + " BYPASSRLS",
			"ALTER ROLE " + login + " NOBYPASSRLS",
			login + " (BYPASSRLS)",
		},
		"reaches the control-plane role": {
			"GRANT app_control_plane TO " + login,
			"REVOKE app_control_plane FROM " + login,
			"app_control_plane (a role other than the login itself",
		},
		"reaches the tenant role": {
			"GRANT app_tenant TO " + login,
			"REVOKE app_tenant FROM " + login,
			"app_tenant (a role other than the login itself",
		},
		"owns a schema": {
			"CREATE SCHEMA " + schema + " AUTHORIZATION " + login,
			"DROP SCHEMA " + schema + " CASCADE",
			"owns a schema",
		},
		"may write a tenant table": {
			"GRANT INSERT ON public.organization_members TO " + login,
			"REVOKE INSERT ON public.organization_members FROM " + login,
			"INSERT on table public.organization_members",
		},
		"may write a table with no row-level security": {
			"GRANT UPDATE ON public.platform_admins TO " + login,
			"REVOKE UPDATE ON public.platform_admins FROM " + login,
			"UPDATE on table public.platform_admins",
		},
		"may read the role catalog": {
			"GRANT SELECT ON pg_catalog.pg_authid TO " + login,
			"REVOKE SELECT ON pg_catalog.pg_authid FROM " + login,
			"granted to the login on table pg_catalog.pg_authid",
		},
	} {
		t.Run(name, func(t *testing.T) {
			asStoreOwner(t, tc.widen)
			t.Cleanup(func() { asStoreOwner(t, tc.restore) })

			_, err := openWithReaderLogin(t, reader)
			require.ErrorContains(t, err, "reaches beyond a read-only capability")
			require.ErrorContains(t, err, tc.want)
		})
	}
}
