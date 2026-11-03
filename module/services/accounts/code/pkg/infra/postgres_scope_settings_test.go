//go:build !pure

package infra_test

import (
	"testing"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/pkg/infra"
)

// The database must read the caller's scope under exactly the two settings the
// platform's Postgres library installs, and under no others. A policy or a
// SECURITY DEFINER body left on a setting nothing binds any more does not fail:
// current_setting(..., true) returns NULL, the predicate is false, and the
// relation reads as empty — so the break is a silent loss of access on one
// table, or, with the comparison the other way round, a silent loss of
// isolation. Nothing else in the suite would see it: a test that sets the old
// name and reads the old policy passes on both halves of the mistake.
//
// This asks the catalog rather than the migration text, which is the only place
// that knows the whole set — 75 policies over 70 relations, 6 more on partitions
// of audit_events, and 9 function bodies, when migration 65 renamed them. It
// reads every object kind an expression can hide in, not just pg_policies, and
// the counts below refuse a vacuous pass: a query that matched nothing would
// otherwise read exactly like a clean database.
func TestEveryScopedPolicyReadsTheLibrarySettings(t *testing.T) {
	require.Equal(t, "codefly.current_tenant_id", infra.TenantScopeSetting)
	require.Equal(t, "codefly.current_user_id", infra.UserScopeSetting)

	dsn, err := codefly.For(testCtx).Service("store").Secret("postgres", "owner-connection")
	require.NoError(t, err)
	pool, err := pgxpool.New(testCtx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	conn, err := pool.Acquire(testCtx)
	require.NoError(t, err)
	defer conn.Release()
	// An empty search_path is what makes the deparser schema-qualify, which is
	// how the ledger's own text is rendered; it also keeps these probes honest
	// about the names they read back.
	_, err = conn.Exec(testCtx, `SELECT pg_catalog.set_config('search_path', '', false)`)
	require.NoError(t, err)

	// Every place a scope setting can be read: a policy predicate, a routine
	// body, a view definition, a check constraint, a column default, an index
	// expression or predicate, and a rule.
	const sources = `
WITH source AS (
    SELECT 'policy ' || p.polrelid::regclass::text || '.' || p.polname AS what,
           coalesce(pg_get_expr(p.polqual, p.polrelid), '') || ' ' ||
           coalesce(pg_get_expr(p.polwithcheck, p.polrelid), '') AS body
      FROM pg_catalog.pg_policy p
    UNION ALL
    SELECT 'routine ' || p.oid::regprocedure::text, p.prosrc
      FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'public'
    UNION ALL
    SELECT 'view ' || c.oid::regclass::text, pg_get_viewdef(c.oid)
      FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public' AND c.relkind IN ('v', 'm')
    UNION ALL
    SELECT 'constraint ' || con.conrelid::regclass::text || '.' || con.conname, pg_get_constraintdef(con.oid)
      FROM pg_catalog.pg_constraint con JOIN pg_catalog.pg_namespace n ON n.oid = con.connamespace
     WHERE n.nspname = 'public'
    UNION ALL
    SELECT 'default ' || a.attrelid::regclass::text || '.' || a.attname, pg_get_expr(ad.adbin, ad.adrelid)
      FROM pg_catalog.pg_attrdef ad
      JOIN pg_catalog.pg_attribute a ON a.attrelid = ad.adrelid AND a.attnum = ad.adnum
    UNION ALL
    SELECT 'index ' || i.indexrelid::regclass::text, pg_get_indexdef(i.indexrelid)
      FROM pg_catalog.pg_index i
    UNION ALL
    SELECT 'rule ' || r.ev_class::regclass::text || '.' || r.rulename, pg_get_ruledef(r.oid)
      FROM pg_catalog.pg_rewrite r WHERE r.rulename <> '_RETURN'
)
SELECT what, body FROM source WHERE body ~ $1 ORDER BY what`

	var stale []string
	rows, err := conn.Query(testCtx, sources, `\mapp\.current_(org|user)_id\M`)
	require.NoError(t, err)
	for rows.Next() {
		var what, body string
		require.NoError(t, rows.Scan(&what, &body))
		stale = append(stale, what)
	}
	require.NoError(t, rows.Err())
	require.Empty(t, stale,
		"these objects still read the host's old scope settings, which nothing binds any more, so each one reads as empty")

	// And the rename did land, rather than the probe above finding nothing
	// because the database is empty or the query is wrong.
	count := func(pattern string) int {
		var n int
		require.NoError(t, conn.QueryRow(testCtx, `SELECT count(*) FROM (`+sources+`) c`, pattern).Scan(&n))
		return n
	}
	tenantReaders := count(`\m` + infra.TenantScopeSetting + `\M`)
	userReaders := count(`\m` + infra.UserScopeSetting + `\M`)
	require.Greater(t, tenantReaders, 70, "the tenant setting is read by far more than this; a low count means the probe is wrong")
	require.Greater(t, userReaders, 25, "the user setting is read by far more than this; a low count means the probe is wrong")

	// Each partition of audit_events carries its own copy of the parent's
	// policies, and a query that names the partition is judged by those alone.
	var outOfStep []string
	partitions, err := conn.Query(testCtx, `
SELECT i.inhrelid::regclass::text
  FROM pg_catalog.pg_inherits i
 WHERE i.inhparent = 'public.audit_events'::regclass
   AND NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_policy p
         WHERE p.polrelid = i.inhrelid
           AND coalesce(pg_get_expr(p.polqual, p.polrelid), '') ~ ('\m' || $1 || '\M'))
 ORDER BY 1`, infra.TenantScopeSetting)
	require.NoError(t, err)
	for partitions.Next() {
		var name string
		require.NoError(t, partitions.Scan(&name))
		outOfStep = append(outOfStep, name)
	}
	require.NoError(t, partitions.Err())
	require.Empty(t, outOfStep, "every partition of audit_events carries its own tenant policy, and these do not read the tenant setting")
}
