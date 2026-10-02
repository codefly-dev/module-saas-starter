//go:build !pure

// Package scopedpools qualifies the public production constructor in a disposable
// database. It does not use the service graph or alter its database fixtures.
package scopedpools

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestScopedPoolsIdentityRotationAndClosure(t *testing.T) {
	dsn := os.Getenv("ACCOUNTS_SCOPED_POOL_TEST_DSN")
	if dsn == "" {
		t.Skip("use scripts/qualify-scoped-pools.py for a disposable native PostgreSQL fixture")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", u.Hostname())
	require.NotEmpty(t, u.Port())
	require.Equal(t, "/scoped_pools_fixture", u.Path)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	EnsureStoreBaseline(t, ctx, admin)
	_, err = admin.Exec(ctx, `
 CREATE ROLE scoped_reader LOGIN PASSWORD 'scoped_reader-first';
 CREATE ROLE scoped_writer LOGIN NOINHERIT PASSWORD 'scoped_writer-first';
 CREATE ROLE scoped_control LOGIN NOINHERIT PASSWORD 'scoped_control-first';
 GRANT app_tenant TO scoped_writer;
 ALTER ROLE scoped_writer SET role = 'app_tenant';
 GRANT app_control_plane TO scoped_control;
 ALTER ROLE scoped_control SET role = 'app_control_plane';
 CREATE TABLE organization_members(org_id uuid, user_id uuid, role text, joined_at timestamptz DEFAULT now());
 ALTER TABLE organization_members ENABLE ROW LEVEL SECURITY;
 ALTER TABLE organization_members FORCE ROW LEVEL SECURITY;
 CREATE POLICY request_scope ON organization_members TO scoped_reader,app_tenant
 USING (org_id::text=current_setting('app.current_org_id',true)
    AND user_id::text=current_setting('app.current_user_id',true));
 CREATE POLICY control_scope ON organization_members TO app_control_plane USING (true);
 GRANT SELECT ON organization_members TO scoped_reader,app_tenant,app_control_plane;
 INSERT INTO organization_members(org_id,user_id,role) VALUES
 ('10000000-0000-0000-0000-000000000001','20000000-0000-0000-0000-000000000001','member'),
 ('10000000-0000-0000-0000-000000000002','20000000-0000-0000-0000-000000000001','admin'),
 ('10000000-0000-0000-0000-000000000001','20000000-0000-0000-0000-000000000002','owner');
 `)
	require.NoError(t, err)
	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "")
	dir := t.TempDir()
	paths := map[string]string{}
	rotate := func(principal, token string) {
		t.Helper()
		path := paths[principal]
		require.NoError(t, os.WriteFile(path+".next", []byte(token), 0o600))
		require.NoError(t, os.Rename(path+".next", path))
	}
	for _, principal := range []string{"scoped_reader", "scoped_writer", "scoped_control"} {
		paths[principal] = filepath.Join(dir, principal)
		rotate(principal, principal+"-first")
	}
	raw, err := json.Marshal(paths)
	require.NoError(t, err)
	t.Setenv("POSTGRES_TOKEN_FILE", "")
	t.Setenv("POSTGRES_TOKEN_FILES", string(raw))
	connection := func(principal, label string) string {
		copy := *u
		copy.User = url.User(principal)
		q := copy.Query()
		q.Set("application_name", label)
		q.Set("pool_max_conns", "1")
		copy.RawQuery = q.Encode()
		return copy.String()
	}
	readerDSN, writerDSN := connection("scoped_reader", "scoped_pool_reader"), connection("scoped_writer", "scoped_pool_writer")
	controlDSN := connection("scoped_control", "scoped_pool_control")

	// The request credential must not be the control-plane credential, and must
	// reach nothing but app_tenant; startup refuses either before serving.
	_, err = infra.NewPostgresStoreWithCapabilities(ctx, readerDSN, writerDSN, writerDSN)
	require.ErrorContains(t, err, "distinct logins")
	_, err = admin.Exec(ctx, "GRANT app_control_plane TO scoped_writer")
	require.NoError(t, err)
	_, err = infra.NewPostgresStoreWithCapabilities(ctx, readerDSN, writerDSN, controlDSN)
	require.ErrorContains(t, err, "app_control_plane")
	_, err = admin.Exec(ctx, "REVOKE app_control_plane FROM scoped_writer")
	require.NoError(t, err)

	store, err := infra.NewPostgresStoreWithCapabilities(ctx, readerDSN, writerDSN, controlDSN)
	require.NoError(t, err)
	t.Cleanup(store.Close)
	const orgA = "10000000-0000-0000-0000-000000000001"
	const orgB = "10000000-0000-0000-0000-000000000002"
	const userA = "20000000-0000-0000-0000-000000000001"
	const userB = "20000000-0000-0000-0000-000000000002"
	verified := auth.WithVerifiedDatabaseIdentity(ctx, userA, orgA)
	read := func() error {
		membership, err := store.GetOrgMembership(verified, orgA, userA)
		if err == nil && (membership == nil || membership.OrgId != orgA || membership.UserId != userA) {
			return errors.New("membership scope changed")
		}
		return err
	}
	require.NoError(t, read())
	for _, scope := range [][2]string{{orgB, userA}, {orgA, userB}, {orgA, userA}} {
		membership, err := store.GetOrgMembership(auth.WithVerifiedDatabaseIdentity(ctx, scope[1], scope[0]), scope[0], scope[1])
		require.NoError(t, err)
		require.NotNil(t, membership)
		require.Equal(t, scope[0], membership.OrgId)
		require.Equal(t, scope[1], membership.UserId)
	}
	_, err = store.GetOrgMembership(verified, orgB, userA)
	require.ErrorIs(t, err, auth.ErrVerifiedDatabaseScopeMismatch)
	_, err = store.GetOrgMembership(verified, orgA, userB)
	require.ErrorIs(t, err, auth.ErrVerifiedDatabaseScopeMismatch)
	_, err = store.GetOrgMembership(ctx, orgA, userA)
	require.ErrorIs(t, err, auth.ErrVerifiedDatabaseIdentityRequired)
	// The request pool starts as app_tenant without selecting it, cannot assume
	// the control-plane role, and spans scopes only through the control-plane
	// transaction, which runs on its own login.
	var role string
	require.NoError(t, store.Pool().QueryRow(ctx, "SELECT current_user").Scan(&role))
	require.Equal(t, "app_tenant", role)
	_, err = store.Pool().Exec(ctx, "SET ROLE app_control_plane")
	var denied *pgconn.PgError
	require.ErrorAs(t, err, &denied)
	require.Equal(t, "42501", denied.Code)
	var count int
	require.NoError(t, store.Pool().QueryRow(ctx, "SELECT count(*) FROM organization_members").Scan(&count))
	require.Zero(t, count)
	controlRead := func() error {
		return store.WithControlPlane(ctx, func(txCtx context.Context) error {
			tx := storetx.Tx(txCtx)
			return tx.QueryRow(txCtx, "SELECT count(*) FROM organization_members").Scan(&count)
		})
	}
	require.NoError(t, controlRead())
	require.Equal(t, 3, count)

	// Rotate one identity at a time; the others keep distinct credentials.
	// Destroy all backends so every pool must authenticate again, not just the
	// changed one. A single shared file cannot pass this test.
	for _, principal := range []string{"scoped_reader", "scoped_writer", "scoped_control"} {
		_, err = admin.Exec(ctx, "ALTER ROLE "+pgx.Identifier{principal}.Sanitize()+" PASSWORD '"+principal+"-second'")
		require.NoError(t, err)
		rotate(principal, principal+"-second")
		staleURL := *u
		staleURL.User = url.UserPassword(principal, principal+"-first")
		stale, err := pgx.Connect(ctx, staleURL.String())
		if stale != nil {
			_ = stale.Close(ctx)
		}
		require.Error(t, err, "fixture must reject the old token at PostgreSQL authentication")
		var authenticationError *pgconn.PgError
		require.ErrorAs(t, err, &authenticationError)
		require.Equal(t, "28P01", authenticationError.Code)
		_, err = admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name LIKE 'scoped_pool_%'")
		require.NoError(t, err)
		require.Eventually(t, func() bool { return read() == nil }, 5*time.Second, 20*time.Millisecond)
		require.Eventually(t, func() bool { return controlRead() == nil }, 5*time.Second, 20*time.Millisecond)
		require.Eventually(t, func() bool { return store.Pool().Ping(ctx) == nil }, 5*time.Second, 20*time.Millisecond)
		require.Equal(t, 3, count)
	}
	// A new constructor also proves startup pings authenticate every capability
	// pool with the current token, including the intentionally unused Writer.
	reopened, err := infra.NewPostgresStoreWithCapabilities(ctx, readerDSN, writerDSN, controlDSN)
	require.NoError(t, err)
	reopened.Close()
	reopened.Close()
	store.Close()
	store.Close()
	require.Error(t, read())
	require.Error(t, store.Pool().Ping(ctx))
	assertNoPools := func() {
		t.Helper()
		require.Eventually(t, func() bool {
			var active int
			err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'scoped_pool_%'").Scan(&active)
			return err == nil && active == 0
		}, 5*time.Second, 20*time.Millisecond)
	}
	assertNoPools()
	// A reader opened before a failed writer startup must not survive the error.
	_, err = infra.NewPostgresStoreWithCapabilities(ctx, readerDSN, strings.Replace(writerDSN, "scoped_writer", "missing_writer", 1), controlDSN)
	require.Error(t, err)
	assertNoPools()
}
