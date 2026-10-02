//go:build !pure

package scopedpools

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Whether a view that is not security_invoker crosses tenants is a property of
// its OWNER's authority, and the provisioned store has exactly one owner. So
// the store-backed test can only exhibit the profile it happens to have, and
// the general claim has to be established somewhere the owner's authority is
// set explicitly. That is here, on the disposable native PostgreSQL this
// package already provisions.
//
// The claim being tested is narrow and the obvious shortcut is wrong: "the
// owner is not a superuser" does NOT mean the crossing cannot arise. Two
// non-superuser owners below leak exactly as a superuser does — one holding
// BYPASSRLS, one merely named by a permissive policy on the base table. A
// fourth, holding neither, does not leak, which is what makes the first three
// findings rather than an artefact of the fixture.
//
// Every case runs through both capability shapes the store serves: a read-only
// login with grants of its own and no role, and a login whose session role is
// app_tenant. Nothing here touches a shared database, and no production grant
// or ownership is widened — the whole fixture is created and dropped in a
// database this package owns.

const (
	dvOrgA = "50000000-0000-0000-0000-000000000001"
	dvOrgB = "50000000-0000-0000-0000-000000000002"
)

// dvOwner is one owner profile and why it can or cannot skip row security.
type dvOwner struct {
	role string
	// provision makes the role hold exactly the authority named.
	provision string
	// leaks is what the authority implies, and what the test requires.
	leaks bool
	why   string
}

var dvOwners = []dvOwner{
	{
		role:      "dv_super",
		provision: "ALTER ROLE dv_super SUPERUSER",
		leaks:     true,
		why:       "a superuser skips row-level security whether or not the table forces it",
	},
	{
		// The case a rolsuper check calls safe and is not.
		role:      "dv_bypass",
		provision: "ALTER ROLE dv_bypass NOSUPERUSER BYPASSRLS",
		leaks:     true,
		why:       "BYPASSRLS on an ordinary role skips a FORCE'd policy exactly as a superuser does",
	},
	{
		// The second way a non-superuser owner leaks: nothing about the role,
		// everything about what the base table's policies say about it.
		role:      "dv_permissive",
		provision: "CREATE POLICY dv_rows_permissive ON dv_rows TO dv_permissive USING (true)",
		leaks:     true,
		why:       "a permissive policy naming the owner returns every row to it, so owner execution returns every row",
	},
	{
		// The control. Without it, "they all leaked" would say nothing.
		role:      "dv_plain",
		provision: "SELECT 1",
		leaks:     false,
		why:       "an ordinary owner is bound by the same tenant policy as anyone else",
	},
}

// dvViews mirror the three shipped views' shapes: a join, a filtered select and
// an aggregate. Each is created per owner, so the owner is the only variable.
var dvViews = []struct{ name, body string }{
	{"dv_recent", `SELECT rows.org_id, rows.secret, actor.display_name AS actor_display_name
		FROM dv_rows rows JOIN dv_principals actor ON actor.id = rows.actor_id`},
	{"dv_pattern", `SELECT rows.org_id, rows.use_count FROM dv_rows rows WHERE rows.kind = 'pattern'`},
	{"dv_stats", `SELECT org_id, count(*) AS count FROM dv_rows GROUP BY org_id`},
}

type dvFixture struct {
	admin *pgx.Conn
	base  *url.URL
}

func newDelegationViewFixture(t *testing.T, ctx context.Context) dvFixture {
	t.Helper()
	dsn := os.Getenv("ACCOUNTS_SCOPED_POOL_TEST_DSN")
	if dsn == "" {
		t.Skip("use scripts/qualify-scoped-pools.py for a disposable native PostgreSQL fixture")
	}
	base, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", base.Hostname())

	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	EnsureStoreBaseline(t, ctx, admin)

	// Two tenants, one row each, under forced row-level security whose tenant
	// policy carries no TO clause — as the shipped delegation_grants does.
	_, err = admin.Exec(ctx, `
 DO $$ BEGIN
   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='dv_reader') THEN CREATE ROLE dv_reader LOGIN NOINHERIT PASSWORD 'dv_reader-pw'; END IF;
   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='dv_tenant') THEN CREATE ROLE dv_tenant LOGIN NOINHERIT PASSWORD 'dv_tenant-pw'; END IF;
 END $$;
 GRANT app_tenant TO dv_tenant;
 ALTER ROLE dv_tenant SET role = 'app_tenant';
 DROP TABLE IF EXISTS dv_rows CASCADE;
 DROP TABLE IF EXISTS dv_principals CASCADE;
 CREATE TABLE dv_principals(id int PRIMARY KEY, display_name text);
 INSERT INTO dv_principals VALUES (1,'actor-a'),(2,'actor-b');
 CREATE TABLE dv_rows(org_id uuid, actor_id int, kind text, use_count int, secret text);
 ALTER TABLE dv_rows ENABLE ROW LEVEL SECURITY;
 ALTER TABLE dv_rows FORCE ROW LEVEL SECURITY;
 CREATE POLICY dv_rows_tenant ON dv_rows
   USING (org_id::text = current_setting('app.current_org_id', true));
 INSERT INTO dv_rows VALUES
   ('`+dvOrgA+`',1,'pattern',3,'a-secret'),
   ('`+dvOrgB+`',2,'pattern',7,'b-secret');
 GRANT SELECT ON dv_rows, dv_principals TO dv_reader, app_tenant;`)
	require.NoError(t, err)
	return dvFixture{admin: admin, base: base}
}

func (f dvFixture) connect(t *testing.T, ctx context.Context, principal string) *pgx.Conn {
	t.Helper()
	copied := *f.base
	copied.User = url.UserPassword(principal, principal+"-pw")
	conn, err := pgx.Connect(ctx, copied.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// orgsRead returns the distinct tenants a connection reads from a relation.
func orgsRead(t *testing.T, ctx context.Context, conn *pgx.Conn, relation string) []string {
	t.Helper()
	rows, err := conn.Query(ctx, fmt.Sprintf(`SELECT DISTINCT org_id::text FROM %s ORDER BY 1`, relation)) //nolint:gosec // relation is from dvViews
	require.NoError(t, err, "%s must be selectable", relation)
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return out
}

// For each owner profile, each view shape and each capability: with
// security_invoker cleared the view executes as its owner, and whether that
// crosses tenants is decided by the owner's authority — including for two
// owners that are NOT superusers. With the setting on, none of it matters and
// every capability reads only what it bound.
func TestDelegationViewOwnerAuthorityDecidesTheCrossing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	fixture := newDelegationViewFixture(t, ctx)

	for _, owner := range dvOwners {
		t.Run(owner.role, func(t *testing.T) {
			_, err := fixture.admin.Exec(ctx, fmt.Sprintf(
				`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='%s') THEN CREATE ROLE %s; END IF; END $$`,
				owner.role, owner.role))
			require.NoError(t, err)
			_, err = fixture.admin.Exec(ctx, `GRANT SELECT ON dv_rows, dv_principals TO `+owner.role)
			require.NoError(t, err)
			_, err = fixture.admin.Exec(ctx, owner.provision)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = fixture.admin.Exec(context.Background(), `DROP POLICY IF EXISTS dv_rows_permissive ON dv_rows`)
				_, _ = fixture.admin.Exec(context.Background(), fmt.Sprintf(`ALTER ROLE %s NOSUPERUSER NOBYPASSRLS`, owner.role))
			})

			// The authority is asserted, not assumed: the whole point is that
			// "not a superuser" is not the question.
			var superuser, bypassrls bool
			require.NoError(t, fixture.admin.QueryRow(ctx,
				`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = $1`, owner.role).Scan(&superuser, &bypassrls))
			t.Logf("owner %s: superuser=%t bypassrls=%t — expected to leak: %t (%s)",
				owner.role, superuser, bypassrls, owner.leaks, owner.why)
			if owner.role == "dv_bypass" {
				require.False(t, superuser, "dv_bypass must NOT be a superuser: it is the case a rolsuper check would call safe")
				require.True(t, bypassrls)
			}
			if owner.role == "dv_permissive" || owner.role == "dv_plain" {
				require.False(t, superuser, "%s must be an ordinary role", owner.role)
				require.False(t, bypassrls, "%s must not hold BYPASSRLS", owner.role)
			}

			for _, view := range dvViews {
				// One view per owner, created by that owner so it owns it.
				_, err := fixture.admin.Exec(ctx, fmt.Sprintf(
					`DROP VIEW IF EXISTS %s; CREATE VIEW %s AS %s; ALTER VIEW %s OWNER TO %s; GRANT SELECT ON %s TO dv_reader, app_tenant`,
					view.name, view.name, view.body, view.name, owner.role, view.name))
				require.NoError(t, err)

				for _, capability := range []struct{ name, login string }{
					{"the read-only capability", "dv_reader"},
					{"the request capability (app_tenant)", "dv_tenant"},
				} {
					t.Run(view.name+"/"+capability.name, func(t *testing.T) {
						conn := fixture.connect(t, ctx, capability.login)

						// Green: with security_invoker the view executes as the
						// selecting session, so the owner's authority is
						// irrelevant and the tenant policy binds.
						_, err := fixture.admin.Exec(ctx, fmt.Sprintf(`ALTER VIEW %s SET (security_invoker = true)`, view.name))
						require.NoError(t, err)
						_, err = conn.Exec(ctx, `SELECT set_config('app.current_org_id', $1, false)`, dvOrgA)
						require.NoError(t, err)
						require.Equal(t, []string{dvOrgA}, orgsRead(t, ctx, conn, view.name),
							"with security_invoker, %s reads only the tenant it bound", capability.name)
						_, err = conn.Exec(ctx, `SELECT set_config('app.current_org_id', '', false)`)
						require.NoError(t, err)
						require.Empty(t, orgsRead(t, ctx, conn, view.name),
							"with security_invoker and nothing bound, %s reads nothing", capability.name)

						// Red: cleared, the view executes as its owner.
						_, err = fixture.admin.Exec(ctx, fmt.Sprintf(`ALTER VIEW %s SET (security_invoker = false)`, view.name))
						require.NoError(t, err)
						crossed := orgsRead(t, ctx, conn, view.name)
						if owner.leaks {
							require.Lenf(t, crossed, 2,
								"owner %s (%s): %s read %v from %s with nothing bound, expected both tenants",
								owner.role, owner.why, capability.name, crossed, view.name)
						} else {
							require.Emptyf(t, crossed,
								"owner %s (%s): %s read %v from %s, but this owner is bound by the tenant policy",
								owner.role, owner.why, capability.name, crossed, view.name)
						}

						// And the base table never crossed, on any profile.
						require.Empty(t, orgsRead(t, ctx, conn, "dv_rows"),
							"the base table's forced policy binds the selecting session throughout")
					})
				}
			}
		})
	}
}
