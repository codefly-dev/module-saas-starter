package business

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type catalogUsageStore struct {
	Store
	entitlementKeys []string
	transactionRuns int
}

func (s *catalogUsageStore) WithOrgTx(
	ctx context.Context,
	_ string,
	fn func(context.Context) error,
) error {
	s.transactionRuns++
	return fn(ctx)
}

func (s *catalogUsageStore) GetEntitlementOverride(
	context.Context,
	string,
	string,
) (*EntitlementOverride, error) {
	return nil, nil
}

func (s *catalogUsageStore) GetOrgPlanID(context.Context, string) (string, error) {
	return "plan-1", nil
}

func (s *catalogUsageStore) GetPlanEntitlement(
	_ context.Context,
	_ string,
	key string,
) (int64, error) {
	s.entitlementKeys = append(s.entitlementKeys, key)
	return 100, nil
}

func (s *catalogUsageStore) ConsumeUsage(
	_ context.Context,
	command UsageConsumption,
) (*UsageReceipt, error) {
	return &UsageReceipt{
		EventID: command.EventID, OrgID: command.OrgID, Meter: command.Meter,
		Quantity: command.Quantity, Accepted: true, Used: command.Quantity,
		Limit: command.Limit, PeriodStart: command.PeriodStart,
		PeriodEnd: command.PeriodEnd, OccurredAt: command.OccurredAt,
	}, nil
}

func (s *catalogUsageStore) GetUsageBuckets(
	context.Context,
	string,
	string,
	time.Time,
	time.Time,
	UsageBucketInterval,
) ([]UsageBucketValue, error) {
	return nil, nil
}

func TestConsumeUsageResolvesTheCatalogEntitlementLink(t *testing.T) {
	catalog := parseUsageCatalogForTest(t, "customer", "sum")
	store := &catalogUsageStore{}
	service := &Service{store: store, usageMeters: catalog}

	_, err := service.ConsumeUsage(t.Context(), ConsumeUsageInput{
		OrgID:          "11111111-1111-4111-8111-111111111111",
		Meter:          "api_operations",
		Quantity:       1,
		IdempotencyKey: "operation-1",
	})

	require.NoError(t, err)
	require.Equal(t, []string{"api_calls_monthly"}, store.entitlementKeys)
}

func TestUsageHistoryRejectsNonCustomerMetersBeforeStorage(t *testing.T) {
	catalog := parseUsageCatalogForTest(t, "operator", "sum")
	store := &catalogUsageStore{}
	service := &Service{store: store, usageMeters: catalog}
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	_, _, err := service.GetUsageHistory(
		t.Context(),
		"11111111-1111-4111-8111-111111111111",
		"api_operations",
		from,
		from.Add(24*time.Hour),
		UsageBucketDay,
	)

	require.ErrorIs(t, err, ErrUsageMeterNotFound)
	require.Zero(t, store.transactionRuns)
}

func parseUsageCatalogForTest(
	t *testing.T,
	visibility string,
	aggregation string,
) *UsageMeterCatalog {
	t.Helper()
	catalog, err := ParseUsageMeterCatalog([]byte(`{
		"version":1,
		"meters":[{
			"key":"api_operations",
			"display_name":"API operations",
			"unit":"request",
			"aggregation":"` + aggregation + `",
			"owner":"product",
			"source":"UsageService.ConsumeUsage",
			"entitlement_key":"api_calls_monthly",
			"reconciliation_rule":"accepted operations equal the aggregate",
			"visibility":"` + visibility + `"
		}]
	}`))
	require.NoError(t, err)
	return catalog
}
