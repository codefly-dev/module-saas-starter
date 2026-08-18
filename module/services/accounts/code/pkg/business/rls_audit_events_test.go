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
// invisible. NULL-org rows are visible only via WithControlPlane.
func TestRLS_AuditEvents_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-ae@rls-test.com", "alice-ae-rls", "Acme AE A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-ae@rls-test.com", "bob-ae-rls", "Acme AE B")

	// Seed: a tenant-scoped event for each org + a NULL-org system event.
	// Direct InsertAuditEvent under the appropriate wrapper isolates the RLS
	// policy from fan-out behavior.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		return testStore.InsertAuditEvent(ctx, business.AuditEntry{
			ActorType: "user", EventType: "test.event", Resource: "test", OrgID: orgA,
		})
	}))
	require.NoError(t, testStore.WithOrgTx(ctx, orgB, func(ctx context.Context) error {
		return testStore.InsertAuditEvent(ctx, business.AuditEntry{
			ActorType: "user", EventType: "test.event", Resource: "test", OrgID: orgB,
		})
	}))
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		return testStore.InsertAuditEvent(ctx, business.AuditEntry{
			ActorType: "system", EventType: "system.event", Resource: "system",
			// OrgID intentionally empty — NULL in DB
		})
	}))

	// As A: query filtered to test.event so the org.created emitted
	// by CreateOrganization (in mustUserAndOrg) doesn't show up. The
	// filter narrows to A's seeded row.
	now := time.Now().Add(1 * time.Hour)
	past := time.Now().Add(-1 * time.Hour)
	asA, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{OrgID: orgA, EventType: "test.event", From: &past, To: &now, PageSize: 100})
	require.NoError(t, err)
	require.Len(t, asA, 1)
	require.Equal(t, orgA, asA[0].OrgID)

	// As B: filter same way.
	asB, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{OrgID: orgB, EventType: "test.event", From: &past, To: &now, PageSize: 100})
	require.NoError(t, err)
	require.Len(t, asB, 1)
	require.Equal(t, orgB, asB[0].OrgID)

	// Probe: from A's tx, query B's events via the Store directly.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, _, _, err := testStore.QueryAuditLog(ctx, business.AuditQuery{OrgID: orgB, EventType: "test.event", From: &past, To: &now, PageSize: 100})
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's audit_events from A's tx")
		return nil
	}))

	// Platform-admin path (orgID==""): WithControlPlane via the Service
	// wrapper. Should see everything (both tenant events + NULL-org
	// system event).
	all, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{From: &past, To: &now, PageSize: 100})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 3,
		"platform-admin scope (orgID='') must see all events including NULL-org rows")

	// Un-wrapped: zero rows.
	noWrap, _, _, err := testStore.QueryAuditLog(context.Background(), business.AuditQuery{OrgID: orgA, EventType: "test.event", From: &past, To: &now, PageSize: 100})
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped QueryAuditLog must return ZERO rows (RLS fail-closed)")
}

// TestRLS_AuditEvents_DurableEmitterPicksWrapper verifies tenant and system
// events choose the correct transactional authority boundary.
func TestRLS_AuditEvents_DurableEmitterPicksWrapper(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-em@rls-test.com", "alice-em-rls", "Acme EM A")

	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)

	emitter.Emit(ctx, business.AuditEntry{
		ActorType: "user", EventType: "tenant.event", Resource: "test", OrgID: orgA,
	})
	emitter.Emit(ctx, business.AuditEntry{
		ActorType: "system", EventType: "system.event", Resource: "test",
		// OrgID empty → NULL-org write under WithControlPlane
	})

	emitter.Close()

	// Tenant event visible to org A.
	now := time.Now().Add(1 * time.Hour)
	past := time.Now().Add(-1 * time.Hour)
	asA, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{OrgID: orgA, EventType: "tenant.event", From: &past, To: &now, PageSize: 100})
	require.NoError(t, err)
	require.Len(t, asA, 1, "tenant event written via durable emitter must be visible to its org")

	// System event visible only via bypass (platform-admin scope).
	all, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{EventType: "system.event", From: &past, To: &now, PageSize: 100})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 1, "system event with NULL org_id must be visible under bypass")
}
