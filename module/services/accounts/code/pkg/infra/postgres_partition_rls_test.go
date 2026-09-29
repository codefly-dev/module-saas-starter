//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"testing"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/relationcatalog"
)

// storedPolicy is one row-level-security policy as PostgreSQL stores it, with
// its expressions deparsed against the relation that carries it.
type storedPolicy struct {
	name, command, roles, using, check string
	permissive                         bool
}

func relationPolicies(t *testing.T, ctx context.Context, tx pgx.Tx, relation string) []storedPolicy {
	t.Helper()
	rows, err := tx.Query(ctx, `
		SELECT polname, polcmd::text, polpermissive, polroles::text,
		       COALESCE(pg_get_expr(polqual, polrelid), ''),
		       COALESCE(pg_get_expr(polwithcheck, polrelid), '')
		  FROM pg_policy
		 WHERE polrelid = $1::regclass
		 ORDER BY polname`, relation)
	require.NoError(t, err, relation)
	defer rows.Close()
	var policies []storedPolicy
	for rows.Next() {
		var policy storedPolicy
		require.NoError(t, rows.Scan(&policy.name, &policy.command, &policy.permissive, &policy.roles, &policy.using, &policy.check))
		policies = append(policies, policy)
	}
	require.NoError(t, rows.Err())
	return policies
}

// requirePartitionsSecured holds every partition of every partitioned public
// relation that requires row-level security to its parent's: enabled, forced,
// and exactly the parent's policies. PostgreSQL applies a partitioned table's
// policies only to a query that names the parent, so a partition queried by
// name is checked against its own policies alone.
func requirePartitionsSecured(t *testing.T, ctx context.Context, tx pgx.Tx) (partitions int) {
	t.Helper()
	rows, err := tx.Query(ctx, `
		SELECT parent.relname, child.oid::regclass::text, child.relrowsecurity, child.relforcerowsecurity
		  FROM pg_class parent
		  JOIN pg_namespace ns ON ns.oid = parent.relnamespace
		  JOIN pg_inherits inheritance ON inheritance.inhparent = parent.oid
		  JOIN pg_class child ON child.oid = inheritance.inhrelid
		 WHERE ns.nspname = 'public' AND parent.relkind = 'p'
		 ORDER BY 1, 2`)
	require.NoError(t, err)
	type partition struct {
		parent, name    string
		enabled, forced bool
	}
	var found []partition
	for rows.Next() {
		var p partition
		require.NoError(t, rows.Scan(&p.parent, &p.name, &p.enabled, &p.forced))
		found = append(found, p)
	}
	rows.Close()
	require.NoError(t, rows.Err())

	for _, p := range found {
		authority, classified := relationcatalog.All()[p.parent]
		require.True(t, classified, "partitioned relation %s is not classified", p.parent)
		if !authority.Scope.RequiresRLS() {
			continue
		}
		partitions++
		require.True(t, p.enabled, "%s is a partition of %s but does not enable row level security", p.name, p.parent)
		require.True(t, p.forced, "%s is a partition of %s but does not force row level security", p.name, p.parent)
		require.Equal(t, relationPolicies(t, ctx, tx, p.parent), relationPolicies(t, ctx, tx, p.name),
			"%s must carry exactly the policies of %s", p.name, p.parent)
	}
	return partitions
}

// Every partition the install created carries its parent's row-level security.
func TestPartitionsCarryTheirParentsRowSecurity(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := txFromCtx(t, ctx)
		require.Positive(t, requirePartitionsSecured(t, ctx, tx), "the install provisions audit_events partitions")
		return nil
	}))
}

// A partition created after the install — by the retention tick, or at startup
// — is secured in the same transaction that creates it. The month is one no
// install provisions, and the transaction rolls back so the partition does not
// outlive the test.
func TestANewAuditPartitionIsSecuredWhenItIsCreated(t *testing.T) {
	rolledBack := errors.New("roll back the probe partition")
	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := txFromCtx(t, ctx)
		var existed bool
		require.NoError(t, tx.QueryRow(ctx, `SELECT to_regclass('public.audit_events_2099_01') IS NOT NULL`).Scan(&existed))
		require.False(t, existed, "the probe month must not exist before the test creates it")
		_, err := tx.Exec(ctx, `SELECT audit_events_ensure_partition('2099-01-15'::date)`)
		require.NoError(t, err)

		var enabled, forced bool
		require.NoError(t, tx.QueryRow(ctx, `
			SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.audit_events_2099_01'::regclass`,
		).Scan(&enabled, &forced))
		require.True(t, enabled)
		require.True(t, forced)
		require.Equal(t, relationPolicies(t, ctx, tx, "public.audit_events"), relationPolicies(t, ctx, tx, "public.audit_events_2099_01"))
		requirePartitionsSecured(t, ctx, tx)
		return rolledBack
	})
	require.ErrorIs(t, err, rolledBack)
}

// A later change to audit_events' policies reaches every partition on the next
// ensure, not only the months EnsureAuditPartitions provisions: retention keeps
// about a year of partitions and the ensure window is five months wide. The
// probe month lies far outside that window, the change is the kind a migration
// makes, and the ensure runs as app_control_plane, the role startup and the
// retention tick run it as. Everything happens in one transaction that rolls
// back, so neither the probe partition nor the changed policy outlives the test.
func TestEnsuringAuditPartitionsReSecuresPartitionsOutsideTheWindow(t *testing.T) {
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()

		const parent, probe = "public.audit_events", "public.audit_events_2001_01"
		var existed bool
		require.NoError(t, tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, probe).Scan(&existed))
		require.False(t, existed, "the probe month must not exist before the test creates it")
		_, err = tx.Exec(ctx, `SELECT audit_events_ensure_partition('2001-01-15'::date)`)
		require.NoError(t, err)
		require.Equal(t, relationPolicies(t, ctx, tx, parent), relationPolicies(t, ctx, tx, probe))

		_, err = tx.Exec(ctx, `
			ALTER POLICY audit_events_tenant ON public.audit_events
			    USING (org_id IS NOT NULL
			           AND org_id::text = current_setting('app.current_org_id', true)
			           AND resource IS NOT NULL)`)
		require.NoError(t, err)
		require.NotEqual(t, relationPolicies(t, ctx, tx, parent), relationPolicies(t, ctx, tx, probe),
			"the premise: the probe partition carries the policies from before the change")

		_, err = tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
		require.NoError(t, err)
		require.NoError(t, testStore.EnsureAuditPartitions(context.WithValue(ctx, "tx", tx), 3)) //nolint:staticcheck // shared transaction context key
		_, err = tx.Exec(ctx, `RESET ROLE`)
		require.NoError(t, err)

		require.Equal(t, relationPolicies(t, ctx, tx, parent), relationPolicies(t, ctx, tx, probe),
			"a partition outside the ensure window must carry the parent's changed policies again")
		requirePartitionsSecured(t, ctx, tx)
	})
}

// The store's read-only login holds SELECT on every table, partitions included.
// Querying a partition by name must still show it only the organization the
// transaction is bound to, and nothing when none is bound — the same rows the
// parent shows it.
func TestReadOnlyLoginSeesOnlyTheBoundOrganizationInAnAuditPartition(t *testing.T) {
	orgA, orgB := uuid.NewString(), uuid.NewString()
	eventIDs := map[string]string{}
	for _, org := range []string{orgA, orgB} {
		id := uuid.NewString()
		eventIDs[org] = id
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			_, err := txFromCtx(t, ctx).Exec(ctx,
				`INSERT INTO audit_events (id, event_type, resource, org_id) VALUES ($1, $2, 'example', $3)`,
				id, string(business.EventAPIKeyCreated), org)
			return err
		}))
	}
	var partition string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return txFromCtx(t, ctx).QueryRow(ctx,
			`SELECT tableoid::regclass::text FROM audit_events WHERE id = $1`, eventIDs[orgA]).Scan(&partition)
	}))
	require.Regexp(t, `^audit_events_\d{4}_\d{2}$`, partition)

	connString, err := codefly.For(testCtx).Service("store").Secret("postgres", "read-only-connection")
	require.NoError(t, err)
	reader, err := pgx.Connect(testCtx, connString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(context.Background()) })

	// The premise: the login may read the partition, and is held by row-level
	// security rather than bypassing it. Without both, an empty answer would
	// prove nothing.
	var superuser, bypass, canSelect bool
	require.NoError(t, reader.QueryRow(testCtx, `
		SELECT rolsuper, rolbypassrls, has_table_privilege(current_user, $1, 'SELECT')
		  FROM pg_roles WHERE rolname = current_user`, partition).Scan(&superuser, &bypass, &canSelect))
	require.False(t, superuser)
	require.False(t, bypass)
	require.True(t, canSelect, "the read-only login holds SELECT on %s", partition)

	orgsVisible := func(relation, boundOrg string) []string {
		t.Helper()
		var orgs []string
		require.NoError(t, pgx.BeginFunc(testCtx, reader, func(tx pgx.Tx) error {
			if boundOrg != "" {
				if _, err := tx.Exec(testCtx, `SELECT set_config('app.current_org_id', $1, true)`, boundOrg); err != nil {
					return err
				}
			}
			rows, err := tx.Query(testCtx, `SELECT DISTINCT org_id::text FROM `+pgx.Identifier{relation}.Sanitize()+` WHERE org_id IS NOT NULL`)
			if err != nil {
				return err
			}
			orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		}))
		return orgs
	}

	for _, relation := range []string{partition, "audit_events"} {
		require.Empty(t, orgsVisible(relation, ""), "%s: nothing bound, nothing visible", relation)
		require.Equal(t, []string{orgA}, orgsVisible(relation, orgA), "%s: only the bound organization", relation)
		require.Equal(t, []string{orgB}, orgsVisible(relation, orgB), "%s: only the bound organization", relation)
	}
}
