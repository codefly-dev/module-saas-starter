//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra/storetx"
)

// A view that is not security_invoker executes as its OWNER. The three
// delegation audit views read delegation_grants — which forces row-level
// security, and whose tenant policy carries no TO clause and so applies to
// every role — but under owner execution that policy is evaluated against the
// owner, and forced row-level security does not apply to a superuser. So
// wherever the migration principal that created them is a superuser, every
// session selecting one read EVERY tenant's rows whatever scope it bound.
//
// That is not confined to the read-only capability: all three are granted to
// app_tenant, the only role the request login reaches, so the crossing sat on
// ordinary request traffic — the boundary the split login exists to draw.
// 14_delegation_views_security_invoker.up.sql makes them execute as the
// selecting session instead.
//
// These run against the actually-provisioned store, so the views are owned by
// the real migration principal and read through the real migrated schema, and
// both probes authenticate as a real served capability: the scoped reader's own
// read-only connection and the request login. No synthetic role stands in for
// either.

// delegationViews are the three audit views 14 sets security_invoker on, with
// the org column each exposes.
var delegationViews = []string{
	"delegation_grants_recent",
	"delegation_pattern_usage",
	"delegation_stats_daily",
}

// viewProbe is one capability's own connection, used to select the views as
// that capability really would.
type viewProbe struct {
	name string
	conn *pgx.Conn
}

// delegationProbes opens the scoped reader's capability and the request login's
// capability. Both are the connections the served process uses; neither is
// created for the test.
func delegationProbes(t *testing.T) []viewProbe {
	t.Helper()
	probes := []viewProbe{
		{name: "the scoped reader", conn: connectAs(t, readerConnection(t))},
		{name: "the request login (app_tenant)", conn: connectAs(t, storeSecret(t, "read-write-connection"))},
	}
	for _, probe := range probes {
		var role string
		require.NoError(t, probe.conn.QueryRow(testCtx, `SELECT current_user::text`).Scan(&role))
		t.Logf("%s connects as %q", probe.name, role)
	}
	return probes
}

func connectAs(t *testing.T, connection string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(testCtx, connection)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// bindScope sets the request scope on a probe connection, or clears it when
// orgID is empty. Session-level, on the probe's own connection.
func bindScope(t *testing.T, conn *pgx.Conn, orgID string) {
	t.Helper()
	_, err := conn.Exec(testCtx, `SELECT set_config('app.current_org_id', $1, false)`, orgID)
	require.NoError(t, err)
}

// orgsVisible returns the distinct org_ids a probe reads from a view, and the
// row count. Every one of the three exposes org_id.
func orgsVisible(t *testing.T, conn *pgx.Conn, view string) ([]string, int) {
	t.Helper()
	rows, err := conn.Query(testCtx, fmt.Sprintf(`SELECT org_id::text FROM public.%s`, view)) //nolint:gosec // view is from delegationViews, not input
	require.NoError(t, err, "%s must be selectable by this capability", view)
	all, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	seen := map[string]bool{}
	var distinct []string
	for _, org := range all {
		if !seen[org] {
			seen[org] = true
			distinct = append(distinct, org)
		}
	}
	return distinct, len(all)
}

// seedDelegationGrant inserts one grant in org, as a tenant-scoped identity so
// the tenant policy's WITH CHECK is what admits it — no bypass.
func seedDelegationGrant(t *testing.T, orgID, actorID, grantorID, kind string) {
	t.Helper()
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx, `
			INSERT INTO delegation_grants
				(org_id, actor_principal_id, grantor_principal_id, action, resource,
				 justification, risk_level, kind, status, expires_at,
				 action_pattern, resource_pattern, max_uses, use_count, request_context)
			VALUES ($1, $2, $3, 'read', 'record', 'view isolation fixture', 'low', $4, 'approved',
				CURRENT_TIMESTAMP + interval '1 day',
				CASE WHEN $4 = 'pattern' THEN 'read' END,
				CASE WHEN $4 = 'pattern' THEN 'record/*' END,
				CASE WHEN $4 = 'pattern' THEN 10 END,
				CASE WHEN $4 = 'pattern' THEN 3 ELSE 0 END,
				'{"via_pattern": true}'::jsonb)`,
			orgID, actorID, grantorID, kind)
		return err
	}))
}

// seedDelegationTenant builds one tenant with rows in all three views: a
// pattern grant (delegation_pattern_usage selects kind = 'pattern') and a
// one-shot, both created now so they fall inside the 7-day and 90-day windows.
func seedDelegationTenant(t *testing.T) string {
	t.Helper()
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)
	seedDelegationGrant(t, orgID, actor, grantor, "pattern")
	seedDelegationGrant(t, orgID, actor, grantor, "one_shot")
	return orgID
}

// The shipped ledger is what sets the option. Everything below reads as a
// property of the running database; this is the line that ties that property to
// 14_delegation_views_security_invoker.up.sql rather than to anything a test
// did. A store booted from the migrations in this tree must already carry it.
func TestShippedMigrationLeavesTheDelegationViewsAsInvoker(t *testing.T) {
	for _, view := range delegationViews {
		t.Run(view, func(t *testing.T) {
			var options []string
			require.NoError(t, testPool.QueryRow(testCtx, `
				SELECT coalesce(relation.reloptions, ARRAY[]::text[])
				FROM pg_catalog.pg_class relation
				JOIN pg_catalog.pg_namespace namespace ON namespace.oid = relation.relnamespace
				WHERE namespace.nspname = 'public' AND relation.relname = $1`, view).Scan(&options))
			require.Containsf(t, options, "security_invoker=true",
				"public.%s must carry security_invoker from the migration; found %v", view, options)
		})
	}
}

// Each view, read through each real capability: nothing without a tenant bound,
// exactly tenant A's rows under A, exactly tenant B's under B. Two tenants are
// seeded so "A only" is a statement about filtering and not about there being
// nothing else to see, and every positive read is required to be non-empty so
// no case can pass by returning nothing.
func TestDelegationViewsIsolateTenantsOnEveryCapability(t *testing.T) {
	orgA := seedDelegationTenant(t)
	orgB := seedDelegationTenant(t)
	require.NotEqual(t, orgA, orgB)

	for _, probe := range delegationProbes(t) {
		for _, view := range delegationViews {
			t.Run(probe.name+"/"+view, func(t *testing.T) {
				bindScope(t, probe.conn, "")
				orgs, rows := orgsVisible(t, probe.conn, view)
				require.Emptyf(t, orgs, "%s read %d rows from %s with no tenant bound", probe.name, rows, view)

				for _, bound := range []struct{ label, org, other string }{
					{"tenant A", orgA, orgB},
					{"tenant B", orgB, orgA},
				} {
					bindScope(t, probe.conn, bound.org)
					orgs, rows := orgsVisible(t, probe.conn, view)
					require.NotZerof(t, rows, "%s read nothing from %s under %s; a passing case must actually read its own rows", probe.name, view, bound.label)
					require.Equalf(t, []string{bound.org}, orgs, "%s read another tenant's rows from %s under %s", probe.name, view, bound.label)
					require.NotContainsf(t, orgs, bound.other, "%s read the other tenant from %s under %s", probe.name, view, bound.label)
				}
				bindScope(t, probe.conn, "")
			})
		}
	}
}

// The columns the views add over their base table are filtered too: a joined
// principal field and an aggregate count are what these views exist for, and a
// crossing that leaked only through them would be just as wide.
func TestDelegationViewProjectionsCarryOnlyTheBoundTenant(t *testing.T) {
	orgA := seedDelegationTenant(t)
	orgB := seedDelegationTenant(t)

	for _, probe := range delegationProbes(t) {
		t.Run(probe.name, func(t *testing.T) {
			bindScope(t, probe.conn, orgA)
			defer bindScope(t, probe.conn, "")

			// The denormalized principal names, which come from the join.
			rows, err := probe.conn.Query(testCtx, `
				SELECT actor_display_name, grantor_display_name
				FROM public.delegation_grants_recent`)
			require.NoError(t, err)
			names, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]*string, error) {
				var pair [2]*string
				return pair, row.Scan(&pair[0], &pair[1])
			})
			require.NoError(t, err)
			require.NotEmpty(t, names, "the joined projection must be non-empty under tenant A")
			for _, pair := range names {
				require.NotNil(t, pair[0], "the actor join resolved to nothing; the principals policy refused it")
				require.NotEmpty(t, *pair[0])
			}

			// The burn-rate projection, whose own WHERE selects pattern grants.
			var patternRows, patternUses int
			require.NoError(t, probe.conn.QueryRow(testCtx, `
				SELECT count(*), coalesce(sum(use_count), 0)
				FROM public.delegation_pattern_usage`).Scan(&patternRows, &patternUses))
			require.Equal(t, 1, patternRows, "tenant A seeded exactly one pattern grant")
			require.Equal(t, 3, patternUses)

			// The aggregate. Tenant A seeded two grants; the count column must
			// total those and not also carry tenant B's.
			var aggregated int
			require.NoError(t, probe.conn.QueryRow(testCtx, `
				SELECT coalesce(sum(count), 0) FROM public.delegation_stats_daily`).Scan(&aggregated))
			require.Equal(t, 2, aggregated, "the aggregate must total tenant A's grants alone")

			bindScope(t, probe.conn, orgB)
			var forB int
			require.NoError(t, probe.conn.QueryRow(testCtx, `
				SELECT coalesce(sum(count), 0) FROM public.delegation_stats_daily`).Scan(&forB))
			require.Equal(t, 2, forB, "tenant B totals its own two grants")
		})
	}
}

// The isolation above is a property of migration 14 and of nothing else. This
// removes only that migration's setting — the down migration's one effect — and
// requires the crossing to come back, then restores it and requires isolation
// again. Without this the assertions above could pass on a store where the
// views never leaked, and the migration would be proving nothing.
//
// The crossing is profile-dependent: it arises because the view owner is a
// superuser, for whom forced row-level security does not apply. Where the owner
// is not a superuser the base table's policy binds it too and clearing the
// setting changes nothing — so this asserts the leak only on the profile that
// has one, and asserts its absence on the profile that does not, rather than
// assuming either.
func TestDelegationViewIsolationComesFromTheInvokerSetting(t *testing.T) {
	orgA := seedDelegationTenant(t)
	seedDelegationTenant(t)

	owner := connectAs(t, storeSecret(t, "owner-connection"))
	setInvoker := func(on bool) {
		for _, view := range delegationViews {
			_, err := owner.Exec(testCtx, fmt.Sprintf(`ALTER VIEW public.%s SET (security_invoker = %t)`, view, on)) //nolint:gosec // view is from delegationViews
			require.NoError(t, err)
		}
	}
	t.Cleanup(func() { setInvoker(true) })

	var ownerIsSuperuser bool
	require.NoError(t, owner.QueryRow(testCtx, `
		SELECT role.rolsuper
		FROM pg_catalog.pg_class view
		JOIN pg_catalog.pg_roles role ON role.oid = view.relowner
		WHERE view.oid = 'public.delegation_grants_recent'::regclass`).Scan(&ownerIsSuperuser))
	t.Logf("the delegation views are owned by a superuser: %t", ownerIsSuperuser)

	for _, probe := range delegationProbes(t) {
		t.Run(probe.name, func(t *testing.T) {
			setInvoker(true)
			bindScope(t, probe.conn, orgA)
			_, bounded := orgsVisible(t, probe.conn, "delegation_grants_recent")
			require.NotZero(t, bounded, "tenant A must read its own rows with the setting on")
			bindScope(t, probe.conn, "")
			orgs, rows := orgsVisible(t, probe.conn, "delegation_grants_recent")
			require.Emptyf(t, orgs, "with security_invoker set, an unbound session read %d rows", rows)

			setInvoker(false)
			orgs, rows = orgsVisible(t, probe.conn, "delegation_grants_recent")
			if ownerIsSuperuser {
				require.NotZerof(t, rows, "without security_invoker the superuser-owned view must leak; if it does not, this test is not proving what closes the crossing")
				require.Greater(t, len(orgs), 1, "the leak crosses tenants: an unbound session reads more than one org")
			} else {
				require.Empty(t, orgs, "on a non-superuser owner the base table's forced policy binds the owner too, so no crossing arises either way")
			}

			setInvoker(true)
			orgs, _ = orgsVisible(t, probe.conn, "delegation_grants_recent")
			require.Empty(t, orgs, "restoring the setting restores the isolation")
		})
	}
}
