//go:build !pure

package scopedpools

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/infra"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Until service-postgres exposed a per-capability validation seam, the accounts
// reader policy ran ONCE, at startup, on a connection that never served. The
// classes the library's own restricted-session policy does not cover —
// ownership of a relation, schema or function, excess grants, SECURITY DEFINER
// grants, sequence and parameter privileges, default ACLs — were therefore
// judged at boot and never again, so a GRANT landing afterwards was served from
// every pooled connection for the life of the process.
//
// WithConnectionPolicies closes that, and this is the regression that proves
// it: a backend that has already served is widened while it sits idle in the
// pool, and the NEXT borrow must be refused — at the connection boundary,
// before the application callback runs — then admitted again once the widening
// is undone. Each widening is a class the restricted-session policy does not
// judge, so a pass here is the application policy running per checkout and not
// the library's generic one.

const (
	cpOrgA  = "60000000-0000-0000-0000-000000000001"
	cpOrgB  = "60000000-0000-0000-0000-000000000002"
	cpUserA = "70000000-0000-0000-0000-000000000001"
)

type policyFixture struct {
	admin                            *pgx.Conn
	readerDSN, writerDSN, controlDSN string
}

// newPolicyFixture provisions three logins shaped as the Postgres agent
// provisions them and the one table GetOrgMembership reads. It drops what it
// creates, so it neither inherits nor leaves state for the other cases in this
// package, which build the same table under their own role names.
func newPolicyFixture(t *testing.T, ctx context.Context) policyFixture {
	t.Helper()
	dsn := os.Getenv("ACCOUNTS_SCOPED_POOL_TEST_DSN")
	if dsn == "" {
		t.Skip("use scripts/qualify-scoped-pools.py for a disposable native PostgreSQL fixture")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", u.Hostname())

	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	EnsureStoreBaseline(t, ctx, admin)

	_, err = admin.Exec(ctx, `
 DROP TABLE IF EXISTS organization_members CASCADE;
 DO $$ BEGIN
   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='cp_reader') THEN CREATE ROLE cp_reader LOGIN PASSWORD 'cp_reader-pw'; END IF;
   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='cp_writer') THEN CREATE ROLE cp_writer LOGIN NOINHERIT PASSWORD 'cp_writer-pw'; END IF;
   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='cp_control') THEN CREATE ROLE cp_control LOGIN NOINHERIT PASSWORD 'cp_control-pw'; END IF;
 END $$;
 GRANT app_tenant TO cp_writer;
 ALTER ROLE cp_writer SET role = 'app_tenant';
 GRANT app_control_plane TO cp_control;
 ALTER ROLE cp_control SET role = 'app_control_plane';
 CREATE TABLE organization_members(org_id uuid, user_id uuid, role text, joined_at timestamptz DEFAULT now());
 ALTER TABLE organization_members ENABLE ROW LEVEL SECURITY;
 ALTER TABLE organization_members FORCE ROW LEVEL SECURITY;
 CREATE POLICY cp_request_scope ON organization_members TO cp_reader, app_tenant
   USING (org_id::text = current_setting('app.current_org_id', true)
      AND user_id::text = current_setting('app.current_user_id', true));
 CREATE POLICY cp_control_scope ON organization_members TO app_control_plane USING (true);
 GRANT SELECT ON organization_members TO cp_reader, app_tenant, app_control_plane;
 INSERT INTO organization_members(org_id,user_id,role) VALUES
   ('`+cpOrgA+`','`+cpUserA+`','member'),
   ('`+cpOrgB+`','`+cpUserA+`','admin');`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP TABLE IF EXISTS organization_members CASCADE`)
	})

	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "")
	t.Setenv("POSTGRES_TOKEN_FILE", "")
	dir := t.TempDir()
	paths := map[string]string{}
	for _, principal := range []string{"cp_reader", "cp_writer", "cp_control"} {
		paths[principal] = filepath.Join(dir, principal)
		require.NoError(t, os.WriteFile(paths[principal], []byte(principal+"-pw"), 0o600))
	}
	raw, err := json.Marshal(paths)
	require.NoError(t, err)
	t.Setenv("POSTGRES_TOKEN_FILES", string(raw))

	connection := func(principal string) string {
		copied := *u
		copied.User = url.User(principal)
		query := copied.Query()
		query.Set("application_name", "connection_policy_"+principal)
		// One physical backend, so the second read is guaranteed to reuse the
		// connection the first one opened. That is the case the startup-only
		// judgement could never refuse.
		query.Set("pool_max_conns", "1")
		copied.RawQuery = query.Encode()
		return copied.String()
	}
	return policyFixture{
		admin:      admin,
		readerDSN:  connection("cp_reader"),
		writerDSN:  connection("cp_writer"),
		controlDSN: connection("cp_control"),
	}
}

func (f policyFixture) exec(t *testing.T, ctx context.Context, statement string) {
	t.Helper()
	_, err := f.admin.Exec(ctx, statement)
	require.NoError(t, err, statement)
}

// readerWidenings are authority a read-only login must not hold, one per class
// the library's restricted-session policy does NOT judge. Each is applied to a
// login whose pooled backend has already served, so what refuses the next
// borrow can only be the application policy on the checkout path.
var readerWidenings = []struct {
	name, widen, restore, want string
}{
	{
		"owns a relation",
		"CREATE TABLE cp_owned(id int); ALTER TABLE cp_owned OWNER TO cp_reader",
		"DROP TABLE IF EXISTS cp_owned",
		"owns a relation",
	},
	{
		"granted a write",
		"GRANT INSERT ON organization_members TO cp_reader",
		"REVOKE INSERT ON organization_members FROM cp_reader",
		"INSERT on table public.organization_members",
	},
	{
		"can execute a SECURITY DEFINER function",
		`CREATE FUNCTION cp_definer() RETURNS bigint LANGUAGE sql SECURITY DEFINER AS 'SELECT count(*) FROM organization_members';
		 REVOKE ALL ON FUNCTION cp_definer() FROM PUBLIC;
		 GRANT EXECUTE ON FUNCTION cp_definer() TO cp_reader`,
		"DROP FUNCTION IF EXISTS cp_definer()",
		"SECURITY DEFINER function",
	},
}

// The whole lifecycle, on the reader the served process actually uses: a valid
// profile is admitted and serves; a widening applied while the backend sits
// idle in the pool is refused at the next borrow, before the read runs; and the
// same pool serves again once the widening is undone, so the judgement is not
// simply poisoning the connection permanently.
func TestReaderPolicyRefusesAWidenedBackendAtTheNextCheckout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fixture := newPolicyFixture(t, ctx)

	store, err := infra.NewPostgresStoreWithCapabilities(ctx, fixture.readerDSN, fixture.writerDSN, fixture.controlDSN)
	require.NoError(t, err, "the correctly provisioned profile must open")
	t.Cleanup(store.Close)

	verified := auth.WithVerifiedDatabaseIdentity(ctx, cpUserA, cpOrgA)
	read := func() error {
		membership, err := store.GetOrgMembership(verified, cpOrgA, cpUserA)
		if err != nil {
			return err
		}
		require.NotNil(t, membership, "a successful read must return the row")
		require.Equal(t, cpOrgA, membership.OrgId)
		require.Equal(t, cpUserA, membership.UserId)
		return nil
	}

	// Valid-profile acceptance, and the backend is now in the pool.
	require.NoError(t, read(), "the valid profile must serve")
	require.NoError(t, read(), "and serve again on the reused backend")

	for _, widening := range readerWidenings {
		t.Run(widening.name, func(t *testing.T) {
			fixture.exec(t, ctx, widening.widen)
			restored := false
			t.Cleanup(func() {
				if !restored {
					fixture.exec(t, context.Background(), widening.restore)
				}
			})

			// The connection that served a moment ago is still the pool's.
			// Refusing now is the checkout judgement: nothing reconnected.
			err := read()
			require.Error(t, err, "a widened login must be refused at the next checkout")
			require.ErrorContains(t, err, "connection policy",
				"the refusal must come from the capability's connection policy, which runs before the read")
			require.ErrorContains(t, err, "reaches beyond a read-only capability")
			require.ErrorContains(t, err, widening.want)

			// Recovery: undo it and the same pool serves again, which also
			// shows the refusal was the authority and not a poisoned pool.
			fixture.exec(t, ctx, widening.restore)
			restored = true
			require.NoError(t, read(), "the reader serves again once the widening is undone")
		})
	}

	// And after the whole sequence the profile is still the valid one.
	require.NoError(t, read(), "the valid profile still serves at the end")
}

// The judgement is on the checkout path, so it costs something on every
// transaction. This records what, rather than leaving it unmeasured: the
// coordinator asked for checkout cost, and a policy whose cost nobody measured
// is a policy nobody can size a pool against.
func TestReaderPolicyCheckoutCost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fixture := newPolicyFixture(t, ctx)

	store, err := infra.NewPostgresStoreWithCapabilities(ctx, fixture.readerDSN, fixture.writerDSN, fixture.controlDSN)
	require.NoError(t, err)
	t.Cleanup(store.Close)

	verified := auth.WithVerifiedDatabaseIdentity(ctx, cpUserA, cpOrgA)
	const runs = 20
	_, err = store.GetOrgMembership(verified, cpOrgA, cpUserA) // warm the backend
	require.NoError(t, err)

	start := time.Now()
	for range runs {
		_, err := store.GetOrgMembership(verified, cpOrgA, cpUserA)
		require.NoError(t, err)
	}
	perRead := time.Since(start) / runs

	t.Logf("scoped read with the reader policy on the checkout path: %v per read over %d reads", perRead, runs)
	// Not a benchmark and deliberately loose: this fails only if the judgement
	// has become pathological, which is what would make it a pool-sizing
	// problem rather than a cost.
	require.Less(t, perRead, 250*time.Millisecond,
		"the per-checkout judgement should cost milliseconds, not hundreds of them")
}
