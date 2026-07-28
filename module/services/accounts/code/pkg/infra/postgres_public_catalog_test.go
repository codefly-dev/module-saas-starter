package infra_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestListPublicPlansUsesAuthoritativeCatalog(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		plans, err := testStore.ListPublicPlans(ctx)
		require.NoError(t, err)
		require.Empty(t, plans)

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
		require.Len(t, plans, 1)
		require.Equal(t, "pro", plans[0].Key)
		require.Equal(t, int64(12900), plans[0].AmountMinor)
		require.Equal(t, "USD", plans[0].Currency)
		require.Equal(t, "month", plans[0].Interval)
		require.True(t, plans[0].CheckoutEnabled)
		require.False(t, plans[0].Fixture)
		require.Equal(t, 14, plans[0].TrialDays)
		require.NotEmpty(t, plans[0].Entitlements)

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
