//go:build integration

package adapters_test

import (
	"accounts/pkg/business"
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

// This exercises Accounts' own factory and fixed role pools on two real Unix
// sockets. It does not claim a cloud proxy, IAM login or remote TLS proof.
func TestAccountsLocalProxyStore(t *testing.T) {
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (strings.HasPrefix(key, "PG") || key == "POSTGRES_TOKEN_FILE") {
			require.NoError(t, os.Unsetenv(key))
			t.Cleanup(func() { _ = os.Setenv(key, value) })
		}
	}
	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "local-identity-proxy")
	dir, err := os.MkdirTemp("/tmp", "ac-proxy-")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	data, readerSocket, writerSocket := filepath.Join(dir, "data"), filepath.Join(dir, "r"), filepath.Join(dir, "w")
	require.NoError(t, os.Mkdir(readerSocket, 0700))
	require.NoError(t, os.Mkdir(writerSocket, 0700))
	run := func(name string, args ...string) {
		t.Helper()
		output, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("local %s failed: %s", name, output)
		}
	}
	run("initdb", "-D", data, "-U", "fixture_admin", "-A", "trust", "--no-locale")
	run("pg_ctl", "-D", data, "-l", filepath.Join(dir, "postgres.log"), "-o", fmt.Sprintf("-h '' -k %s,%s -p 5432", readerSocket, writerSocket), "-w", "start")
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
	require.Len(t, migrations, 127)
	for _, path := range migrations {
		sql, err := os.ReadFile(path)
		require.NoError(t, err)
		_, err = admin.Exec(ctx, string(sql))
		require.NoError(t, err, filepath.Base(path))
	}
	_, err = admin.Exec(ctx, `CREATE ROLE proxy_reader LOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS;
CREATE ROLE proxy_writer LOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS;
GRANT USAGE ON SCHEMA public TO proxy_reader,proxy_writer;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO proxy_reader;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO proxy_writer;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO proxy_writer;
REVOKE ALL ON execution_custody FROM proxy_reader,proxy_writer;
GRANT app_tenant,app_control_plane,app_job_worker,app_webhook_worker,app_billing_worker TO proxy_writer;`)
	require.NoError(t, err)
	owner, org := uuid.NewString(), uuid.NewString()
	_, err = admin.Exec(ctx, `INSERT INTO users(uuid,primary_email,status) VALUES($1,'proxy@example.invalid','active')`, owner)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, `INSERT INTO organizations(id,name,slug,owner_id) VALUES($1,'Example','proxy-fixture',$2)`, org, owner)
	require.NoError(t, err)
	reader, writer := connection("proxy_reader", readerSocket), connection("proxy_writer", writerSocket)
	for _, entry := range []struct{ dsn, user string }{{reader, "proxy_reader"}, {writer, "proxy_writer"}} {
		pool, err := pgxpool.New(ctx, entry.dsn)
		require.NoError(t, err)
		var user string
		require.NoError(t, pool.QueryRow(ctx, "SELECT current_user").Scan(&user))
		require.Equal(t, entry.user, user)
		_, err = pool.Exec(ctx, "SELECT * FROM execution_custody")
		require.Error(t, err)
		pool.Close()
	}
	open := func() *infra.PostgresStore {
		store, err := infra.NewPostgresStoreWithCapabilities(ctx, reader, writer)
		require.NoError(t, err)
		return store
	}
	store := open()
	record := business.ExecutionCustodyRecord{Reference: uuid.NewString(), OwnerID: owner, OrgID: org, AdmissionID: "proxy-fixture", Fingerprint: strings.Repeat("a", 64), Envelope: "cfs1:vault-transit:non-secret-storage-fixture", ExpiresAt: time.Now().Add(time.Minute).Truncate(time.Microsecond)}
	original, err := store.RegisterExecutionCustody(ctx, record)
	require.NoError(t, err)
	_, err = store.Pool().Exec(ctx, "SELECT * FROM execution_custody")
	require.Error(t, err, "tenant role must not read private custody")
	store.Close()
	store = open()
	defer store.Close()
	recovered, err := store.GetExecutionCustody(ctx, record.Reference)
	require.NoError(t, err)
	require.Equal(t, original.Reference, recovered.Reference)
	require.Equal(t, original.Envelope, recovered.Envelope)
	require.True(t, original.ExpiresAt.Equal(recovered.ExpiresAt))
	_, err = infra.NewPostgresStoreWithCapabilities(ctx, reader, connection("proxy_writer", readerSocket))
	require.Error(t, err, "identity sockets must be distinct")
	for _, makePool := range []func(context.Context, string) (*pgxpool.Pool, error){infra.NewJobWorkerPoolFromURL, infra.NewWebhookProjectionPoolFromURL, infra.NewBillingWorkerPoolFromURL} {
		pool, err := makePool(ctx, writer)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, "SELECT * FROM execution_custody")
		require.Error(t, err, "fixed worker role must not read private custody")
		pool.Close()
	}
	t.Log("Accounts two-socket identities, full migrations, private custody recovery and three fixed worker pools passed")
}
