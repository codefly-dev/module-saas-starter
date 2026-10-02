//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/internal/testdb"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"
)

// A request connection is only as tenant-bound as the most powerful role its
// login can reach. Which role the connection happens to be running as proves
// nothing: a login that may assume app_control_plane or a worker role needs one
// statement to leave the tenant filter behind. These tests therefore pin the
// request login itself — everything reachable from session_user through
// pg_auth_members, whatever the membership's INHERIT or SET option — rather than
// the role the connection happens to be running as.

// crossTenantRoles are the application roles whose policies admit rows from
// every tenant. None of them is BYPASSRLS on the store baseline; each sees across
// tenants through explicit `current_user = '<role>'` policies, so "can reach a
// BYPASSRLS role" alone would not catch a request login that reaches one.
var crossTenantRoles = []string{
	"app_control_plane",
	"app_billing_worker",
	"app_webhook_worker",
	"app_job_worker",
}

func requestLogin(t *testing.T) string {
	t.Helper()
	var login string
	require.NoError(t, testPool.QueryRow(testCtx, `SELECT session_user::text`).Scan(&login))
	return login
}

// The request pool's login reaches app_tenant and nothing else, and nothing it
// reaches is a superuser, BYPASSRLS, CREATEROLE, CREATEDB or REPLICATION, or the
// owner of a relation, schema, function or database — any of which would let a
// request session read or rewrite rows the tenant policies filter. The suite
// reads this through the same query startup refuses a request login by.
func TestRequestLoginReachesOnlyTheTenantRole(t *testing.T) {
	authority, err := infra.InspectLoginAuthority(testCtx, testPool)
	require.NoError(t, err)
	require.Equal(t, "app_tenant", authority.CurrentRole, "a request connection must start as app_tenant")
	require.Equal(t, requestLogin(t), authority.Login)
	require.Empty(t, infra.BeyondTenant(authority),
		"request login %q must reach app_tenant and nothing else; cross-tenant work belongs on its own login", authority.Login)

	var got []string
	for _, role := range authority.Reachable {
		got = append(got, role.Name)
	}
	expected := []string{authority.Login, "app_tenant"}
	sort.Strings(got)
	sort.Strings(expected)
	require.Equal(t, expected, got)
}

// A request connection comes back to the pool as app_tenant whatever role
// change its borrower left behind. `SET ROLE NONE` at session level turns the
// connection into the bare login, which holds no table privileges; without the
// release check the next borrower would inherit that until the connection is
// recycled.
func TestRequestConnectionReturnsToTheTenantRoleOnRelease(t *testing.T) {
	conn, err := testPool.Acquire(testCtx)
	require.NoError(t, err)
	pid := conn.Conn().PgConn().PID()
	_, err = conn.Exec(testCtx, "SET ROLE NONE")
	require.NoError(t, err)
	var during string
	require.NoError(t, conn.QueryRow(testCtx, `SELECT current_user::text`).Scan(&during))
	require.NotEqual(t, "app_tenant", during, "the borrower really did leave the tenant role")
	conn.Release()

	require.Eventually(t, func() bool {
		idle := testPool.AcquireAllIdle(testCtx)
		defer func() {
			for _, c := range idle {
				c.Release()
			}
		}()
		found := false
		for _, c := range idle {
			var current string
			require.NoError(t, c.QueryRow(testCtx, `SELECT current_user::text`).Scan(&current))
			require.Equal(t, "app_tenant", current, "an idle request connection must be app_tenant")
			found = found || c.Conn().PgConn().PID() == pid
		}
		return found
	}, 5*time.Second, 50*time.Millisecond, "the released connection returns to the pool as app_tenant")
}

// Startup refuses a control-plane capability whose login cannot assume
// app_control_plane, instead of failing on the first registration or login.
func TestStartupRefusesAControlPlaneLoginWithoutTheControlPlaneRole(t *testing.T) {
	readOnly := storeSecret(t, "read-only-connection")
	readWrite := storeSecret(t, "read-write-connection")
	lacking, err := testdb.CreateLogin(testCtx, "accounts_test_no_control_plane")
	require.NoError(t, err)

	_, err = infra.NewPostgresStoreWithCapabilities(testCtx, readOnly, readWrite, lacking)
	require.ErrorContains(t, err, "cannot assume app_control_plane")
}

// Startup refuses a control-plane capability that authenticates as the reader.
func TestStartupRefusesAControlPlaneLoginSharedWithTheReader(t *testing.T) {
	readOnly := storeSecret(t, "read-only-connection")
	readWrite := storeSecret(t, "read-write-connection")

	_, err := infra.NewPostgresStoreWithCapabilities(testCtx, readOnly, readWrite, readOnly)
	require.ErrorContains(t, err, "read-only and control-plane Postgres capabilities must use distinct logins")
}

func storeSecret(t *testing.T, key string) string {
	t.Helper()
	value, err := codefly.For(testCtx).Service("store").Secret("postgres", key)
	require.NoError(t, err)
	return value
}

// The behavioural half of the same property: on a request connection, asking
// for any cross-tenant role is refused by PostgreSQL itself.
func TestRequestSessionCannotAssumeACrossTenantRole(t *testing.T) {
	for _, role := range crossTenantRoles {
		t.Run(role, func(t *testing.T) {
			tx, err := testPool.Begin(testCtx)
			require.NoError(t, err)
			defer tx.Rollback(testCtx) //nolint:errcheck // the transaction is discarded either way

			_, err = tx.Exec(testCtx, "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize())
			var pgErr *pgconn.PgError
			require.ErrorAsf(t, err, &pgErr, "a request connection assumed %s", role)
			require.Equal(t, "42501", pgErr.Code, role)
		})
	}
}

// Cross-tenant code paths hold their own login: the control plane and every
// worker pool authenticate as a principal distinct from the request login, so
// the request credential is never the one that can reach them.
func TestCrossTenantPathsUseADistinctLogin(t *testing.T) {
	login := requestLogin(t)

	var controlLogin, controlRole string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := storetx.Tx(ctx)
		return tx.QueryRow(ctx, `SELECT session_user::text, current_user::text`).Scan(&controlLogin, &controlRole)
	}))
	require.Equal(t, "app_control_plane", controlRole)
	require.NotEqual(t, login, controlLogin, "control-plane work must not run on the request login")

	for name, open := range map[string]func(context.Context) (*pgxpool.Pool, error){
		"billing":            infra.NewBillingWorkerPool,
		"webhook-projection": infra.NewWebhookProjectionPool,
		"job":                infra.NewJobWorkerPool,
	} {
		t.Run(name, func(t *testing.T) {
			pool, err := open(testCtx)
			require.NoError(t, err)
			defer pool.Close()
			var workerLogin string
			require.NoError(t, pool.QueryRow(testCtx, `SELECT session_user::text`).Scan(&workerLogin))
			require.NotEqual(t, login, workerLogin, "%s worker must not run on the request login", name)
		})
	}
}

// asStoreOwner runs statements as the store's migration owner in one
// transaction, under the lock the Postgres agent takes around its own role and
// grant changes, so a concurrent reconcile cannot collide with them.
func asStoreOwner(t *testing.T, statements ...string) {
	t.Helper()
	conn, err := pgx.Connect(testCtx, storeSecret(t, "owner-connection"))
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // closing a test fixture connection
	tx, err := conn.Begin(testCtx)
	require.NoError(t, err)
	defer tx.Rollback(testCtx) //nolint:errcheck // a no-op after Commit
	_, err = tx.Exec(testCtx, `SELECT pg_advisory_xact_lock(hashtext('codefly-runtime-access:' || current_database()))`)
	require.NoError(t, err)
	for _, statement := range statements {
		_, err := tx.Exec(testCtx, statement)
		require.NoError(t, err, statement)
	}
	require.NoError(t, tx.Commit(testCtx))
}

// openWithRequestLogin opens the production store with request as its request
// capability and the suite's own reader and control-plane capabilities.
func openWithRequestLogin(t *testing.T, request string) (*infra.PostgresStore, error) {
	t.Helper()
	store, err := infra.NewPostgresStoreWithCapabilities(testCtx,
		storeSecret(t, "read-only-connection"), request, storeSecret(t, "control-plane-connection"))
	if err == nil {
		t.Cleanup(store.Close)
	}
	return store, err
}

// A privilege granted to the request login itself, rather than to app_tenant,
// is reachable from any request session: `SET ROLE NONE` drops the session to
// the bare login, and on a table without row-level security — platform_admins,
// say — nothing then filters what it writes. Startup refuses a request login
// holding any table, column, sequence or function privilege app_tenant does not
// hold, and any default privilege that would hand it one on the next migration.
// It refuses the same for the database and its schemas: `CREATE` on the
// database or on a schema lets the bare login place un-RLS'd tables where a
// request session's search_path resolves an unqualified name, and `TEMPORARY`
// does the same through pg_temp, which is searched first. The baseline revokes
// `TEMPORARY` from PUBLIC and app_tenant holds none of these, so each grant
// below is one the login holds alone. `CREATEDB` is refused as an attribute of
// the login, like SUPERUSER or CREATEROLE.
func TestStartupRefusesARequestLoginHoldingItsOwnPrivileges(t *testing.T) {
	const login = "accounts_test_privileged_request"
	const schema = "accounts_test_request_schema"
	request, err := testdb.CreateLogin(testCtx, login, "app_tenant")
	require.NoError(t, err)
	_, err = openWithRequestLogin(t, request)
	require.NoError(t, err, "the login is tenant-bound before any grant")

	var database string
	require.NoError(t, testPool.QueryRow(testCtx, `SELECT current_database()`).Scan(&database))
	onDatabase := " ON DATABASE " + pgx.Identifier{database}.Sanitize() + " "

	for name, tc := range map[string]struct {
		grant, revoke, want string
	}{
		"table": {
			"GRANT INSERT ON public.platform_admins TO " + login,
			"REVOKE ALL ON public.platform_admins FROM " + login,
			"INSERT on table public.platform_admins",
		},
		"column": {
			"GRANT UPDATE (event_type) ON public.audit_events TO " + login,
			"REVOKE ALL (event_type) ON public.audit_events FROM " + login,
			"UPDATE on column public.audit_events.event_type",
		},
		"sequence": {
			"GRANT USAGE ON SEQUENCE public.authorization_revision_sequence TO " + login,
			"REVOKE ALL ON SEQUENCE public.authorization_revision_sequence FROM " + login,
			"USAGE on sequence public.authorization_revision_sequence",
		},
		"function": {
			"GRANT EXECUTE ON FUNCTION public.audit_events_drop_partitions_before(timestamptz) TO " + login,
			"REVOKE ALL ON FUNCTION public.audit_events_drop_partitions_before(timestamptz) FROM " + login,
			"EXECUTE on function public.audit_events_drop_partitions_before",
		},
		"default privilege": {
			"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO " + login,
			"ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM " + login,
			"default SELECT on tables",
		},
		"database create": {
			"GRANT CREATE" + onDatabase + "TO " + login,
			"REVOKE CREATE" + onDatabase + "FROM " + login,
			"CREATE on database",
		},
		"database temporary": {
			"GRANT TEMPORARY" + onDatabase + "TO " + login,
			"REVOKE TEMPORARY" + onDatabase + "FROM " + login,
			"TEMPORARY on database",
		},
		"schema create": {
			"GRANT CREATE ON SCHEMA public TO " + login,
			"REVOKE CREATE ON SCHEMA public FROM " + login,
			"CREATE on schema public",
		},
		"schema usage": {
			"CREATE SCHEMA " + schema + "; GRANT USAGE ON SCHEMA " + schema + " TO " + login,
			"DROP SCHEMA " + schema,
			"USAGE on schema " + schema,
		},
		"createdb": {
			"ALTER ROLE " + login + " CREATEDB",
			"ALTER ROLE " + login + " NOCREATEDB",
			login + " (CREATEDB)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			asStoreOwner(t, tc.grant)
			t.Cleanup(func() { asStoreOwner(t, tc.revoke) })

			_, err := openWithRequestLogin(t, request)
			require.ErrorContains(t, err, "reaches beyond app_tenant")
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// A grant on the system catalog reaches past row-level security as surely as
// one on an application table: EXECUTE on pg_read_file reads the server's files,
// and SELECT on pg_authid or on pg_subscription.subconninfo reads password
// hashes and connection strings, from any session of the login whatever role it
// has selected. So does a parameter privilege: SET on session_replication_role
// switches off every trigger, and ALTER SYSTEM on archive_command runs a server
// command. Startup refuses a request login holding one. Only a superuser can
// grant on the catalog or on a parameter; the local store's owner connection is
// one, which each case checks before relying on its grant.
func TestStartupRefusesARequestLoginHoldingACatalogGrant(t *testing.T) {
	const login = "accounts_test_catalog_request"
	request, err := testdb.CreateLogin(testCtx, login, "app_tenant")
	require.NoError(t, err)
	_, err = openWithRequestLogin(t, request)
	require.NoError(t, err, "the login is tenant-bound before any grant, catalog included")

	for name, tc := range map[string]struct {
		grant, revoke, holds, want string
	}{
		"function": {
			"GRANT EXECUTE ON FUNCTION pg_catalog.pg_read_file(text) TO " + login,
			"REVOKE EXECUTE ON FUNCTION pg_catalog.pg_read_file(text) FROM " + login,
			"SELECT pg_catalog.has_function_privilege($1, 'pg_catalog.pg_read_file(text)', 'EXECUTE')",
			"EXECUTE on function pg_catalog.pg_read_file(",
		},
		"table": {
			"GRANT SELECT ON pg_catalog.pg_authid TO " + login,
			"REVOKE SELECT ON pg_catalog.pg_authid FROM " + login,
			"SELECT pg_catalog.has_table_privilege($1, 'pg_catalog.pg_authid', 'SELECT')",
			"SELECT on table pg_catalog.pg_authid",
		},
		"column": {
			"GRANT SELECT (subconninfo) ON pg_catalog.pg_subscription TO " + login,
			"REVOKE SELECT (subconninfo) ON pg_catalog.pg_subscription FROM " + login,
			"SELECT pg_catalog.has_column_privilege($1, 'pg_catalog.pg_subscription', 'subconninfo', 'SELECT')",
			"SELECT on column pg_catalog.pg_subscription.subconninfo",
		},
		// work_mem is user-settable, so granting SET on it adds nothing a session
		// could not already do; it stands in for the superuser-only parameters a
		// real grant would target. The judgement does not rank parameters: a
		// parameter privilege no declared role holds is refused whatever it names.
		"parameter": {
			"GRANT SET ON PARAMETER work_mem TO " + login,
			"REVOKE SET ON PARAMETER work_mem FROM " + login,
			"SELECT pg_catalog.has_parameter_privilege($1, 'work_mem', 'SET')",
			"SET on parameter work_mem",
		},
	} {
		t.Run(name, func(t *testing.T) {
			asStoreOwner(t, tc.grant)
			t.Cleanup(func() { asStoreOwner(t, tc.revoke) })
			var holds bool
			require.NoError(t, testPool.QueryRow(testCtx, tc.holds, login).Scan(&holds))
			require.True(t, holds, "the grant did not take; granting on the catalog needs a superuser owner connection")

			_, err := openWithRequestLogin(t, request)
			require.ErrorContains(t, err, "reaches beyond app_tenant")
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// Authority is judged on every new request connection, not once at boot: a
// membership granted to the request login after startup, or a session default
// role that no longer applies — PostgreSQL only warns and leaves the session as
// the bare login — refuses the connection instead of serving from it.
func TestRequestPoolJudgesEveryNewConnection(t *testing.T) {
	for name, tc := range map[string]struct {
		change, restore, want string
	}{
		"membership granted after startup": {
			"GRANT app_control_plane TO %s",
			"REVOKE app_control_plane FROM %s",
			"reaches beyond app_tenant",
		},
		"session default role no longer applies": {
			"REVOKE app_tenant FROM %s",
			"GRANT app_tenant TO %s",
			"not app_tenant",
		},
	} {
		t.Run(name, func(t *testing.T) {
			const login = "accounts_test_rejudged_request"
			request, err := testdb.CreateLogin(testCtx, login, "app_tenant")
			require.NoError(t, err)
			store, err := openWithRequestLogin(t, request)
			require.NoError(t, err)

			asStoreOwner(t, fmt.Sprintf(tc.change, login))
			t.Cleanup(func() { asStoreOwner(t, fmt.Sprintf(tc.restore, login)) })

			// Reset closes every pooled connection, so the next query opens a new one.
			store.Pool().Reset()
			_, err = store.Pool().Exec(testCtx, "SELECT 1")
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// The control-plane login may reach the roles the store declares for it and
// nothing else: a stray membership — the request role included — widens every
// cross-tenant path at once, so startup refuses it as it refuses a wider
// request login.
func TestStartupRefusesAControlPlaneLoginReachingAnUndeclaredRole(t *testing.T) {
	readOnly := storeSecret(t, "read-only-connection")
	readWrite := storeSecret(t, "read-write-connection")
	wide, err := testdb.CreateLogin(testCtx, "accounts_test_wide_control_plane",
		"app_control_plane", "app_billing_worker", "app_webhook_worker", "app_job_worker", "app_tenant")
	require.NoError(t, err)

	store, err := infra.NewPostgresStoreWithCapabilities(testCtx, readOnly, readWrite, wide)
	if err == nil {
		store.Close()
	}
	require.ErrorContains(t, err, "control-plane Postgres login")
	require.ErrorContains(t, err, "app_tenant")
}
