//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestListPublicPlansUsesAuthoritativeCatalog(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		// ListPublicPlans scans the whole plans table, so every assertion here
		// is about the 'pro' row this test configures — a database where some
		// other plan was published says nothing about the catalog query.
		plans, err := testStore.ListPublicPlans(ctx)
		require.NoError(t, err)
		require.NotContains(t, publicPlanKeys(plans), "pro")

		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // test uses the scoped control-plane transaction
		_, err = tx.Exec(ctx, `
			UPDATE plans
			SET description = 'Configured plan',
			    amount_minor = 12900,
			    billing_interval = 'month',
			    public_visible = TRUE,
			    fixture = FALSE,
			    stripe_price_id = 'price_configured_pro',
			    updated_at = CURRENT_TIMESTAMP
			WHERE name = 'pro'`)
		require.NoError(t, err)

		plans, err = testStore.ListPublicPlans(ctx)
		require.NoError(t, err)
		pro := publicPlan(t, plans, "pro")
		require.Equal(t, int64(12900), pro.AmountMinor)
		require.Equal(t, "USD", pro.Currency)
		require.Equal(t, "month", pro.Interval)
		require.True(t, pro.CheckoutEnabled)
		require.False(t, pro.Fixture)
		require.Equal(t, 14, pro.TrialDays)
		require.NotEmpty(t, pro.Entitlements)

		_, err = tx.Exec(ctx, `
			UPDATE plans
			SET description = 'Development fixture plan. Replace before launch.',
			    amount_minor = NULL,
			    billing_interval = 'month',
			    public_visible = FALSE,
			    fixture = TRUE,
			    stripe_price_id = NULL,
			    updated_at = CURRENT_TIMESTAMP
			WHERE name = 'pro'`)
		return err
	}))
}

func publicPlanKeys(plans []business.PublicPlan) []string {
	keys := make([]string, 0, len(plans))
	for _, plan := range plans {
		keys = append(keys, plan.Key)
	}
	return keys
}

func publicPlan(t *testing.T, plans []business.PublicPlan, key string) business.PublicPlan {
	t.Helper()
	for _, plan := range plans {
		if plan.Key == key {
			return plan
		}
	}
	require.FailNowf(t, "plan is not publicly listed", "%q not in %v", key, publicPlanKeys(plans))
	return business.PublicPlan{}
}
