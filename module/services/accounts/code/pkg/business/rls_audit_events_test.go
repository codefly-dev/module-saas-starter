package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// TestRLS_AuditEvents_CrossTenantBlocked — Phase 2D, polymorphic
// policy on audit_events. Two orgs each emit a tenant-scoped audit
// event. Org A's tx must see only its own events; B's events stay
// invisible. NULL-org rows are visible only via WithBypass.
func TestRLS_AuditEvents_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-ae@rls-test.com", "alice-ae-rls", "Acme AE A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-ae@rls-test.com", "bob-ae-rls", "Acme AE B")

	// Seed: a tenant-scoped event for each org + a NULL-org system event.
	// Direct InsertAuditEvent under the appropriate wrapper — bypassing
	// the AsyncAuditEmitter so the test is synchronous.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		return testStore.InsertAuditEvent(ctx, business.AuditEntry{
			ActorType: "user", Action: "test.event", Resource: "test", OrgID: orgA,
		})
	}))
	require.NoError(t, testStore.WithOrgTx(ctx, orgB, func(ctx context.Context) error {
		return testStore.InsertAuditEvent(ctx, business.AuditEntry{
			ActorType: "user", Action: "test.event", Resource: "test", OrgID: orgB,
		})
	}))
	require.NoError(t, testStore.WithBypass(ctx, func(ctx context.Context) error {
		return testStore.InsertAuditEvent(ctx, business.AuditEntry{
			ActorType: "system", Action: "system.event", Resource: "system",
			// OrgID intentionally empty — NULL in DB
		})
	}))

	// As A: query filtered to test.event so the org.created emitted
	// by CreateOrganization (in mustUserAndOrg) doesn't show up. The
	// filter narrows to A's seeded row.
	now := time.Now().Add(1 * time.Hour)
	past := time.Now().Add(-1 * time.Hour)
	asA, _, _, err := testService.QueryAuditLog(ctx, orgA, "", "test.event", "", "", &past, &now, 100, "")
	require.NoError(t, err)
	require.Len(t, asA, 1)
	require.Equal(t, orgA, asA[0].OrgID)

	// As B: filter same way.
	asB, _, _, err := testService.QueryAuditLog(ctx, orgB, "", "test.event", "", "", &past, &now, 100, "")
	require.NoError(t, err)
	require.Len(t, asB, 1)
	require.Equal(t, orgB, asB[0].OrgID)

	// Probe: from A's tx, query B's events via the Store directly.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, _, _, err := testStore.QueryAuditLog(ctx, orgB, "", "test.event", "", "", &past, &now, 100, "")
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's audit_events from A's tx")
		return nil
	}))

	// Platform-admin path (orgID==""): WithBypass via the Service
	// wrapper. Should see everything (both tenant events + NULL-org
	// system event).
	all, _, _, err := testService.QueryAuditLog(ctx, "", "", "", "", "", &past, &now, 100, "")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 3,
		"platform-admin scope (orgID='') must see all events including NULL-org rows")

	// Un-wrapped: zero rows.
	noWrap, _, _, err := testStore.QueryAuditLog(context.Background(), orgA, "", "test.event", "", "", &past, &now, 100, "")
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped QueryAuditLog must return ZERO rows (RLS fail-closed)")
}

// TestRLS_AuditEvents_AsyncEmitterPicksWrapper — Phase 2D, the
// AsyncAuditEmitter must pick WithOrgTx vs WithBypass based on
// entry.OrgID. Drains the channel synchronously by closing it and
// waiting; then verifies both tenant + system events landed.
func TestRLS_AuditEvents_AsyncEmitterPicksWrapper(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-em@rls-test.com", "alice-em-rls", "Acme EM A")

	// Drive the global asyncAuditEmitter by way of Service.emit which
	// calls it under the hood. We use a fresh emitter to control the
	// drain timing — close + sleep is reliable for buffered channels.
	emitter := business.NewAsyncAuditEmitter(testStore, 16)

	emitter.Emit(ctx, business.AuditEntry{
		ActorType: "user", Action: "tenant.event", Resource: "test", OrgID: orgA,
	})
	emitter.Emit(ctx, business.AuditEntry{
		ActorType: "system", Action: "system.event", Resource: "test",
		// OrgID empty → NULL-org write under WithBypass
	})

	emitter.Close()
	// Give drain() a moment to commit the writes; the test pool's
	// flush is sub-millisecond but be generous.
	time.Sleep(200 * time.Millisecond)

	// Tenant event visible to org A.
	now := time.Now().Add(1 * time.Hour)
	past := time.Now().Add(-1 * time.Hour)
	asA, _, _, err := testService.QueryAuditLog(ctx, orgA, "", "tenant.event", "", "", &past, &now, 100, "")
	require.NoError(t, err)
	require.Len(t, asA, 1, "tenant event written via async emitter must be visible to its org")

	// System event visible only via bypass (platform-admin scope).
	all, _, _, err := testService.QueryAuditLog(ctx, "", "", "system.event", "", "", &past, &now, 100, "")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 1, "system event with NULL org_id must be visible under bypass")
}
