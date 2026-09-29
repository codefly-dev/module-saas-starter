//go:build !pure

package scopedpools

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"accounts/pkg/infra"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// The scoped reader is authenticated, but authentication says nothing about
// authority: the pool's login is provisioned by the deployment, and a reader
// that holds BYPASSRLS, owns an object, or has been granted more than its reads
// returns rows the tenant policies were supposed to filter. These cases drive
// the public production constructor against a disposable native PostgreSQL, one
// widened reader at a time, and require it to refuse before it serves.
//
// A correctly provisioned reader is the positive control: every case re-opens
// the store with the unwidened reader first, so a refusal proves the widening
// was what the judgement caught rather than the fixture being unopenable.

const (
	readerOrgA  = "30000000-0000-0000-0000-000000000001"
	readerOrgB  = "30000000-0000-0000-0000-000000000002"
	readerUserA = "40000000-0000-0000-0000-000000000001"
)

// EnsureStoreBaseline provisions, idempotently, the parts of the store baseline
// both qualification fixtures depend on. Two tests share one disposable
// database, so neither may create a shared role outright.
//
// The four cross-tenant roles must all exist: accounts names them as the scoped
// pools' denied roles, and the library's restricted-session policy fails closed
// on a denied role it cannot find, so a fixture missing one refuses every
// capability rather than admitting it. TEMPORARY is revoked from PUBLIC as the
// store baseline revokes it — PostgreSQL grants it to PUBLIC by default, and
// pg_temp is searched before every other schema, so a reader holding it could
// shadow the unqualified names its own queries resolve.
func EnsureStoreBaseline(t *testing.T, ctx context.Context, admin *pgx.Conn) {
	t.Helper()
	_, err := admin.Exec(ctx, `
 DO $$
 DECLARE role_name text;
 BEGIN
   FOREACH role_name IN ARRAY ARRAY['app_tenant','app_control_plane','app_billing_worker','app_webhook_worker','app_job_worker'] LOOP
     IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
       EXECUTE format('CREATE ROLE %I NOLOGIN', role_name);
     END IF;
   END LOOP;
 END $$;
 REVOKE TEMPORARY ON DATABASE scoped_pools_fixture FROM PUBLIC;`)
	require.NoError(t, err)
}

// readerFixture is a two-tenant table under forced row-level security, with a
// reader, a request and a control-plane login provisioned as the Postgres agent
// provisions them — and, as the store baseline does, with TEMPORARY revoked
// from PUBLIC so pg_temp cannot shadow an unqualified name.
type readerFixture struct {
	admin                            *pgx.Conn
	base                             *url.URL
	readerDSN, writerDSN, controlDSN string
}

func newReaderFixture(t *testing.T, ctx context.Context) readerFixture {
	t.Helper()
	dsn := os.Getenv("ACCOUNTS_SCOPED_POOL_TEST_DSN")
	if dsn == "" {
		t.Skip("use scripts/qualify-scoped-pools.py for a disposable native PostgreSQL fixture")
	}
	base, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", base.Hostname())
	require.Equal(t, "/scoped_pools_fixture", base.Path)

	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	EnsureStoreBaseline(t, ctx, admin)
	_, err = admin.Exec(ctx, `
 DO $$ BEGIN
   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='ra_reader') THEN CREATE ROLE ra_reader LOGIN NOINHERIT PASSWORD 'ra_reader-pw'; END IF;
   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='ra_writer') THEN CREATE ROLE ra_writer LOGIN NOINHERIT PASSWORD 'ra_writer-pw'; END IF;
   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='ra_control') THEN CREATE ROLE ra_control LOGIN NOINHERIT PASSWORD 'ra_control-pw'; END IF;
 END $$;
 GRANT app_tenant TO ra_writer;
 ALTER ROLE ra_writer SET role = 'app_tenant';
 GRANT app_control_plane TO ra_control;
 ALTER ROLE ra_control SET role = 'app_control_plane';
 DROP TABLE IF EXISTS reader_rows;
 CREATE TABLE reader_rows(org_id uuid, user_id uuid, secret text);
 ALTER TABLE reader_rows ENABLE ROW LEVEL SECURITY;
 ALTER TABLE reader_rows FORCE ROW LEVEL SECURITY;
 CREATE POLICY reader_scope ON reader_rows TO ra_reader, app_tenant
   USING (org_id::text = current_setting('app.current_org_id', true));
 CREATE POLICY reader_control ON reader_rows TO app_control_plane USING (true);
 GRANT SELECT ON reader_rows TO ra_reader, app_tenant, app_control_plane;
 INSERT INTO reader_rows(org_id,user_id,secret) VALUES
   ('`+readerOrgA+`','`+readerUserA+`','tenant-a-secret'),
   ('`+readerOrgB+`','`+readerUserA+`','tenant-b-secret');
 `)
	require.NoError(t, err)

	connection := func(principal string) string {
		copied := *base
		copied.User = url.UserPassword(principal, principal+"-pw")
		query := copied.Query()
		query.Set("application_name", "reader_authority_"+principal)
		query.Set("pool_max_conns", "1")
		copied.RawQuery = query.Encode()
		return copied.String()
	}
	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "")
	t.Setenv("POSTGRES_TOKEN_FILE", "")
	t.Setenv("POSTGRES_TOKEN_FILES", "")
	return readerFixture{
		admin: admin, base: base,
		readerDSN:  connection("ra_reader"),
		writerDSN:  connection("ra_writer"),
		controlDSN: connection("ra_control"),
	}
}

func (f readerFixture) open(t *testing.T, ctx context.Context) (*infra.PostgresStore, error) {
	t.Helper()
	store, err := infra.NewPostgresStoreWithCapabilities(ctx, f.readerDSN, f.writerDSN, f.controlDSN)
	if err == nil {
		t.Cleanup(store.Close)
	}
	return store, err
}

func (f readerFixture) exec(t *testing.T, ctx context.Context, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		_, err := f.admin.Exec(ctx, statement)
		require.NoError(t, err, statement)
	}
}

// A reader holding BYPASSRLS reads every tenant's rows whatever scope is bound,
// which is the defect this judgement exists for. The first half demonstrates
// that on the database itself — the same reader credential, the same forced
// policies, the production scope setting bound — and the second half requires
// the production constructor to refuse to open a pool on it.
func TestScopedReaderWithBypassRLSIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture := newReaderFixture(t, ctx)

	store, err := fixture.open(t, ctx)
	require.NoError(t, err, "the unwidened reader is the positive control")
	store.Close()

	fixture.exec(t, ctx, "ALTER ROLE ra_reader BYPASSRLS")
	t.Cleanup(func() { fixture.exec(t, context.Background(), "ALTER ROLE ra_reader NOBYPASSRLS") })

	// What the widened credential can actually read, bound to tenant A only.
	rawReader := *fixture.base
	rawReader.User = url.UserPassword("ra_reader", "ra_reader-pw")
	widened, err := pgx.Connect(ctx, rawReader.String())
	require.NoError(t, err)
	defer widened.Close(context.Background()) //nolint:errcheck
	_, err = widened.Exec(ctx, `SELECT set_config('app.current_org_id', $1, false)`, readerOrgA)
	require.NoError(t, err)
	var visible int
	require.NoError(t, widened.QueryRow(ctx, `SELECT count(*) FROM reader_rows`).Scan(&visible))
	require.Equal(t, 2, visible, "a BYPASSRLS reader bound to one tenant reads both: the defect being fixed")

	_, err = fixture.open(t, ctx)
	require.Error(t, err, "the production constructor must refuse a BYPASSRLS reader")
	require.ErrorContains(t, err, "reaches beyond a read-only capability")
	require.ErrorContains(t, err, "BYPASSRLS")
}

// Every other widening, one at a time, against the same positive control.
func TestScopedReaderAuthorityRefusesEachWidening(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fixture := newReaderFixture(t, ctx)
	store, err := fixture.open(t, ctx)
	require.NoError(t, err, "the unwidened reader is the positive control")
	store.Close()

	for name, tc := range map[string]struct {
		widen, restore, want string
	}{
		// Reaching a cross-tenant role: one SET ROLE and the reader reads every
		// tenant through app_control_plane's own policy.
		"reaches the control-plane role": {
			"GRANT app_control_plane TO ra_reader",
			"REVOKE app_control_plane FROM ra_reader",
			"app_control_plane (a role other than the login itself)",
		},
		"reaches the tenant role": {
			"GRANT app_tenant TO ra_reader",
			"REVOKE app_tenant FROM ra_reader",
			"a role other than the login itself",
		},
		// An owner's own tables do not apply row-level security to it unless the
		// table forces it, and the owner may drop FORCE.
		"owns a schema": {
			"CREATE SCHEMA ra_owned AUTHORIZATION ra_reader",
			"DROP SCHEMA ra_owned CASCADE",
			"owns a schema",
		},
		"owns a relation": {
			"CREATE TABLE ra_owned_table(id int); ALTER TABLE ra_owned_table OWNER TO ra_reader",
			"DROP TABLE ra_owned_table",
			"owns a relation",
		},
		// A SECURITY DEFINER function runs as its owner, so executing one lends
		// the reader that owner's authority. This is the synthetic definer case.
		"can execute a SECURITY DEFINER function": {
			`CREATE FUNCTION ra_definer() RETURNS bigint LANGUAGE sql SECURITY DEFINER AS 'SELECT count(*) FROM reader_rows';
			 REVOKE ALL ON FUNCTION ra_definer() FROM PUBLIC;
			 GRANT EXECUTE ON FUNCTION ra_definer() TO ra_reader`,
			"DROP FUNCTION ra_definer()",
			"SECURITY DEFINER function",
		},
		// A grant of EXECUTE to the login itself on an ordinary function is
		// still something added to this login alone.
		"granted EXECUTE on an ordinary function": {
			`CREATE FUNCTION ra_plain() RETURNS int LANGUAGE sql AS 'SELECT 1';
			 REVOKE ALL ON FUNCTION ra_plain() FROM PUBLIC;
			 GRANT EXECUTE ON FUNCTION ra_plain() TO ra_reader`,
			"DROP FUNCTION ra_plain()",
			"EXECUTE granted to the login on function",
		},
		"granted a write": {
			"GRANT INSERT ON reader_rows TO ra_reader",
			"REVOKE INSERT ON reader_rows FROM ra_reader",
			"INSERT on table public.reader_rows",
		},
		"granted a column write": {
			"GRANT UPDATE (secret) ON reader_rows TO ra_reader",
			"REVOKE UPDATE (secret) ON reader_rows FROM ra_reader",
			"UPDATE granted to the login on column public.reader_rows.secret",
		},
		"granted a sequence": {
			"CREATE SEQUENCE ra_sequence; GRANT USAGE ON SEQUENCE ra_sequence TO ra_reader",
			"DROP SEQUENCE ra_sequence",
			"USAGE on sequence public.ra_sequence",
		},
		"granted CREATE on a schema": {
			"GRANT CREATE ON SCHEMA public TO ra_reader",
			"REVOKE CREATE ON SCHEMA public FROM ra_reader",
			"CREATE on schema public",
		},
		"granted CREATE on the database": {
			"GRANT CREATE ON DATABASE scoped_pools_fixture TO ra_reader",
			"REVOKE CREATE ON DATABASE scoped_pools_fixture FROM ra_reader",
			"CREATE on database",
		},
		// pg_temp is searched before every other schema, so a temporary relation
		// shadows the unqualified names the reader's own queries resolve.
		"granted TEMPORARY on the database": {
			"GRANT TEMPORARY ON DATABASE scoped_pools_fixture TO ra_reader",
			"REVOKE TEMPORARY ON DATABASE scoped_pools_fixture FROM ra_reader",
			"TEMPORARY on database",
		},
		"granted a parameter": {
			"GRANT SET ON PARAMETER work_mem TO ra_reader",
			"REVOKE SET ON PARAMETER work_mem FROM ra_reader",
			"SET on parameter work_mem",
		},
		"granted SELECT on the system catalog": {
			"GRANT SELECT ON pg_catalog.pg_authid TO ra_reader",
			"REVOKE SELECT ON pg_catalog.pg_authid FROM ra_reader",
			"granted to the login on table pg_catalog.pg_authid",
		},
		"granted a default privilege beyond SELECT": {
			"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT INSERT ON TABLES TO ra_reader",
			"ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE INSERT ON TABLES FROM ra_reader",
			"default INSERT on tables",
		},
		"owns a default privilege of its own": {
			"ALTER DEFAULT PRIVILEGES FOR ROLE ra_reader IN SCHEMA public GRANT SELECT ON TABLES TO app_tenant",
			"ALTER DEFAULT PRIVILEGES FOR ROLE ra_reader IN SCHEMA public REVOKE SELECT ON TABLES FROM app_tenant",
			"created by ra_reader",
		},
		"holds an elevated attribute": {
			"ALTER ROLE ra_reader CREATEROLE",
			"ALTER ROLE ra_reader NOCREATEROLE",
			"ra_reader (CREATEROLE)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture.exec(t, ctx, tc.widen)
			t.Cleanup(func() { fixture.exec(t, context.Background(), tc.restore) })

			_, err := fixture.open(t, ctx)
			require.Error(t, err, "the production constructor must refuse this reader")
			require.ErrorContains(t, err, tc.want)

			// And the same reader opens again once the widening is undone, so the
			// judgement is not simply refusing everything.
			fixture.exec(t, ctx, tc.restore)
			reopened, err := fixture.open(t, ctx)
			require.NoError(t, err, "the reader opens again once the widening is undone")
			reopened.Close()
			fixture.exec(t, ctx, tc.widen)
		})
	}
}
