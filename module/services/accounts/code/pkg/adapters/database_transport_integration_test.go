//go:build integration

package adapters_test

import (
	"accounts/pkg/infra"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// This exercises Accounts' own factory and fixed role pools on three real Unix
// sockets, one per identity. It does not claim a cloud proxy, IAM login or remote TLS proof.
func TestAccountsLocalProxyStore(t *testing.T) {
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (strings.HasPrefix(key, "PG") || key == "POSTGRES_TOKEN_FILE" || key == "POSTGRES_TOKEN_FILES") {
			require.NoError(t, os.Unsetenv(key))
			t.Cleanup(func() { _ = os.Setenv(key, value) })
		}
	}
	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "local-identity-proxy")
	dir, err := os.MkdirTemp("/tmp", "ac-proxy-")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	data, readerSocket, writerSocket := filepath.Join(dir, "data"), filepath.Join(dir, "r"), filepath.Join(dir, "w")
	controlSocket := filepath.Join(dir, "c")
	require.NoError(t, os.Mkdir(readerSocket, 0700))
	require.NoError(t, os.Mkdir(writerSocket, 0700))
	require.NoError(t, os.Mkdir(controlSocket, 0700))
	run := func(name string, args ...string) {
		t.Helper()
		output, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("local %s failed: %s", name, output)
		}
	}
	run("initdb", "-D", data, "-U", "fixture_admin", "-A", "trust", "--no-locale")
	run("pg_ctl", "-D", data, "-l", filepath.Join(dir, "postgres.log"), "-o", fmt.Sprintf("-h '' -k %s,%s,%s -p 5432", readerSocket, writerSocket, controlSocket), "-w", "start")
	defer run("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop")
	connection := func(user, socket string) string {
		return "postgresql://" + user + "@/postgres?" + url.Values{"host": {socket}, "port": {"5432"}, "sslmode": {"disable"}, "passfile": {"/dev/null"}}.Encode()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	admin, err := pgxpool.New(ctx, connection("fixture_admin", writerSocket))
	require.NoError(t, err)
	defer admin.Close()
	migrations, err := filepath.Glob("../../../../store/migrations/*.up.sql")
	require.NoError(t, err)
	sort.Slice(migrations, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.Split(filepath.Base(migrations[i]), "_")[0])
		b, _ := strconv.Atoi(strings.Split(filepath.Base(migrations[j]), "_")[0])
		return a < b
	})
	require.Len(t, migrations, 1)
	for _, path := range migrations {
		sql, err := os.ReadFile(path)
		require.NoError(t, err)
		_, err = admin.Exec(ctx, string(sql))
		require.NoError(t, err, filepath.Base(path))
	}
	// The request writer reaches app_tenant alone and starts there; cross-tenant
	// roles belong to the separate control-plane identity.
	_, err = admin.Exec(ctx, `CREATE ROLE proxy_reader LOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS;
CREATE ROLE proxy_writer LOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS;
CREATE ROLE proxy_control LOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS;
GRANT USAGE ON SCHEMA public TO proxy_reader,proxy_writer,proxy_control;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO proxy_reader;
GRANT app_tenant TO proxy_writer;
ALTER ROLE proxy_writer SET role = 'app_tenant';
GRANT app_control_plane,app_job_worker,app_webhook_worker,app_billing_worker TO proxy_control;
ALTER ROLE proxy_control SET role = 'app_control_plane';`)
	require.NoError(t, err)
	owner, org := uuid.NewString(), uuid.NewString()
	_, err = admin.Exec(ctx, `INSERT INTO users(uuid,primary_email,status) VALUES($1,'proxy@example.invalid','active')`, owner)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, `INSERT INTO organizations(id,name,slug,owner_id) VALUES($1,'Example','proxy-fixture',$2)`, org, owner)
	require.NoError(t, err)
	reader, writer := connection("proxy_reader", readerSocket), connection("proxy_writer", writerSocket)
	control := connection("proxy_control", controlSocket)
	for _, entry := range []struct{ dsn, user string }{{reader, "proxy_reader"}, {writer, "app_tenant"}, {control, "app_control_plane"}} {
		pool, err := pgxpool.New(ctx, entry.dsn)
		require.NoError(t, err)
		var user string
		require.NoError(t, pool.QueryRow(ctx, "SELECT current_user").Scan(&user))
		require.Equal(t, entry.user, user)
		pool.Close()
	}
	open := func() *infra.PostgresStore {
		store, err := infra.NewPostgresStoreWithCapabilities(ctx, reader, writer, control)
		require.NoError(t, err)
		return store
	}
	store := open()
	defer store.Close()
	_, err = infra.NewPostgresStoreWithCapabilities(ctx, reader, connection("proxy_writer", readerSocket), control)
	require.Error(t, err, "identity sockets must be distinct")
	_, err = infra.NewPostgresStoreWithCapabilities(ctx, reader, writer, connection("proxy_control", writerSocket))
	require.Error(t, err, "the control-plane identity needs a socket of its own")
	for _, makePool := range []func(context.Context, string) (*pgxpool.Pool, error){infra.NewJobWorkerPoolFromURL, infra.NewWebhookProjectionPoolFromURL, infra.NewBillingWorkerPoolFromURL} {
		pool, err := makePool(ctx, control)
		require.NoError(t, err)
		pool.Close()
	}
	t.Log("Accounts three-socket identities, full migrations and three fixed worker pools passed")
}
