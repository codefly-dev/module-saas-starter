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
// owner, and an owner that can skip row-level security skips it. So every
// session selecting one read EVERY tenant's rows whatever scope it bound.
//
// That is not confined to the read-only capability: all three are granted to
// app_tenant, the only role the request login reaches, so the crossing sat on
// ordinary request traffic — the boundary the split login exists to draw.
// 15_delegation_views_security_invoker.up.sql makes them execute as the
// selecting session instead.
//
// These run against the actually-provisioned store, so the views are the real
// ones, owned by the real migration principal and read through the real
// migrated schema, and both probes authenticate as a real served capability:
// the scoped reader's own read-only connection and the request login. No
// synthetic role stands in for either.
//
// What this file does NOT do is decide the general question. Whether the
// crossing arises depends on the owner's authority, and this store has exactly
// one owner profile. The profile-independent proof — including the
// non-superuser owner that a superuser check would wrongly call safe — is in
// qualification/scopedpools/delegation_view_owner_test.go, on a disposable
// native PostgreSQL where the owner's authority is established explicitly.

// delegationViews are the three audit views 14 sets security_invoker on. Every
// one exposes org_id.
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
// row count.
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

// delegationTenant is one seeded tenant and the principal names only it has, so
// a projection can be checked against the tenant it belongs to rather than
// merely against being non-empty.
type delegationTenant struct {
	orgID       string
	actorName   string
	grantorName string
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
//
// The principal identifiers carry label, so the denormalized names the views
// project are unique to this tenant. The shared seedActorPrincipal helpers use
// one fixed identifier for every org, which would make both tenants' names
// identical and reduce a projection check to "not empty" — which is exactly
// what a leak would also satisfy.
func seedDelegationTenant(t *testing.T, label string) delegationTenant {
	t.Helper()
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	actor := seedAgentPrincipal(t, orgID, "ci.test/view-actor-"+label+":0.1.0")
	grantor := seedAgentPrincipal(t, orgID, "ci.test/view-grantor-"+label+":0.1.0")
	seedDelegationGrant(t, orgID, actor.ID, grantor.ID, "pattern")
	seedDelegationGrant(t, orgID, actor.ID, grantor.ID, "one_shot")
	return delegationTenant{orgID: orgID, actorName: actor.DisplayName, grantorName: grantor.DisplayName}
}

// The shipped ledger is what sets the option. Everything below reads as a
// property of the running database; this is the line that ties that property to
// 15_delegation_views_security_invoker.up.sql rather than to anything a test
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
	tenantA := seedDelegationTenant(t, "iso-a")
	tenantB := seedDelegationTenant(t, "iso-b")
	require.NotEqual(t, tenantA.orgID, tenantB.orgID)

	for _, probe := range delegationProbes(t) {
		for _, view := range delegationViews {
			t.Run(probe.name+"/"+view, func(t *testing.T) {
				bindScope(t, probe.conn, "")
				orgs, rows := orgsVisible(t, probe.conn, view)
				require.Emptyf(t, orgs, "%s read %d rows from %s with no tenant bound", probe.name, rows, view)

				for _, bound := range []struct {
					label, org, other string
				}{
					{"tenant A", tenantA.orgID, tenantB.orgID},
					{"tenant B", tenantB.orgID, tenantA.orgID},
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

// The columns the views add over their base table are filtered too. These are
// checked against the names that belong to the bound tenant and against the
// absence of the other tenant's names — not merely against being non-empty,
// which a crossing would satisfy just as well.
func TestDelegationViewProjectionsCarryOnlyTheBoundTenant(t *testing.T) {
	tenantA := seedDelegationTenant(t, "proj-a")
	tenantB := seedDelegationTenant(t, "proj-b")
	require.NotEqual(t, tenantA.actorName, tenantB.actorName, "the fixture must give each tenant its own names")
	require.NotEqual(t, tenantA.grantorName, tenantB.grantorName)

	for _, probe := range delegationProbes(t) {
		t.Run(probe.name, func(t *testing.T) {
			defer bindScope(t, probe.conn, "")

			for _, bound := range []struct {
				label string
				own   delegationTenant
				other delegationTenant
			}{
				{"tenant A", tenantA, tenantB},
				{"tenant B", tenantB, tenantA},
			} {
				bindScope(t, probe.conn, bound.own.orgID)

				// The denormalized principal names, which come from the join.
				// Both sides are compared: a leak that carried the other
				// tenant's grantor while the actor looked right would pass a
				// non-empty check.
				rows, err := probe.conn.Query(testCtx, `
					SELECT coalesce(actor_display_name, ''), coalesce(grantor_display_name, '')
					FROM public.delegation_grants_recent`)
				require.NoError(t, err)
				names, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]string, error) {
					var pair [2]string
					return pair, row.Scan(&pair[0], &pair[1])
				})
				require.NoError(t, err)
				require.Lenf(t, names, 2, "%s seeded two grants, both inside the 7-day window", bound.label)
				for _, pair := range names {
					require.Equalf(t, bound.own.actorName, pair[0], "%s: actor name under %s", probe.name, bound.label)
					require.Equalf(t, bound.own.grantorName, pair[1], "%s: grantor name under %s", probe.name, bound.label)
					require.NotEqual(t, bound.other.actorName, pair[0], "the other tenant's actor name must not appear")
					require.NotEqual(t, bound.other.grantorName, pair[1], "the other tenant's grantor name must not appear")
				}

				// The burn-rate projection carries both names too, and its own
				// WHERE selects pattern grants.
				var patternActor, patternGrantor string
				var patternUses int
				require.NoError(t, probe.conn.QueryRow(testCtx, `
					SELECT coalesce(actor_display_name, ''), coalesce(grantor_display_name, ''), use_count
					FROM public.delegation_pattern_usage`).Scan(&patternActor, &patternGrantor, &patternUses))
				require.Equal(t, bound.own.actorName, patternActor)
				require.Equal(t, bound.own.grantorName, patternGrantor)
				require.Equal(t, 3, patternUses)
				var patternRows int
				require.NoError(t, probe.conn.QueryRow(testCtx, `
					SELECT count(*) FROM public.delegation_pattern_usage`).Scan(&patternRows))
				require.Equalf(t, 1, patternRows, "%s seeded exactly one pattern grant", bound.label)

				// The aggregate must total this tenant's grants alone.
				var aggregated int
				require.NoError(t, probe.conn.QueryRow(testCtx, `
					SELECT coalesce(sum(count), 0) FROM public.delegation_stats_daily`).Scan(&aggregated))
				require.Equalf(t, 2, aggregated, "%s: the aggregate must total %s's grants alone", probe.name, bound.label)
			}
		})
	}
}

// ownerAuthority is what decides whether a view executing as its owner can skip
// row-level security.
type ownerAuthority struct {
	Role      string
	Superuser bool
	BypassRLS bool
}

// canSkipRowSecurity reports whether this owner escapes a FORCE'd policy.
// Superuser is not the only way: BYPASSRLS on an ordinary role does the same,
// which is why "the owner is not a superuser" is not a statement that the
// crossing cannot arise.
func (o ownerAuthority) canSkipRowSecurity() bool { return o.Superuser || o.BypassRLS }

// delegationViewOwner reads the authority of one view's owner.
func delegationViewOwner(t *testing.T, view string) ownerAuthority {
	t.Helper()
	var owner ownerAuthority
	require.NoError(t, testPool.QueryRow(testCtx, `
		SELECT role.rolname::text, role.rolsuper, role.rolbypassrls
		FROM pg_catalog.pg_class relation
		JOIN pg_catalog.pg_roles role ON role.oid = relation.relowner
		WHERE relation.oid = to_regclass('public.' || $1)`, view).Scan(
		&owner.Role, &owner.Superuser, &owner.BypassRLS))
	return owner
}

// The isolation above is a property of migration 14 and of nothing else. This
// removes only that migration's setting — the down migration's one effect —
// from each of the three views in turn and requires the crossing to come back
// on every capability, then restores it and requires isolation again. Without
// this the assertions above could pass on a store where the views never leaked.
//
// It runs the red half only where the owner's authority makes a crossing
// possible, and it qualifies that authority explicitly per view rather than
// assuming it: superuser OR BYPASSRLS, because either skips a FORCE'd policy.
// Where the owner holds neither, this store cannot demonstrate the red half —
// and this test then says so and asserts NOTHING about that profile's safety.
// A non-superuser owner is not thereby safe: it can hold BYPASSRLS, or the base
// table can carry a policy permissive to it, and
// qualification/scopedpools/delegation_view_owner_test.go establishes both
// cases on a fixture where the owner's authority is set explicitly.
func TestDelegationViewIsolationComesFromTheInvokerSetting(t *testing.T) {
	tenantA := seedDelegationTenant(t, "attr-a")
	seedDelegationTenant(t, "attr-b")

	owner := connectAs(t, storeSecret(t, "owner-connection"))
	setInvoker := func(view string, on bool) {
		_, err := owner.Exec(testCtx, fmt.Sprintf(`ALTER VIEW public.%s SET (security_invoker = %t)`, view, on)) //nolint:gosec // view is from delegationViews
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		for _, view := range delegationViews {
			setInvoker(view, true)
		}
	})

	for _, view := range delegationViews {
		authority := delegationViewOwner(t, view)
		t.Logf("public.%s is owned by %q (superuser=%t, bypassrls=%t): a crossing is possible here = %t",
			view, authority.Role, authority.Superuser, authority.BypassRLS, authority.canSkipRowSecurity())

		for _, probe := range delegationProbes(t) {
			t.Run(view+"/"+probe.name, func(t *testing.T) {
				setInvoker(view, true)
				bindScope(t, probe.conn, tenantA.orgID)
				_, bounded := orgsVisible(t, probe.conn, view)
				require.NotZero(t, bounded, "tenant A must read its own rows with the setting on")
				bindScope(t, probe.conn, "")
				orgs, rows := orgsVisible(t, probe.conn, view)
				require.Emptyf(t, orgs, "with security_invoker set, an unbound session read %d rows from %s", rows, view)

				if !authority.canSkipRowSecurity() {
					// Nothing is asserted about the cleared state here. On this
					// profile the owner cannot skip the base table's policy, so
					// clearing the setting would demonstrate neither a crossing
					// nor its absence for any other profile — and an owner that
					// is merely not a superuser is not safe.
					t.Skipf("owner %q holds neither SUPERUSER nor BYPASSRLS, so this store cannot exhibit the crossing; "+
						"the profile-independent red/green is in qualification/scopedpools", authority.Role)
				}

				setInvoker(view, false)
				orgs, rows = orgsVisible(t, probe.conn, view)
				require.NotZerof(t, rows, "without security_invoker, %s owned by %q must leak to %s; if it does not, this test is not proving what closes the crossing", view, authority.Role, probe.name)
				require.Greaterf(t, len(orgs), 1, "the leak crosses tenants: %s read one org only from %s", probe.name, view)

				setInvoker(view, true)
				orgs, _ = orgsVisible(t, probe.conn, view)
				require.Empty(t, orgs, "restoring the setting restores the isolation")
			})
		}
	}
}
