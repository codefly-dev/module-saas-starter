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
		OrgID: "11111111-1111-4111-8111-111111111111", Meter: "jobs_monthly",
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
		OrgID: "11111111-1111-4111-8111-111111111111", Meter: "jobs_monthly",
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
		{name: "missing organization", input: business.ConsumeUsageInput{Meter: "jobs_monthly", Quantity: 1, IdempotencyKey: "op:1"}},
		{name: "noncanonical meter", input: business.ConsumeUsageInput{OrgID: "org", Meter: "Jobs", Quantity: 1, IdempotencyKey: "op:1"}},
		{name: "nonpositive quantity", input: business.ConsumeUsageInput{OrgID: "org", Meter: "jobs_monthly", Quantity: 0, IdempotencyKey: "op:1"}},
		{name: "missing idempotency key", input: business.ConsumeUsageInput{OrgID: "org", Meter: "jobs_monthly", Quantity: 1}},
		{name: "invalid dimension", input: business.ConsumeUsageInput{OrgID: "org", Meter: "jobs_monthly", Quantity: 1, IdempotencyKey: "op:1", Dimensions: map[string]string{"Not Canonical": "value"}}},
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
		context.Background(), "11111111-1111-4111-8111-111111111111", "jobs_monthly",
	)
	require.NoError(t, err)
	require.Equal(t, int64(42), snapshot.Used)
	require.Equal(t, int64(-1), snapshot.Limit)
	require.True(t, snapshot.PeriodStart.Before(snapshot.PeriodEnd))
	require.Equal(t, time.UTC, snapshot.PeriodStart.Location())
}
