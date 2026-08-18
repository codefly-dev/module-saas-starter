package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// TestAuditEventTypes_Parity verifies the DB projection reconciles exactly to
// the Go catalog: every registered type lands in audit_event_types with a
// matching version and category, and none is stale/deprecated.
func TestAuditEventTypes_Parity(t *testing.T) {
	ctx := testCtx
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		return testStore.SyncAuditEventTypes(ctx, business.AuditEventCatalog())
	}))

	var rows []business.AuditEventTypeRow
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		rows, err = testStore.ListAuditEventTypes(ctx)
		return err
	}))

	byName := make(map[string]business.AuditEventTypeRow, len(rows))
	for _, r := range rows {
		byName[r.Name] = r
	}
	for _, d := range business.AuditEventCatalog() {
		row, ok := byName[string(d.Type)]
		require.Truef(t, ok, "catalog type %q missing from audit_event_types", d.Type)
		require.Equal(t, d.Version, row.Version, "version mismatch for %q", d.Type)
		require.Equal(t, string(d.Category), row.Category, "category mismatch for %q", d.Type)
		require.Falsef(t, row.Deprecated, "catalog type %q must not be deprecated", d.Type)
	}
}

// TestAuditRetention_DropsOldPartitions proves retention actually removes data
// now: it drops whole partitions whose range is entirely older than the cutoff
// (which the append-only trigger would have blocked as a row DELETE), while
// leaving current-month data intact.
func TestAuditRetention_DropsOldPartitions(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, org := mustUserAndOrg(t, ctx, "ret@audit-test.com", "ret-audit", "Retention Co")

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		return testStore.EnsureAuditPartitions(ctx, 3)
	}))

	// A current-month event we expect to survive retention.
	require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
		return testStore.InsertAuditEvent(ctx, business.AuditEntry{
			ActorType: "user", EventType: business.EventUserUpdated, Resource: "user", OrgID: org,
		})
	}))

	// Cutoff = first instant of the current month. The empty prior-month
	// partitions (seeded at migration + EnsureAuditPartitions) are entirely
	// older than the cutoff and must be dropped; the current partition must not.
	now := time.Now().UTC()
	cutoff := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var dropped int64
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		dropped, err = testStore.DropAuditPartitionsBefore(ctx, cutoff)
		return err
	}))
	require.Positive(t, dropped, "expected at least one old empty partition to be dropped")

	// Current-month data survives.
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	got, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{
		OrgID: org, EventType: string(business.EventUserUpdated), From: &past, To: &future, PageSize: 100,
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "current-month audit rows must survive partition-drop retention")

	// Restore the pruned prior-month partitions so later tests are unaffected.
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		return testStore.EnsureAuditPartitions(ctx, 3)
	}))
}

// TestAuditAggregation exercises the analytics path: counts grouped by event
// type and by day.
func TestAuditAggregation(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, org := mustUserAndOrg(t, ctx, "agg@audit-test.com", "agg-audit", "Aggregate Co")

	seed := func(et business.EventType, n int) {
		require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
			for i := 0; i < n; i++ {
				if err := testStore.InsertAuditEvent(ctx, business.AuditEntry{
					ActorType: "user", EventType: et, Resource: "user", OrgID: org,
				}); err != nil {
					return err
				}
			}
			return nil
		}))
	}
	seed(business.EventUserUpdated, 3)
	seed(business.EventSettingsUpdated, 2)

	byType, err := testService.AggregateAuditLog(ctx, business.AuditQuery{OrgID: org}, "event_type", "")
	require.NoError(t, err)
	counts := map[string]int64{}
	var typeTotal int64
	for _, b := range byType {
		counts[b.Key] = b.Count
		typeTotal += b.Count
	}
	// The org also carries the org.created event emitted at creation, so we
	// assert the seeded types' counts precisely rather than the grand total.
	require.Equal(t, int64(3), counts[string(business.EventUserUpdated)])
	require.Equal(t, int64(2), counts[string(business.EventSettingsUpdated)])

	// A filtered event_type aggregation isolates one type.
	filtered, err := testService.AggregateAuditLog(ctx,
		business.AuditQuery{OrgID: org, EventType: string(business.EventUserUpdated)}, "event_type", "")
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, int64(3), filtered[0].Count)

	byDay, err := testService.AggregateAuditLog(ctx, business.AuditQuery{OrgID: org}, "time", "day")
	require.NoError(t, err)
	var dayTotal int64
	for _, b := range byDay {
		dayTotal += b.Count
	}
	require.Equal(t, typeTotal, dayTotal, "time buckets must cover the same events as type buckets")
}

// TestAuditPayloadSearch verifies JSONB containment filtering over typed payloads.
func TestAuditPayloadSearch(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, org := mustUserAndOrg(t, ctx, "pay@audit-test.com", "pay-audit", "Payload Co")

	require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
		if err := testStore.InsertAuditEvent(ctx, business.AuditEntry{
			ActorType: "user", EventType: business.EventOnboardingStepDone, Resource: "organization",
			OrgID: org, Payload: map[string]any{"step": "invite_team"},
		}); err != nil {
			return err
		}
		return testStore.InsertAuditEvent(ctx, business.AuditEntry{
			ActorType: "user", EventType: business.EventOnboardingStepDone, Resource: "organization",
			OrgID: org, Payload: map[string]any{"step": "connect_billing"},
		})
	}))

	hit, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{
		OrgID: org, PayloadContains: map[string]any{"step": "invite_team"}, PageSize: 100,
	})
	require.NoError(t, err)
	require.Len(t, hit, 1)
	require.Equal(t, "invite_team", hit[0].Payload["step"])

	miss, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{
		OrgID: org, PayloadContains: map[string]any{"step": "nonexistent"}, PageSize: 100,
	})
	require.NoError(t, err)
	require.Empty(t, miss)
}
