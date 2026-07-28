package infra_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListPublicPlansUsesAuthoritativeCatalog(t *testing.T) {
	plans, err := testStore.ListPublicPlans(testCtx)
	require.NoError(t, err)
	require.Len(t, plans, 3)
	require.Equal(t, []string{"free", "pro", "enterprise"}, []string{
		plans[0].Key,
		plans[1].Key,
		plans[2].Key,
	})

	require.Equal(t, int64(0), plans[0].AmountMinor)
	require.Equal(t, "USD", plans[0].Currency)
	require.Equal(t, "month", plans[0].Interval)
	require.False(t, plans[0].CheckoutEnabled)
	require.True(t, plans[0].Fixture)
	require.NotEmpty(t, plans[0].Entitlements)

	require.Equal(t, int64(4900), plans[1].AmountMinor)
	require.Equal(t, 14, plans[1].TrialDays)
	require.False(t, plans[1].CheckoutEnabled)

	require.Equal(t, "contact", plans[2].Interval)
	require.True(t, plans[2].ContactSales)
	require.False(t, plans[2].CheckoutEnabled)
}
