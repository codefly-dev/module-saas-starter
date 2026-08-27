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

	byType, err := testService.AggregateAuditLog(ctx, business.AuditQuery{OrgID: org},
		business.AuditAggregationSpec{GroupBy: []string{"event_type"}})
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
		business.AuditQuery{OrgID: org, EventType: string(business.EventUserUpdated)},
		business.AuditAggregationSpec{GroupBy: []string{"event_type"}})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, int64(3), filtered[0].Count)

	byDay, err := testService.AggregateAuditLog(ctx, business.AuditQuery{OrgID: org},
		business.AuditAggregationSpec{GroupBy: []string{"time"}, Bucket: "day"})
	require.NoError(t, err)
	var dayTotal int64
	for _, b := range byDay {
		dayTotal += b.Count
	}
	require.Equal(t, typeTotal, dayTotal, "time buckets must cover the same events as type buckets")
}

// TestAuditAggregationMetrics exercises the non-count aggregations: numeric
// sum/avg/min/max/percentile over a payload field, distinct-count, multi-
// dimensional grouping, and derived ratios — all under org-scoped RLS.
func TestAuditAggregationMetrics(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, org := mustUserAndOrg(t, ctx, "metrics@audit-test.com", "metrics-audit", "Metrics Co")

	// Seed events carrying a numeric payload field "amount" plus a
	// non-numeric outcome, across two distinct actors.
	seed := func(actor string, outcome string, amount int) {
		require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
			return testStore.InsertAuditEvent(ctx, business.AuditEntry{
				ActorType: "user", ActorID: actor, EventType: business.EventUserUpdated, Resource: "user",
				OrgID: org, Payload: map[string]any{"amount": amount, "outcome": outcome},
			})
		}))
	}
	actorA := business.NewIDString()
	actorB := business.NewIDString()
	seed(actorA, "ok", 10)
	seed(actorA, "ok", 20)
	seed(actorB, "error", 30)
	seed(actorB, "ok", 40)

	q := business.AuditQuery{OrgID: org, EventType: string(business.EventUserUpdated)}

	// Numeric aggregations over payload:amount, grouped as a single bucket.
	agg, err := testService.AggregateAuditLog(ctx, q, business.AuditAggregationSpec{
		GroupBy: []string{"event_type"},
		Metrics: []business.AuditMetric{
			{Op: "sum", Field: "payload:amount"},
			{Op: "avg", Field: "payload:amount"},
			{Op: "min", Field: "payload:amount"},
			{Op: "max", Field: "payload:amount"},
			{Op: "count_distinct", Field: "actor_id", Alias: "actors"},
			{Op: "percentile", Field: "payload:amount", Percentile: 0.5, Alias: "p50"},
		},
	})
	require.NoError(t, err)
	require.Len(t, agg, 1)
	m := agg[0].Metrics
	require.Equal(t, int64(4), agg[0].Count)
	require.InDelta(t, 100, m["sum_amount"], 0.001)
	require.InDelta(t, 25, m["avg_amount"], 0.001)
	require.InDelta(t, 10, m["min_amount"], 0.001)
	require.InDelta(t, 40, m["max_amount"], 0.001)
	require.InDelta(t, 2, m["actors"], 0.001)
	require.InDelta(t, 25, m["p50"], 0.001)

	// A numeric metric over a group with no numeric values is absent, not 0:
	// filtering to a non-numeric payload field yields an empty min/avg/max.
	blank, err := testService.AggregateAuditLog(ctx, q, business.AuditAggregationSpec{
		GroupBy: []string{"event_type"},
		Metrics: []business.AuditMetric{
			{Op: "min", Field: "payload:outcome", Alias: "min_outcome"},
			{Op: "sum", Field: "payload:outcome", Alias: "sum_outcome"},
		},
	})
	require.NoError(t, err)
	require.Len(t, blank, 1)
	_, hasMin := blank[0].Metrics["min_outcome"]
	require.False(t, hasMin, "min over non-numeric values must be absent, not coerced to 0")
	require.InDelta(t, 0, blank[0].Metrics["sum_outcome"], 0.001, "empty sum is the additive identity 0")

	// Multi-dimensional grouping by actor + payload outcome.
	byActorOutcome, err := testService.AggregateAuditLog(ctx, q, business.AuditAggregationSpec{
		GroupBy: []string{"actor", "payload:outcome"},
	})
	require.NoError(t, err)
	seen := map[string]int64{}
	for _, b := range byActorOutcome {
		require.Len(t, b.Keys, 2)
		seen[b.Keys[0]+"/"+b.Keys[1]] = b.Count
	}
	require.Equal(t, int64(2), seen[actorA+"/ok"])
	require.Equal(t, int64(1), seen[actorB+"/error"])
	require.Equal(t, int64(1), seen[actorB+"/ok"])

	// Derived ratio: error rate = count(errors) / count(all).
	byOutcome, err := testService.AggregateAuditLog(ctx, q, business.AuditAggregationSpec{
		GroupBy: []string{"event_type"},
		Metrics: []business.AuditMetric{
			{Op: "count", Alias: "total"},
			{Op: "count_distinct", Field: "payload:outcome", Alias: "outcomes"},
		},
		Derived: []business.AuditDerivedMetric{
			{Alias: "distinct_ratio", Numerator: "outcomes", Denominator: "total"},
		},
	})
	require.NoError(t, err)
	require.Len(t, byOutcome, 1)
	// 2 distinct outcomes over 4 events = 0.5.
	require.InDelta(t, 0.5, byOutcome[0].Metrics["distinct_ratio"], 0.001)

	// Invalid specs are rejected before hitting the database.
	require.Error(t, business.AuditAggregationSpec{GroupBy: []string{"bogus"}}.Validate())
	require.Error(t, business.AuditAggregationSpec{
		Metrics: []business.AuditMetric{{Op: "sum", Field: "actor_id"}},
	}.Validate())
	require.Error(t, business.AuditAggregationSpec{
		Metrics: []business.AuditMetric{{Op: "percentile", Field: "payload:amount", Percentile: 2}},
	}.Validate())
	// Colliding aliases would silently drop a metric from the response map.
	require.Error(t, business.AuditAggregationSpec{
		Metrics: []business.AuditMetric{
			{Op: "count", Alias: "x"},
			{Op: "count_distinct", Field: "actor_id", Alias: "x"},
		},
	}.Validate(), "duplicate metric alias must be rejected")
	require.Error(t, business.AuditAggregationSpec{
		Metrics: []business.AuditMetric{{Op: "count", Alias: "total"}},
		Derived: []business.AuditDerivedMetric{{Alias: "total", Numerator: "total", Denominator: "total"}},
	}.Validate(), "derived alias colliding with a metric alias must be rejected")
}

// TestAuditAggregationNumericAndDerivedEdges covers two "no data ≠ zero" edges:
// numeric aggregation keys off the JSON type (every JSON number plus numeric
// strings, skipping non-numeric and missing values), and a derived ratio is
// omitted — not reported as 0 — when an operand is absent or the denominator is 0.
func TestAuditAggregationNumericAndDerivedEdges(t *testing.T) {
	clearData(t)
	ctx := testCtx
	_, org := mustUserAndOrg(t, ctx, "edges@audit-test.com", "edges-audit", "Edges Co")

	seed := func(payload map[string]any) {
		require.NoError(t, testStore.WithOrgTx(ctx, org, func(ctx context.Context) error {
			return testStore.InsertAuditEvent(ctx, business.AuditEntry{
				ActorType: "user", ActorID: business.NewIDString(), EventType: business.EventUserUpdated,
				Resource: "user", OrgID: org, Payload: payload,
			})
		}))
	}
	// price spans a JSON int, a JSON float, a numeric string, a non-numeric
	// string, and a missing key. label is always non-numeric.
	seed(map[string]any{"price": 10, "label": "x"})
	seed(map[string]any{"price": 2.5, "label": "x"})
	seed(map[string]any{"price": "7", "label": "x"})
	seed(map[string]any{"price": "free", "label": "x"})
	seed(map[string]any{"label": "x"})

	q := business.AuditQuery{OrgID: org, EventType: string(business.EventUserUpdated)}

	// Numeric aggregation includes 10 + 2.5 + 7 and skips "free" and the missing
	// key — proving the type-aware cast, not a text regex over ->>.
	agg, err := testService.AggregateAuditLog(ctx, q, business.AuditAggregationSpec{
		GroupBy: []string{"event_type"},
		Metrics: []business.AuditMetric{
			{Op: "sum", Field: "payload:price", Alias: "sum_price"},
			{Op: "min", Field: "payload:price", Alias: "min_price"},
		},
	})
	require.NoError(t, err)
	require.Len(t, agg, 1)
	require.Equal(t, int64(5), agg[0].Count)
	require.InDelta(t, 19.5, agg[0].Metrics["sum_price"], 0.001)
	require.InDelta(t, 2.5, agg[0].Metrics["min_price"], 0.001)

	// Derived ratios: present when both operands are defined and the denominator
	// is non-zero; omitted otherwise.
	derived, err := testService.AggregateAuditLog(ctx, q, business.AuditAggregationSpec{
		GroupBy: []string{"event_type"},
		Metrics: []business.AuditMetric{
			{Op: "count", Alias: "n"},
			{Op: "min", Field: "payload:price", Alias: "min_price"},
			{Op: "min", Field: "payload:missing", Alias: "min_missing"}, // no numeric rows → absent
			{Op: "sum", Field: "payload:label", Alias: "sum_label"},     // non-numeric → 0
		},
		Derived: []business.AuditDerivedMetric{
			{Alias: "ok_ratio", Numerator: "min_price", Denominator: "n"},     // 2.5 / 5
			{Alias: "absent_num", Numerator: "min_missing", Denominator: "n"}, // numerator absent
			{Alias: "zero_den", Numerator: "n", Denominator: "sum_label"},     // denominator 0
		},
	})
	require.NoError(t, err)
	require.Len(t, derived, 1)
	dm := derived[0].Metrics
	require.InDelta(t, 0.5, dm["ok_ratio"], 0.001)
	_, hasAbsent := dm["absent_num"]
	require.False(t, hasAbsent, "ratio with an absent operand must be omitted, not 0")
	_, hasZeroDen := dm["zero_den"]
	require.False(t, hasZeroDen, "ratio over a zero denominator must be omitted, not 0")
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
