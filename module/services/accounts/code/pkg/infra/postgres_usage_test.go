package infra_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

func TestConsumeUsageIsIdempotentAndEnforcesQuota(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	periodStart, periodEnd := usageTestPeriod(time.Now())
	occurredAt := periodStart.Add(time.Hour)

	first := usageTestConsumption(orgID, "usage-first", 7, 1, occurredAt, periodStart, periodEnd)
	var firstReceipt *business.UsageReceipt
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		firstReceipt, err = testStore.ConsumeUsage(ctx, first)
		return err
	}))
	require.True(t, firstReceipt.Accepted)
	require.False(t, firstReceipt.Duplicate)
	require.Equal(t, int64(7), firstReceipt.Used)

	// A retry may carry a newly generated candidate event ID; the persisted
	// receipt remains the first one and the aggregate is not incremented.
	retry := first
	retry.EventID = business.NewIDString()
	var retryReceipt *business.UsageReceipt
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		retryReceipt, err = testStore.ConsumeUsage(ctx, retry)
		return err
	}))
	require.True(t, retryReceipt.Duplicate)
	require.Equal(t, firstReceipt.EventID, retryReceipt.EventID)
	require.Equal(t, int64(7), retryReceipt.Used)

	conflict := retry
	conflict.RequestHash[0]++
	err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, err := testStore.ConsumeUsage(ctx, conflict)
		return err
	})
	require.ErrorIs(t, err, business.ErrUsageIdempotencyConflict)

	rejected := usageTestConsumption(orgID, "usage-over-limit", 4, 2, occurredAt, periodStart, periodEnd)
	var rejectedReceipt *business.UsageReceipt
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		rejectedReceipt, err = testStore.ConsumeUsage(ctx, rejected)
		return err
	}))
	require.False(t, rejectedReceipt.Accepted)
	require.Equal(t, int64(7), rejectedReceipt.Used)

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		used, err := testStore.GetUsageTotal(ctx, orgID, first.Meter, periodStart)
		require.NoError(t, err)
		require.Equal(t, int64(7), used)
		return nil
	}))
}

func TestConsumeUsageSerializesConcurrentQuotaChecks(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	periodStart, periodEnd := usageTestPeriod(time.Now())
	occurredAt := periodStart.Add(time.Hour)

	commands := []business.UsageConsumption{
		usageTestConsumption(orgID, "concurrent-a", 6, 3, occurredAt, periodStart, periodEnd),
		usageTestConsumption(orgID, "concurrent-b", 6, 4, occurredAt, periodStart, periodEnd),
	}
	receipts := make([]*business.UsageReceipt, len(commands))
	errs := make([]error, len(commands))
	var wg sync.WaitGroup
	for index := range commands {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			var receipt *business.UsageReceipt
			err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
				var consumeErr error
				receipt, consumeErr = testStore.ConsumeUsage(ctx, commands[index])
				return consumeErr
			})
			receipts[index] = receipt
			errs[index] = err
		}(index)
	}
	wg.Wait()

	accepted := 0
	for index := range receipts {
		require.NoError(t, errs[index])
		require.NotNil(t, receipts[index])
		if receipts[index].Accepted {
			accepted++
		}
	}
	require.Equal(t, 1, accepted, "only one six-unit operation may fit under a ten-unit cap")
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		used, err := testStore.GetUsageTotal(ctx, orgID, commands[0].Meter, periodStart)
		require.NoError(t, err)
		require.Equal(t, int64(6), used)
		return nil
	}))
}

func TestCardinalityQuotaLockRequiresTenantTransaction(t *testing.T) {
	err := testStore.LockEntitlementQuota(testCtx, business.NewIDString(), "seats")
	require.EqualError(t, err, "entitlement quota lock requires a tenant transaction")
}

func TestGetUsageBucketsReadsAcceptedEventsOnly(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	periodStart, periodEnd := usageTestPeriod(time.Now())
	first := usageTestConsumption(
		orgID, "history-first", 3, 11, periodStart.Add(time.Hour), periodStart, periodEnd,
	)
	rejected := usageTestConsumption(
		orgID, "history-rejected", 20, 12, periodStart.Add(2*time.Hour), periodStart, periodEnd,
	)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if _, err := testStore.ConsumeUsage(ctx, first); err != nil {
			return err
		}
		if _, err := testStore.ConsumeUsage(ctx, rejected); err != nil {
			return err
		}
		buckets, err := testStore.GetUsageBuckets(
			ctx, orgID, first.Meter, periodStart, periodStart.Add(3*time.Hour), business.UsageBucketHour,
		)
		require.NoError(t, err)
		require.Len(t, buckets, 1)
		require.Equal(t, int64(3), buckets[0].Quantity)
		require.Equal(t, periodStart.Add(time.Hour), buckets[0].Start)
		daily, err := testStore.GetUsageBuckets(
			ctx, orgID, first.Meter, periodStart, periodStart.AddDate(0, 0, 1), business.UsageBucketDay,
		)
		require.NoError(t, err)
		require.Len(t, daily, 1)
		require.Equal(t, periodStart, daily[0].Start)
		return nil
	}))
}

func usageTestConsumption(orgID, key string, quantity int64, hashByte byte, occurredAt, periodStart, periodEnd time.Time) business.UsageConsumption {
	var requestHash [32]byte
	for index := range requestHash {
		requestHash[index] = hashByte
	}
	return business.UsageConsumption{
		EventID: business.NewIDString(), OrgID: orgID, Meter: "api_calls_monthly",
		Quantity: quantity, IdempotencyKey: key, RequestHash: requestHash,
		OccurredAt: occurredAt, PeriodStart: periodStart, PeriodEnd: periodEnd,
		Dimensions: map[string]string{"source": "test"}, Limit: 10,
	}
}

func usageTestPeriod(at time.Time) (time.Time, time.Time) {
	at = at.UTC()
	start := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}
