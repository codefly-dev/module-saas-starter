package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// freePlanStore is an organization on the default free plan (the baseline
// migration's values) that has used every seat and API key it allows.
type freePlanStore struct {
	usageStoreFake
	members int
	pending int32
	keys    int64
}

func newFreePlanStore() *freePlanStore {
	return &freePlanStore{members: 5, keys: 2}
}

var freePlan = map[string]int64{
	business.EntitlementSeats:   5,
	business.EntitlementAPIKeys: 2,
	"api_calls_monthly":         10000,
	"sso":                       0,
	"audit_log":                 0,
}

func (f *freePlanStore) GetPlanEntitlement(_ context.Context, _ string, feature string) (int64, error) {
	return freePlan[feature], nil
}

func (f *freePlanStore) LockEntitlementQuota(context.Context, string, string) error { return nil }

func (f *freePlanStore) ListOrgMembers(context.Context, string) ([]*gen.OrgMembership, error) {
	return make([]*gen.OrgMembership, f.members), nil
}

func (f *freePlanStore) CountPendingInvitations(context.Context, string) (int32, error) {
	return f.pending, nil
}

func (f *freePlanStore) CountActiveAPIKeys(context.Context, string) (int64, error) {
	return f.keys, nil
}

const planPolicyOrg = "11111111-1111-4111-8111-111111111111"

func TestPlanPolicyFollowsTheBillingProvider(t *testing.T) {
	require.Equal(t, business.PlansSold, business.PlanPolicyForBillingProvider("stripe"))
	for _, provider := range []string{"", "disabled"} {
		require.Equal(t, business.NoPlansSold, business.PlanPolicyForBillingProvider(provider), provider)
	}
}

// A deployment that sells no plans holds an organization to none: the sixth
// seat and the third API key are admitted past the free plan's 5 and 2.
func TestNoPlansSoldAdmitsPastTheFreePlanCardinality(t *testing.T) {
	checker := business.NewDefaultEntitlementChecker(newFreePlanStore(), business.NoPlansSold)
	for _, feature := range []string{business.EntitlementSeats, business.EntitlementAPIKeys} {
		quota, err := checker.CheckCardinalityQuotaInTx(context.Background(), planPolicyOrg, feature)
		require.NoError(t, err)
		require.Equal(t, int64(-1), quota.Limit, feature)
		require.NoError(t, quota.RequireAvailable(), feature)
	}
}

func TestPlansSoldRefusesPastTheFreePlanCardinality(t *testing.T) {
	checker := business.NewDefaultEntitlementChecker(newFreePlanStore(), business.PlansSold)
	for feature, used := range map[string]int64{business.EntitlementSeats: 5, business.EntitlementAPIKeys: 2} {
		quota, err := checker.CheckCardinalityQuotaInTx(context.Background(), planPolicyOrg, feature)
		require.NoError(t, err)
		err = quota.RequireAvailable()
		require.ErrorIs(t, err, business.ErrEntitlementQuotaExceeded, feature)
		var refusal *business.EntitlementQuotaError
		require.ErrorAs(t, err, &refusal)
		require.Equal(t, used, refusal.Used)
		require.Equal(t, used, refusal.Limit)
	}
}

// A feature the free plan disables (0) is not switched off on a deployment
// that sells no plans; on one that does, the plan still decides.
func TestNoPlansSoldDisablesNoFeature(t *testing.T) {
	ctx := context.Background()
	unsold := business.NewDefaultEntitlementChecker(newFreePlanStore(), business.NoPlansSold)
	sold := business.NewDefaultEntitlementChecker(newFreePlanStore(), business.PlansSold)
	for _, feature := range []string{"sso", "audit_log"} {
		enabled, err := unsold.HasFeature(ctx, planPolicyOrg, feature)
		require.NoError(t, err)
		require.True(t, enabled, feature)
		limit, err := unsold.GetLimit(ctx, planPolicyOrg, feature)
		require.NoError(t, err)
		require.Equal(t, int64(-1), limit, feature)

		enabled, err = sold.HasFeature(ctx, planPolicyOrg, feature)
		require.NoError(t, err)
		require.False(t, enabled, feature)
	}
}

// The meter is unlimited too: the ledger records the consumption with no cap,
// and the usage read-out reports none, past the free plan's 10000 calls.
func TestNoPlansSoldMetersWithoutACap(t *testing.T) {
	store := newFreePlanStore()
	store.used = 10000
	service, err := business.NewService(store)
	require.NoError(t, err)
	service.SetEntitlementChecker(business.NewDefaultEntitlementChecker(store, business.NoPlansSold))

	receipt, err := service.ConsumeUsage(context.Background(), business.ConsumeUsageInput{
		OrgID: planPolicyOrg, Meter: "api_calls_monthly", Quantity: 1, IdempotencyKey: "call:10001",
	})
	require.NoError(t, err)
	require.Equal(t, int64(-1), store.commands[0].Limit)
	require.True(t, receipt.Accepted)

	snapshot, err := service.GetUsage(context.Background(), planPolicyOrg, "api_calls_monthly")
	require.NoError(t, err)
	require.Equal(t, int64(-1), snapshot.Limit)
}

func TestPlansSoldMetersAgainstThePlan(t *testing.T) {
	store := newFreePlanStore()
	service, err := business.NewService(store)
	require.NoError(t, err)
	service.SetEntitlementChecker(business.NewDefaultEntitlementChecker(store, business.PlansSold))

	_, err = service.ConsumeUsage(context.Background(), business.ConsumeUsageInput{
		OrgID: planPolicyOrg, Meter: "api_calls_monthly", Quantity: 1, IdempotencyKey: "call:1",
	})
	require.NoError(t, err)
	require.Equal(t, int64(10000), store.commands[0].Limit)
}
