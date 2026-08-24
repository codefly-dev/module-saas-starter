package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// TestRLS_WebhookSubscriptions_CrossTenantBlocked — proves Phase 2A
// RLS on webhook_subscriptions: org A's session cannot see org B's
// subscription rows even when the SQL has no org_id filter.
//
// Three-act test:
//  1. Seed: each org gets a subscription via WithControlPlane (skip Service
//     so we can write any orgID — the WithOrgTx-wrapped Service
//     paths only let you write to your own org, which is correct
//     but unhelpful for seed setup).
//  2. WithOrgTx as A: Service.ListSubscriptions returns A's row only.
//     ListWebhookSubscriptions called with org B's id from inside
//     A's tx returns nothing (RLS hides B's row from A).
//  3. Un-wrapped: zero rows visible (fail-closed).
func TestRLS_WebhookSubscriptions_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	orgA := mustOrgWithOwner(t, ctx, "alice-wh@rls-test.com", "alice-wh-rls", "Acme Wh A")
	orgB := mustOrgWithOwner(t, ctx, "bob-wh@rls-test.com", "bob-wh-rls", "Acme Wh B")

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		if err := testStore.CreateWebhookSubscription(ctx, &business.WebhookSubscription{
			ID: business.NewIDString(), OrgID: orgA, URL: "https://a.example.com/hook",
			SecretEncrypted: "encrypted:sa", Events: []string{"user.created"}, Active: true,
		}); err != nil {
			return err
		}
		return testStore.CreateWebhookSubscription(ctx, &business.WebhookSubscription{
			ID: business.NewIDString(), OrgID: orgB, URL: "https://b.example.com/hook",
			SecretEncrypted: "encrypted:sb", Events: []string{"user.created"}, Active: true,
		})
	}))

	// As A: see only A's sub.
	subsA, err := testService.ListSubscriptions(ctx, orgA)
	require.NoError(t, err)
	require.Len(t, subsA, 1, "org A must see exactly its own subscription")
	require.Equal(t, "https://a.example.com/hook", subsA[0].URL)

	// As B: see only B's sub.
	subsB, err := testService.ListSubscriptions(ctx, orgB)
	require.NoError(t, err)
	require.Len(t, subsB, 1)
	require.Equal(t, "https://b.example.com/hook", subsB[0].URL)

	// Cross-tenant probe: from inside A's tx, query B's
	// subscriptions. RLS hides them.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.ListWebhookSubscriptions(ctx, orgB)
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's rows from A's tx, even when SQL filters by B's org_id")
		return nil
	}))

	// Un-wrapped: app_tenant role + no app.current_org_id → zero
	// rows. Fail-closed.
	noWrap, err := testStore.ListWebhookSubscriptions(context.Background(), orgA)
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped ListWebhookSubscriptions must return ZERO rows (RLS fail-closed)")
}

// TestRLS_WebhookDeliveries_PolicyJoinsToParent — webhook_deliveries
// has no direct org_id; the policy walks subscription_id to
// webhook_subscriptions for tenant scope. This proves the JOIN
// policy fires the same way.
func TestRLS_WebhookDeliveries_PolicyJoinsToParent(t *testing.T) {
	clearData(t)
	ctx := testCtx

	orgA := mustOrgWithOwner(t, ctx, "alice-d@rls-test.com", "alice-d-rls", "Acme D A")
	orgB := mustOrgWithOwner(t, ctx, "bob-d@rls-test.com", "bob-d-rls", "Acme D B")

	subA := business.NewIDString()
	subB := business.NewIDString()
	delA := business.NewIDString()
	delB := business.NewIDString()

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		if err := testStore.CreateWebhookSubscription(ctx, &business.WebhookSubscription{
			ID: subA, OrgID: orgA, URL: "https://a.example.com/hook",
			SecretEncrypted: "encrypted:sa", Events: []string{"user.created"}, Active: true,
		}); err != nil {
			return err
		}
		if err := testStore.CreateWebhookSubscription(ctx, &business.WebhookSubscription{
			ID: subB, OrgID: orgB, URL: "https://b.example.com/hook",
			SecretEncrypted: "encrypted:sb", Events: []string{"user.created"}, Active: true,
		}); err != nil {
			return err
		}
		if err := testStore.CreateWebhookDelivery(ctx, &business.WebhookDelivery{
			ID: delA, SubscriptionID: subA, EventType: "user.created",
			Payload: `{}`, Status: "pending",
		}); err != nil {
			return err
		}
		return testStore.CreateWebhookDelivery(ctx, &business.WebhookDelivery{
			ID: delB, SubscriptionID: subB, EventType: "user.created",
			Payload: `{}`, Status: "pending",
		})
	}))

	// As A: GetWebhookDelivery for A's id succeeds, B's id returns nil.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		got, err := testStore.GetWebhookDelivery(ctx, delA)
		require.NoError(t, err)
		require.NotNil(t, got, "A's tx should see A's delivery")

		stolen, err := testStore.GetWebhookDelivery(ctx, delB)
		require.NoError(t, err)
		require.Nil(t, stolen, "RLS JOIN policy must hide B's delivery from A's tx")
		return nil
	}))
}

// mustOrgWithOwner registers a user and creates an organization they own,
// returning the org id. Shared by the RLS cross-tenant tests to seed two
// isolated tenants. (Previously lived in rls_audit_export_test.go.)
func mustOrgWithOwner(t *testing.T, ctx context.Context, email, providerID, orgName string) string {
	t.Helper()
	resp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: email,
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: providerID, ProviderEmail: email,
		},
	})
	require.NoError(t, err)
	org, err := testService.CreateOrganization(ctx, resp.User.Uuid, &gen.CreateOrganizationRequest{
		Name: orgName, Slug: providerID + "-org",
	})
	require.NoError(t, err)
	return org.Organization.Id
}
