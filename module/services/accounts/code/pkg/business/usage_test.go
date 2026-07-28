package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

type usageStoreFake struct {
	business.Store
	commands []business.UsageConsumption
	buckets  []business.UsageBucketValue
	used     int64
	limit    int64
}

func (f *usageStoreFake) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *usageStoreFake) GetEntitlementOverride(context.Context, string, string) (*business.EntitlementOverride, error) {
	return nil, nil
}

func (f *usageStoreFake) GetOrgPlanID(context.Context, string) (string, error) {
	return "plan-1", nil
}

func (f *usageStoreFake) GetPlanEntitlement(context.Context, string, string) (int64, error) {
	return f.limit, nil
}

func (f *usageStoreFake) ConsumeUsage(_ context.Context, command business.UsageConsumption) (*business.UsageReceipt, error) {
	f.commands = append(f.commands, command)
	f.used += command.Quantity
	return &business.UsageReceipt{
		EventID: command.EventID, OrgID: command.OrgID, Meter: command.Meter,
		Quantity: command.Quantity, Accepted: true, Used: f.used,
		Limit: command.Limit, PeriodStart: command.PeriodStart,
		PeriodEnd: command.PeriodEnd, OccurredAt: command.OccurredAt,
	}, nil
}

func (f *usageStoreFake) GetUsageTotal(context.Context, string, string, time.Time) (int64, error) {
	return f.used, nil
}

func (f *usageStoreFake) GetUsageBuckets(
	context.Context,
	string,
	string,
	time.Time,
	time.Time,
	business.UsageBucketInterval,
) ([]business.UsageBucketValue, error) {
	return f.buckets, nil
}

func newUsageService(t *testing.T, store business.Store) *business.Service {
	t.Helper()
	service, err := business.NewService(store)
	require.NoError(t, err)
	return service
}

func TestConsumeUsageBuildsStableCanonicalCommand(t *testing.T) {
	store := &usageStoreFake{limit: 25}
	service := newUsageService(t, store)
	// This is still April in UTC, which pins that bucketing is not based on the
	// caller's local calendar month.
	eventTime := time.Date(2026, time.May, 1, 1, 30, 0, 0, time.FixedZone("UTC+2", 2*60*60))
	dimensions := map[string]string{"region": "us-east", "source.service": "product"}

	first, err := service.ConsumeUsage(context.Background(), business.ConsumeUsageInput{
		OrgID: "11111111-1111-4111-8111-111111111111", Meter: "api_calls_monthly",
		Quantity: 3, IdempotencyKey: "job:1", OccurredAt: &eventTime,
		Dimensions: dimensions,
	})
	require.NoError(t, err)
	require.True(t, first.Accepted)
	require.Equal(t, int64(25), first.Limit)
	require.Equal(t, time.April, first.PeriodStart.Month())
	require.Equal(t, time.UTC, first.PeriodStart.Location())
	require.Equal(t, time.May, first.PeriodEnd.Month())

	// A caller cannot mutate persisted command metadata after the call.
	dimensions["region"] = "mutated"
	require.Equal(t, "us-east", store.commands[0].Dimensions["region"])

	// Map insertion order and idempotency-key choice are not semantic payload
	// differences, so independently built retries produce the same request hash.
	_, err = service.ConsumeUsage(context.Background(), business.ConsumeUsageInput{
		OrgID: "11111111-1111-4111-8111-111111111111", Meter: "api_calls_monthly",
		Quantity: 3, IdempotencyKey: "job:2", OccurredAt: &eventTime,
		Dimensions: map[string]string{"source.service": "product", "region": "us-east"},
	})
	require.NoError(t, err)
	require.Equal(t, store.commands[0].RequestHash, store.commands[1].RequestHash)
}

func TestConsumeUsageRejectsInvalidDomainInputBeforeStorage(t *testing.T) {
	tests := []struct {
		name  string
		input business.ConsumeUsageInput
	}{
		{name: "missing organization", input: business.ConsumeUsageInput{Meter: "api_calls_monthly", Quantity: 1, IdempotencyKey: "op:1"}},
		{name: "noncanonical meter", input: business.ConsumeUsageInput{OrgID: "org", Meter: "Jobs", Quantity: 1, IdempotencyKey: "op:1"}},
		{name: "unregistered meter", input: business.ConsumeUsageInput{OrgID: "org", Meter: "jobs_monthly", Quantity: 1, IdempotencyKey: "op:1"}},
		{name: "nonpositive quantity", input: business.ConsumeUsageInput{OrgID: "org", Meter: "api_calls_monthly", Quantity: 0, IdempotencyKey: "op:1"}},
		{name: "missing idempotency key", input: business.ConsumeUsageInput{OrgID: "org", Meter: "api_calls_monthly", Quantity: 1}},
		{name: "invalid dimension", input: business.ConsumeUsageInput{OrgID: "org", Meter: "api_calls_monthly", Quantity: 1, IdempotencyKey: "op:1", Dimensions: map[string]string{"Not Canonical": "value"}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &usageStoreFake{limit: 10}
			_, err := newUsageService(t, store).ConsumeUsage(context.Background(), test.input)
			require.Error(t, err)
			require.Empty(t, store.commands)
		})
	}
}

func TestGetUsageReturnsCurrentAggregateAndEffectiveLimit(t *testing.T) {
	store := &usageStoreFake{limit: -1, used: 42}
	snapshot, err := newUsageService(t, store).GetUsage(
		context.Background(), "11111111-1111-4111-8111-111111111111", "api_calls_monthly",
	)
	require.NoError(t, err)
	require.Equal(t, int64(42), snapshot.Used)
	require.Equal(t, int64(-1), snapshot.Limit)
	require.True(t, snapshot.PeriodStart.Before(snapshot.PeriodEnd))
	require.Equal(t, time.UTC, snapshot.PeriodStart.Location())
}

func TestUsageHistoryFillsEmptyUTCBucketsAndPreservesPartialRange(t *testing.T) {
	from := time.Date(2026, time.March, 28, 22, 30, 0, 0, time.FixedZone("UTC+2", 2*60*60))
	to := from.Add(3 * time.Hour)
	firstUTCBucket := from.UTC().Truncate(time.Hour)
	store := &usageStoreFake{buckets: []business.UsageBucketValue{
		{Start: firstUTCBucket.Add(time.Hour), Quantity: 7},
	}}
	history, observedAt, err := newUsageService(t, store).GetUsageHistory(
		context.Background(),
		"11111111-1111-4111-8111-111111111111",
		"api_calls_monthly",
		from,
		to,
		business.UsageBucketHour,
	)
	require.NoError(t, err)
	require.Len(t, history.Values, 4)
	require.Equal(t, from.UTC(), history.Values[0].Start)
	require.Equal(t, int64(0), history.Values[0].Quantity)
	require.Equal(t, int64(7), history.Values[1].Quantity)
	require.Equal(t, to.UTC(), history.Values[3].End)
	require.Equal(t, int64(7), history.Total)
	require.Equal(t, time.UTC, observedAt.Location())
}

func TestUsageHistoryRejectsUnboundedRanges(t *testing.T) {
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, _, err := newUsageService(t, &usageStoreFake{}).GetUsageHistory(
		context.Background(),
		"11111111-1111-4111-8111-111111111111",
		"api_calls_monthly",
		from,
		from.Add(45*24*time.Hour),
		business.UsageBucketHour,
	)
	require.ErrorContains(t, err, "more than 1000 buckets")
}

func TestDefaultUsageMeterCatalogIsComplete(t *testing.T) {
	catalog, err := business.DefaultUsageMeterCatalog()
	require.NoError(t, err)
	meters := catalog.Definitions(business.UsageVisibilityCustomer)
	require.Len(t, meters, 1)
	require.Equal(t, "api_calls_monthly", meters[0].Key)
	require.Equal(t, "api_calls_monthly", meters[0].EntitlementKey)
	require.NotEmpty(t, meters[0].ReconciliationRule)
}

func TestUsageMeterCatalogRejectsMissingReconciliationRule(t *testing.T) {
	_, err := business.ParseUsageMeterCatalog([]byte(`{
		"version":1,
		"meters":[{
			"key":"api_calls_monthly",
			"display_name":"API calls",
			"unit":"request",
			"aggregation":"sum",
			"owner":"product",
			"source":"UsageService.ConsumeUsage",
			"entitlement_key":"api_calls_monthly",
			"reconciliation_rule":"",
			"visibility":"customer"
		}]
	}`))
	require.ErrorContains(t, err, "missing required metadata")
}

func TestUsageMeterCatalogRejectsUnsupportedAggregation(t *testing.T) {
	_, err := business.ParseUsageMeterCatalog([]byte(`{
		"version":1,
		"meters":[{
			"key":"concurrent_jobs",
			"display_name":"Concurrent jobs",
			"unit":"job",
			"aggregation":"max",
			"owner":"product",
			"source":"UsageService.ConsumeUsage",
			"entitlement_key":"concurrent_jobs",
			"reconciliation_rule":"maximum concurrent jobs equals the aggregate",
			"visibility":"customer"
		}]
	}`))
	require.ErrorContains(t, err, "aggregation must be sum")
}
