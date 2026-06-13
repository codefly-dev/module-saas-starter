package business_test

// Phase 2B cross-tenant tests. The migration in 29_rls_direct_org
// added RLS to six "direct org_id" tables but landed without any
// dedicated cross-tenant tests. This file is the backfill — one test
// per table, asserting:
//
//   1. Org A's tx sees only A's row(s).
//   2. From inside A's tx, a query for B's id returns zero rows
//      (RLS hides them, even when the SQL filters by B's org_id).
//   3. An un-wrapped Store call returns ZERO rows (fail-closed via
//      BeforeAcquire SET ROLE app_tenant).
//
// Same shape as rls_audit_export_test.go / rls_webhooks_test.go —
// changes here probably need to land alongside changes there.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"api/pkg/business"
	"api/pkg/gen"
)

// TestRLS_OrgSettings_CrossTenantBlocked — org_settings has direct
// org_id; row is the org's branding (logo, color, custom domain).
func TestRLS_OrgSettings_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-os@rls-test.com", "alice-os-rls", "Acme OS A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-os@rls-test.com", "bob-os-rls", "Acme OS B")

	require.NoError(t, testStore.WithBypass(ctx, func(ctx context.Context) error {
		if err := testStore.UpsertOrgSettings(ctx, &business.OrgSettings{
			OrgID: orgA, PrimaryColor: "#aaaaaa", LogoURL: "https://a/logo.png",
		}); err != nil {
			return err
		}
		return testStore.UpsertOrgSettings(ctx, &business.OrgSettings{
			OrgID: orgB, PrimaryColor: "#bbbbbb", LogoURL: "https://b/logo.png",
		})
	}))

	// As A: see A's row.
	gotA, err := testService.GetOrgSettings(ctx, orgA)
	require.NoError(t, err)
	require.Equal(t, "#aaaaaa", gotA.PrimaryColor)

	// Cross-tenant probe: from A's tx, ask Store for B's row.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.GetOrgSettings(ctx, orgB)
		require.NoError(t, err)
		require.Nil(t, stolen, "RLS must hide B's org_settings from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows.
	noWrap, err := testStore.GetOrgSettings(context.Background(), orgA)
	require.NoError(t, err)
	require.Nil(t, noWrap, "un-wrapped GetOrgSettings must return nil (RLS fail-closed)")
}

// TestRLS_Invitations_CrossTenantBlocked — invitations has direct
// org_id; cross-org pending invitations must stay invisible.
func TestRLS_Invitations_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	userA, orgA := mustUserAndOrg(t, ctx, "alice-inv@rls-test.com", "alice-inv-rls", "Acme INV A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-inv@rls-test.com", "bob-inv-rls", "Acme INV B")

	// CreateInvitation goes through Service (WithOrgTx-wrapped).
	_, err := testService.CreateInvitation(ctx, userA, &gen.CreateInvitationRequest{
		OrgId: orgA, Email: "newhire-a@example.com", Role: "member",
	})
	require.NoError(t, err)
	_, err = testService.CreateInvitation(ctx, userA /* fixture, not real auth */, &gen.CreateInvitationRequest{
		OrgId: orgB, Email: "newhire-b@example.com", Role: "member",
	})
	require.NoError(t, err)

	// As A: see A's invitations only.
	listA, err := testService.ListInvitations(ctx, &gen.ListInvitationsRequest{OrgId: orgA})
	require.NoError(t, err)
	require.Len(t, listA.Invitations, 1)
	require.Equal(t, "newhire-a@example.com", listA.Invitations[0].Email)

	// Cross-tenant probe: from A's tx, ListInvitations for B → zero.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.ListInvitations(ctx, orgB, "")
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's invitations from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows.
	noWrap, err := testStore.ListInvitations(context.Background(), orgA, "")
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped ListInvitations must return ZERO rows (RLS fail-closed)")
}

// TestRLS_OrganizationMembers_CrossTenantBlocked — organization_members
// is the join table (org_id, user_id, role). Each org's membership list
// must be invisible to other tenants.
func TestRLS_OrganizationMembers_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-om@rls-test.com", "alice-om-rls", "Acme OM A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-om@rls-test.com", "bob-om-rls", "Acme OM B")

	// Each owner is auto-added to their own org as 'owner' by
	// CreateOrganization. So both orgs have exactly 1 member.

	// Cross-tenant probe: from A's tx, ListOrgMembers(B) returns
	// nothing (RLS hides B's row).
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.ListOrgMembers(ctx, orgB)
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's org_members from A's tx")
		return nil
	}))

	// As A's scope, listing A's members works.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		mine, err := testStore.ListOrgMembers(ctx, orgA)
		require.NoError(t, err)
		require.Len(t, mine, 1, "org A should see its own owner row")
		return nil
	}))

	// Un-wrapped: zero rows (fail-closed).
	noWrap, err := testStore.ListOrgMembers(context.Background(), orgA)
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped ListOrgMembers must return ZERO rows (RLS fail-closed)")
}

// TestRLS_Subscriptions_CrossTenantBlocked — subscriptions has direct
// org_id (links org→plan). One row per org max.
func TestRLS_Subscriptions_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-sub@rls-test.com", "alice-sub-rls", "Acme SUB A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-sub@rls-test.com", "bob-sub-rls", "Acme SUB B")

	// Resolve the seeded "free" plan's UUID — subscriptions.plan_id
	// is a UUID FK to plans(id), not the plan name.
	var freePlanID, proPlanID string
	require.NoError(t, testStore.WithBypass(ctx, func(ctx context.Context) error {
		free, err := testStore.GetPlanByName(ctx, "free")
		if err != nil {
			return err
		}
		require.NotNil(t, free, "expected seeded 'free' plan from migration 9")
		freePlanID = free.ID
		pro, err := testStore.GetPlanByName(ctx, "pro")
		if err != nil {
			return err
		}
		require.NotNil(t, pro, "expected seeded 'pro' plan from migration 9")
		proPlanID = pro.ID
		return nil
	}))

	now := time.Now()
	end := now.Add(30 * 24 * time.Hour)
	require.NoError(t, testStore.WithBypass(ctx, func(ctx context.Context) error {
		if err := testStore.CreateSubscription(ctx, &business.Subscription{
			ID: business.NewIDString(), OrgID: orgA, PlanID: freePlanID, Status: "active",
			StripeSubscriptionID: "sub_a", CurrentPeriodStart: &now, CurrentPeriodEnd: &end,
		}); err != nil {
			return err
		}
		return testStore.CreateSubscription(ctx, &business.Subscription{
			ID: business.NewIDString(), OrgID: orgB, PlanID: proPlanID, Status: "active",
			StripeSubscriptionID: "sub_b", CurrentPeriodStart: &now, CurrentPeriodEnd: &end,
		})
	}))

	// As A's tx: GetSubscription(orgA) succeeds, GetSubscription(orgB) returns nil.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		mine, err := testStore.GetSubscription(ctx, orgA)
		require.NoError(t, err)
		require.NotNil(t, mine, "org A should see its own subscription")
		require.Equal(t, "sub_a", mine.StripeSubscriptionID)

		stolen, err := testStore.GetSubscription(ctx, orgB)
		require.NoError(t, err)
		require.Nil(t, stolen, "RLS must hide B's subscription from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows.
	noWrap, err := testStore.GetSubscription(context.Background(), orgA)
	require.NoError(t, err)
	require.Nil(t, noWrap, "un-wrapped GetSubscription must return nil (RLS fail-closed)")
}

// TestRLS_EntitlementOverrides_CrossTenantBlocked — entitlement_overrides
// records per-org limit overrides applied by platform admin.
func TestRLS_EntitlementOverrides_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	// CreatedBy is a UUID FK to users — use each org's owner so
	// the FK passes.
	userA, orgA := mustUserAndOrg(t, ctx, "alice-eo@rls-test.com", "alice-eo-rls", "Acme EO A")
	userB, orgB := mustUserAndOrg(t, ctx, "bob-eo@rls-test.com", "bob-eo-rls", "Acme EO B")

	limitA := int64(50)
	limitB := int64(100)
	require.NoError(t, testStore.WithBypass(ctx, func(ctx context.Context) error {
		if err := testStore.CreateEntitlementOverride(ctx, &business.EntitlementOverride{
			ID: business.NewIDString(), OrgID: orgA, Feature: "seats",
			LimitValue: &limitA, Reason: "promo", CreatedBy: userA,
		}); err != nil {
			return err
		}
		return testStore.CreateEntitlementOverride(ctx, &business.EntitlementOverride{
			ID: business.NewIDString(), OrgID: orgB, Feature: "seats",
			LimitValue: &limitB, Reason: "promo", CreatedBy: userB,
		})
	}))

	// Cross-tenant probe.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		mine, err := testStore.ListEntitlementOverrides(ctx, orgA)
		require.NoError(t, err)
		require.Len(t, mine, 1)
		require.Equal(t, int64(50), *mine[0].LimitValue)

		stolen, err := testStore.ListEntitlementOverrides(ctx, orgB)
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's overrides from A's tx")

		// GetEntitlementOverride is also tenant-scoped.
		stolenGet, err := testStore.GetEntitlementOverride(ctx, orgB, "seats")
		require.NoError(t, err)
		require.Nil(t, stolenGet, "RLS must hide B's seats override from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows.
	noWrap, err := testStore.ListEntitlementOverrides(context.Background(), orgA)
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped ListEntitlementOverrides must return ZERO rows (RLS fail-closed)")
}

// TestRLS_UsageRecords_CrossTenantBlocked — usage_records is the
// metered-feature counter (org_id, feature, period, quantity).
func TestRLS_UsageRecords_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-ur@rls-test.com", "alice-ur-rls", "Acme UR A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-ur@rls-test.com", "bob-ur-rls", "Acme UR B")

	const period = "2026-04"
	require.NoError(t, testStore.WithBypass(ctx, func(ctx context.Context) error {
		if err := testStore.RecordUsage(ctx, orgA, "api_calls", 50, period); err != nil {
			return err
		}
		return testStore.RecordUsage(ctx, orgB, "api_calls", 200, period)
	}))

	// As A's tx: see A's count, B's returns 0.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		mine, err := testStore.GetUsageForPeriod(ctx, orgA, "api_calls", period)
		require.NoError(t, err)
		require.Equal(t, int64(50), mine, "org A should see its own usage")

		stolen, err := testStore.GetUsageForPeriod(ctx, orgB, "api_calls", period)
		require.NoError(t, err)
		require.Equal(t, int64(0), stolen, "RLS must hide B's usage from A's tx")
		return nil
	}))

	// Un-wrapped: zero.
	noWrap, err := testStore.GetUsageForPeriod(context.Background(), orgA, "api_calls", period)
	require.NoError(t, err)
	require.Equal(t, int64(0), noWrap,
		"un-wrapped GetUsageForPeriod must return 0 (RLS fail-closed)")
}
