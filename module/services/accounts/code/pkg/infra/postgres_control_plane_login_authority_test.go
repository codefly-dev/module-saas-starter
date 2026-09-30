//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/internal/testdb"
	"accounts/pkg/infra"
)

// The control-plane login — which the billing, webhook-projection and job
// worker pools share — reaches app_control_plane and the three worker roles,
// which span tenants through the exact-role policies and grants the migrations
// give them and nothing else. Every new connection on those pools is judged
// against that declared set, the way a request connection is judged against
// app_tenant: an attribute that skips RLS, a role the store does not declare,
// or a privilege the login holds for itself would reach every cross-tenant path
// at once.

// openWithControlPlaneLogin opens the production store with control as its
// control-plane capability and the suite's own reader and request capabilities.
func openWithControlPlaneLogin(t *testing.T, control string) (*infra.PostgresStore, error) {
	t.Helper()
	store, err := infra.NewPostgresStoreWithCapabilities(testCtx,
		storeSecret(t, "read-only-connection"), storeSecret(t, "read-write-connection"), control)
	if err == nil {
		t.Cleanup(store.Close)
	}
	return store, err
}

// Startup refuses a control-plane login that holds, or reaches a role holding,
// an attribute past its declared roles, a role the store does not declare for
// it, or a privilege none of its declared roles holds.
func TestStartupRefusesAControlPlaneLoginBeyondItsDeclaredRoles(t *testing.T) {
	const login = "accounts_test_widened_control_plane"
	control, err := testdb.CreateLogin(testCtx, login, crossTenantRoles...)
	require.NoError(t, err)
	_, err = openWithControlPlaneLogin(t, control)
	require.NoError(t, err, "the login is bounded by its declared roles before any change")

	for name, tc := range map[string]struct {
		change, restore, want string
	}{
		"bypassrls": {
			"ALTER ROLE " + login + " BYPASSRLS",
			"ALTER ROLE " + login + " NOBYPASSRLS",
			login + " (BYPASSRLS)",
		},
		"createdb": {
			"ALTER ROLE " + login + " CREATEDB",
			"ALTER ROLE " + login + " NOCREATEDB",
			login + " (CREATEDB)",
		},
		"undeclared role": {
			"GRANT app_tenant TO " + login,
			"REVOKE app_tenant FROM " + login,
			"app_tenant (a role other than app_control_plane",
		},
		// No declared role may truncate: PostgreSQL applies no row-level
		// security to TRUNCATE.
		"direct table grant": {
			"GRANT TRUNCATE ON public.platform_admins TO " + login,
			"REVOKE ALL ON public.platform_admins FROM " + login,
			"TRUNCATE on table public.platform_admins (held by the login",
		},
	} {
		t.Run(name, func(t *testing.T) {
			asStoreOwner(t, tc.change)
			t.Cleanup(func() { asStoreOwner(t, tc.restore) })

			_, err := openWithControlPlaneLogin(t, control)
			require.ErrorContains(t, err, fmt.Sprintf("control-plane Postgres login %q reaches beyond the roles the store declares for it", login))
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// The control-plane pool judges every new connection, not only the one startup
// opens: a membership granted after startup refuses the next connection.
func TestControlPlanePoolJudgesEveryNewConnection(t *testing.T) {
	const login = "accounts_test_rejudged_control_plane"
	control, err := testdb.CreateLogin(testCtx, login, crossTenantRoles...)
	require.NoError(t, err)
	store, err := openWithControlPlaneLogin(t, control)
	require.NoError(t, err)

	asStoreOwner(t, "GRANT app_tenant TO "+login)
	t.Cleanup(func() { asStoreOwner(t, "REVOKE app_tenant FROM "+login) })

	// Reset closes every pooled connection, so the next transaction opens a new one.
	infra.ControlPlanePool(store).Reset()
	err = store.WithControlPlane(testCtx, func(context.Context) error { return nil })
	require.ErrorContains(t, err, "app_tenant (a role other than app_control_plane")
}

// A worker pool authenticates as the control-plane login and is judged as the
// control-plane pool is, before its PrepareConn selects the worker role.
func TestWorkerPoolRefusesAControlPlaneLoginBeyondItsDeclaredRoles(t *testing.T) {
	const login = "accounts_test_widened_worker"
	control, err := testdb.CreateLogin(testCtx, login, crossTenantRoles...)
	require.NoError(t, err)
	asStoreOwner(t, "GRANT TRUNCATE ON public.platform_admins TO "+login)
	t.Cleanup(func() { asStoreOwner(t, "REVOKE ALL ON public.platform_admins FROM "+login) })

	for name, open := range map[string]func(context.Context, string) (*pgxpool.Pool, error){
		"billing":            infra.NewBillingWorkerPoolFromURL,
		"webhook-projection": infra.NewWebhookProjectionPoolFromURL,
		"job":                infra.NewJobWorkerPoolFromURL,
	} {
		t.Run(name, func(t *testing.T) {
			pool, err := open(testCtx, control)
			if err == nil {
				pool.Close()
			}
			require.ErrorContains(t, err, "TRUNCATE on table public.platform_admins (held by the login")
		})
	}
}

// sessionState is what a pooled connection carries into its next borrower.
func sessionState(t *testing.T, conn *pgxpool.Conn) (role, org, user string) {
	t.Helper()
	require.NoError(t, conn.QueryRow(testCtx, `SELECT current_user::text,
		coalesce(current_setting('app.current_org_id', true), ''),
		coalesce(current_setting('app.current_user_id', true), '')`).Scan(&role, &org, &user))
	return role, org, user
}

// leaveSessionState makes the session-level changes a careless borrower could
// leave on a control-plane or worker connection: a role other than the one the
// pool hands out, and a request binding that outlives the transaction.
func leaveSessionState(t *testing.T, conn *pgxpool.Conn, role string) {
	t.Helper()
	_, err := conn.Exec(testCtx, "SET ROLE "+role)
	require.NoError(t, err)
	_, err = conn.Exec(testCtx, `SELECT set_config('app.current_org_id', $1, false), set_config('app.current_user_id', $2, false)`,
		uuid.NewString(), uuid.NewString())
	require.NoError(t, err)
}

// A released control-plane connection is reset to the role it started as, with
// no request binding, or discarded. Every control-plane transaction assumes
// app_control_plane with SET LOCAL, so a session-level role or binding a
// borrower left behind would otherwise reach the next borrower.
func TestControlPlaneConnectionIsResetOnRelease(t *testing.T) {
	store, err := openWithControlPlaneLogin(t, storeSecret(t, "control-plane-connection"))
	require.NoError(t, err)
	pool := infra.ControlPlanePool(store)

	conn, err := pool.Acquire(testCtx)
	require.NoError(t, err)
	pid := conn.Conn().PgConn().PID()
	started, _, _ := sessionState(t, conn)
	leaveSessionState(t, conn, "app_job_worker")
	role, org, _ := sessionState(t, conn)
	require.Equal(t, "app_job_worker", role, "the borrower really did change the session role")
	require.NotEmpty(t, org, "the borrower really did leave a session-level binding")
	conn.Release()

	require.Eventually(t, func() bool {
		idle := pool.AcquireAllIdle(testCtx)
		defer func() {
			for _, c := range idle {
				c.Release()
			}
		}()
		found := false
		for _, c := range idle {
			role, org, user := sessionState(t, c)
			require.Equal(t, started, role, "an idle control-plane connection must be back at the role it started as")
			require.Empty(t, org, "an idle control-plane connection must carry no org binding")
			require.Empty(t, user, "an idle control-plane connection must carry no user binding")
			found = found || c.Conn().PgConn().PID() == pid
		}
		return found
	}, 5*time.Second, 50*time.Millisecond, "the released connection returns to the pool reset")
}

// A released worker connection is reset the same way, and its next checkout
// still runs as the worker role: PrepareConn selects it again on top of the
// reset.
func TestWorkerConnectionIsResetOnReleaseAndRunsAsItsRoleAgain(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	conn, err := pool.Acquire(testCtx)
	require.NoError(t, err)
	pid := conn.Conn().PgConn().PID()
	role, _, _ := sessionState(t, conn)
	require.Equal(t, "app_job_worker", role)
	leaveSessionState(t, conn, "app_control_plane")
	conn.Release()

	require.Eventually(t, func() bool {
		idle := pool.AcquireAllIdle(testCtx)
		defer func() {
			for _, c := range idle {
				c.Release()
			}
		}()
		found := false
		for _, c := range idle {
			role, org, user := sessionState(t, c)
			require.Equal(t, "app_job_worker", role, "a worker checkout must run as the worker role")
			require.Empty(t, org, "a worker connection must carry no org binding into its next checkout")
			require.Empty(t, user, "a worker connection must carry no user binding into its next checkout")
			found = found || c.Conn().PgConn().PID() == pid
		}
		return found
	}, 5*time.Second, 50*time.Millisecond, "the released worker connection returns to the pool reset")
}
