package business_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// freePlanID resolves the seeded free plan's UUID. subscriptions.plan_id is a
// UUID FK to plans(id), so tests that assert an org is on the free plan need
// the id, not the "free" name.
func freePlanID(t *testing.T, ctx context.Context) string {
	t.Helper()
	var id string
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		free, err := testStore.GetPlanByName(ctx, "free")
		if err != nil {
			return err
		}
		require.NotNil(t, free, "expected seeded 'free' plan from migration 9")
		id = free.ID
		return nil
	}))
	return id
}

// subscriptionRowCount counts every subscription row for an org regardless of
// status, so a duplicate insert is observable rather than hidden behind the
// active-only LIMIT 1 read path.
func subscriptionRowCount(t *testing.T, ctx context.Context, orgID string) int {
	t.Helper()
	var count int
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared store transaction key
		return tx.QueryRow(ctx, `SELECT count(*) FROM subscriptions WHERE org_id = $1`, orgID).Scan(&count)
	}))
	return count
}

// TestNewOrganizationIsOnFreePlanWithChoosePlanSatisfied covers the primary
// creation path (Service.CreateOrganization): the org is never plan-less, and
// CHOOSE_PLAN is already complete without a POST /v1/billing/free-plan.
func TestNewOrganizationIsOnFreePlanWithChoosePlanSatisfied(t *testing.T) {
	clearData(t)
	ctx := testCtx

	userID, orgID := mustUserAndOrg(t, ctx, "freeplan-create@test.invalid", "freeplan-create", "Free Plan Org")
	wantPlanID := freePlanID(t, ctx)

	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		sub, err := testStore.GetSubscription(ctx, orgID)
		require.NoError(t, err)
		require.NotNil(t, sub, "a newly created org must not be plan-less")
		require.Equal(t, wantPlanID, sub.PlanID, "org is on the free plan")
		require.Equal(t, "active", sub.Status)
		require.Empty(t, sub.StripeSubscriptionID, "the free plan has no Stripe subscription id")
		return nil
	}))

	progress, err := testService.GetProgress(ctx, userID, orgID)
	require.NoError(t, err)
	require.True(t, progressStepCompleted(progress, "choose_plan"),
		"CHOOSE_PLAN is satisfied by the free plan attached at creation")
}

// TestSignupProvisionedOrganizationIsOnFreePlan covers the second creation
// path (ensureOrg inside the resolver). An org created through the signup flow
// gets the same free-plan treatment as one created through CreateOrganization.
func TestSignupProvisionedOrganizationIsOnFreePlan(t *testing.T) {
	clearData(t)
	ctx := testCtx

	authResp, err := authenticateFixture(ctx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "freeplan-signup",
		ProviderEmail: "freeplan-signup@test.invalid",
		EmailVerified: true,
		// A non-empty org_name routes a first-seen identity through
		// SignupIntent, which provisions the org via ensureOrg.
		Profile: map[string]string{"org_name": "Signup Free Org"},
	})
	require.NoError(t, err)

	identity, err := testService.JWTMinter().VerifyAccess(authResp.AccessToken)
	require.NoError(t, err)
	orgID := identity.OrgID.String()
	require.NotEqual(t, "00000000-0000-0000-0000-000000000000", orgID, "signup must land in an org")
	wantPlanID := freePlanID(t, ctx)

	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		sub, err := testStore.GetSubscription(ctx, orgID)
		require.NoError(t, err)
		require.NotNil(t, sub, "signup-provisioned org must not be plan-less")
		require.Equal(t, wantPlanID, sub.PlanID)
		require.Equal(t, "active", sub.Status)
		return nil
	}))
}

// TestEntitlementsResolveImmediatelyAfterCreation asserts entitlement checks
// pass at creation without a follow-up call — the free plan's seeded
// plan_entitlements are in effect straight away.
func TestEntitlementsResolveImmediatelyAfterCreation(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgID := mustUserAndOrg(t, ctx, "freeplan-ent@test.invalid", "freeplan-ent", "Entitlement Org")

	checker := business.NewDefaultEntitlementChecker(testStore)

	seats, err := checker.GetLimit(ctx, orgID, business.EntitlementSeats)
	require.NoError(t, err)
	require.Equal(t, int64(5), seats, "free plan seats limit is seeded by migration 9")

	hasSeats, err := checker.HasFeature(ctx, orgID, business.EntitlementSeats)
	require.NoError(t, err)
	require.True(t, hasSeats)

	hasSSO, err := checker.HasFeature(ctx, orgID, business.EntitlementSSO)
	require.NoError(t, err)
	require.False(t, hasSSO, "SSO is disabled (limit 0) on the free plan")
}

// TestSelectFreePlanOnFreeOrgIsIdempotentNoOp asserts POST /v1/billing/free-plan
// remains safe on an org that already holds the free plan: no error, and no
// second subscription row.
func TestSelectFreePlanOnFreeOrgIsIdempotentNoOp(t *testing.T) {
	clearData(t)
	ctx := testCtx

	userID, orgID := mustUserAndOrg(t, ctx, "freeplan-idem@test.invalid", "freeplan-idem", "Idempotent Org")

	require.Equal(t, 1, subscriptionRowCount(t, ctx, orgID), "creation attaches exactly one subscription")

	var originalID string
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		sub, err := testStore.GetSubscription(ctx, orgID)
		require.NoError(t, err)
		require.NotNil(t, sub)
		originalID = sub.ID
		return nil
	}))

	// Explicit free-plan selection twice — the endpoint's own contract.
	require.NoError(t, testService.SelectFreePlan(ctx, userID, orgID))
	require.NoError(t, testService.SelectFreePlan(ctx, userID, orgID))

	require.Equal(t, 1, subscriptionRowCount(t, ctx, orgID), "re-selecting free must not duplicate the subscription")

	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		sub, err := testStore.GetSubscription(ctx, orgID)
		require.NoError(t, err)
		require.NotNil(t, sub)
		require.Equal(t, originalID, sub.ID, "the original subscription row is untouched")
		return nil
	}))
}

// TestUpgradedOrgIsNotSilentlyDowngradedByFreePlanReselect asserts that once an
// org has upgraded away from free, re-calling the endpoint is rejected rather
// than silently reverting it to the free tier.
func TestUpgradedOrgIsNotSilentlyDowngradedByFreePlanReselect(t *testing.T) {
	clearData(t)
	ctx := testCtx

	userID, orgID := mustUserAndOrg(t, ctx, "freeplan-upgrade@test.invalid", "freeplan-upgrade", "Upgrade Org")

	var proPlanID string
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		pro, err := testStore.GetPlanByName(ctx, "pro")
		if err != nil {
			return err
		}
		require.NotNil(t, pro)
		proPlanID = pro.ID
		return nil
	}))

	// Upgrade the free subscription that creation attached to the pro plan,
	// as the Stripe webhook reconciliation would.
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		sub, err := testStore.GetSubscription(ctx, orgID)
		require.NoError(t, err)
		require.NotNil(t, sub)
		sub.PlanID = proPlanID
		sub.Status = "active"
		sub.StripeSubscriptionID = "sub_upgrade"
		return testStore.UpdateSubscription(ctx, sub)
	}))

	// Re-selecting free on a paid org must fail closed, not downgrade.
	require.Error(t, testService.SelectFreePlan(ctx, userID, orgID))

	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		sub, err := testStore.GetSubscription(ctx, orgID)
		require.NoError(t, err)
		require.NotNil(t, sub)
		require.Equal(t, proPlanID, sub.PlanID, "the org stays on the paid plan")
		return nil
	}))
}
