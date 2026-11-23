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

	// The function is the control-plane role's, so the grant is that role's to
	// make. The store's owner may assume it, as its migrations do; only a
	// superuser could grant on a function it does not own.
	asControlPlaneRole := "SET LOCAL ROLE " + infra.ControlPlaneDatabaseRole
	asStoreOwner(t, asControlPlaneRole, "GRANT EXECUTE ON FUNCTION "+signature+" TO "+login)
	t.Cleanup(func() {
		asStoreOwner(t, asControlPlaneRole, "REVOKE EXECUTE ON FUNCTION "+signature+" FROM "+login)
	})

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

	const assumeLogin = "GRANT " + login + " TO CURRENT_USER WITH INHERIT FALSE, SET TRUE"
	const releaseLogin = "REVOKE " + login + " FROM CURRENT_USER"
	for name, tc := range map[string]struct {
		widen, restore []string
		want           string
		// superuser names an arrangement only a superuser can make.
		superuser string
	}{
		"bypasses row-level security": {
			widen:     []string{"ALTER ROLE " + login + " BYPASSRLS"},
			restore:   []string{"ALTER ROLE " + login + " NOBYPASSRLS"},
			want:      login + " (BYPASSRLS)",
			superuser: "give a login BYPASSRLS",
		},
		"reaches the control-plane role": {
			widen:   []string{"GRANT app_control_plane TO " + login},
			restore: []string{"REVOKE app_control_plane FROM " + login},
			want:    "app_control_plane (a role other than the login itself",
		},
		"reaches the tenant role": {
			widen:   []string{"GRANT app_tenant TO " + login},
			restore: []string{"REVOKE app_tenant FROM " + login},
			want:    "app_tenant (a role other than the login itself",
		},
		"owns a schema": {
			// A schema is given to a role its creator may assume, and dropped
			// by its owner; the store's owner is neither until it is granted
			// the login, which it created and so administers.
			widen: []string{
				assumeLogin,
				"CREATE SCHEMA " + schema + " AUTHORIZATION " + login,
				releaseLogin,
			},
			restore: []string{
				assumeLogin,
				"SET LOCAL ROLE " + login,
				"DROP SCHEMA " + schema + " CASCADE",
				"RESET ROLE",
				releaseLogin,
			},
			want: "owns a schema",
		},
		"may write a tenant table": {
			widen:   []string{"GRANT INSERT ON public.organization_members TO " + login},
			restore: []string{"REVOKE INSERT ON public.organization_members FROM " + login},
			want:    "INSERT on table public.organization_members",
		},
		"may write a table with no row-level security": {
			widen:   []string{"GRANT UPDATE ON public.platform_admins TO " + login},
			restore: []string{"REVOKE UPDATE ON public.platform_admins FROM " + login},
			want:    "UPDATE on table public.platform_admins",
		},
		"may read the role catalog": {
			widen:     []string{"GRANT SELECT ON pg_catalog.pg_authid TO " + login},
			restore:   []string{"REVOKE SELECT ON pg_catalog.pg_authid FROM " + login},
			want:      "granted to the login on table pg_catalog.pg_authid",
			superuser: "grant on the system catalog",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.superuser != "" {
				requireSuperuserStoreOwner(t, tc.superuser)
			}
			asStoreOwner(t, tc.widen...)
			t.Cleanup(func() { asStoreOwner(t, tc.restore...) })

			_, err := openWithReaderLogin(t, reader)
			require.ErrorContains(t, err, "reaches beyond a read-only capability")
			require.ErrorContains(t, err, tc.want)
		})
	}
}
