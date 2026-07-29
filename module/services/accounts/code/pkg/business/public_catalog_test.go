package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

type publicCatalogStore struct {
	business.Store
	plans []business.PublicPlan
}

func (store *publicCatalogStore) ListPublicPlans(context.Context) ([]business.PublicPlan, error) {
	return append([]business.PublicPlan(nil), store.plans...), nil
}

func TestPublicPlanCatalogRevisionCoversCheckoutFields(t *testing.T) {
	store := &publicCatalogStore{plans: []business.PublicPlan{{
		Key: "pro", Name: "Pro", Description: "Growing teams", Currency: "EUR",
		AmountMinor: 4900, Interval: "month", CheckoutEnabled: true,
		TrialDays: 14, TaxBehavior: "automatic",
		Entitlements: []business.PlanFeatureLimit{{Feature: "seats", Limit: 50}},
	}}}
	service, err := business.NewService(store)
	require.NoError(t, err)

	plans, revision, err := service.ListPublicPlans(context.Background())
	require.NoError(t, err)
	require.Equal(t, store.plans, plans)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, revision)

	store.plans[0].AmountMinor = 9900
	_, changed, err := service.ListPublicPlans(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, revision, changed)
}
