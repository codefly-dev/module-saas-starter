package business_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/billing"
	"accounts/pkg/business"
)

type billingStoreFake struct {
	business.Store
	plan       *business.PlanFull
	customerID string
}

func (f *billingStoreFake) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *billingStoreFake) GetPlanByName(_ context.Context, _ string) (*business.PlanFull, error) {
	if f.plan == nil {
		return nil, nil
	}
	copy := *f.plan
	return &copy, nil
}

func (f *billingStoreFake) GetOrgStripeCustomerID(context.Context, string) (string, error) {
	return f.customerID, nil
}

type billingClientFake struct {
	checkoutCalls []billing.CheckoutParams
	portalCalls   []portalCall
}

type portalCall struct {
	customerID     string
	returnURL      string
	idempotencyKey string
}

func (f *billingClientFake) CreateCustomer(context.Context, string, string, string) (*billing.Customer, error) {
	return &billing.Customer{ID: "cus_created"}, nil
}

func (f *billingClientFake) CreateCheckoutSession(_ context.Context, p billing.CheckoutParams) (*billing.CheckoutSession, error) {
	f.checkoutCalls = append(f.checkoutCalls, p)
	return &billing.CheckoutSession{ID: "cs_test", URL: "https://checkout.stripe.test/cs_test"}, nil
}

func (f *billingClientFake) CreateBillingPortalSession(_ context.Context, customerID, returnURL, idempotencyKey string) (*billing.BillingPortalSession, error) {
	f.portalCalls = append(f.portalCalls, portalCall{
		customerID: customerID, returnURL: returnURL, idempotencyKey: idempotencyKey,
	})
	return &billing.BillingPortalSession{ID: "bps_test", URL: "https://billing.stripe.test/bps_test"}, nil
}

func (f *billingClientFake) ListInvoices(context.Context, string, int) ([]billing.Invoice, error) {
	return nil, nil
}

func newBillingService(t *testing.T, store business.Store, client business.BillingClient) *business.Service {
	t.Helper()
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetBillingClient(client)
	svc.SetBillingRedirects(business.BillingRedirects{
		CheckoutSuccessURL: "https://app.example.com/admin/billing/success?session_id={CHECKOUT_SESSION_ID}",
		CheckoutCancelURL:  "https://app.example.com/admin/billing",
		PortalReturnURL:    "https://app.example.com/admin/billing",
	})
	return svc
}

func TestStartCheckoutUsesOnlyServerCatalogPolicy(t *testing.T) {
	store := &billingStoreFake{
		customerID: "cus_existing",
		plan: &business.PlanFull{
			Plan:          business.Plan{ID: "plan-id", Name: "pro", DisplayName: "Pro"},
			StripePriceID: "price_server_owned",
			Currency:      "eur", CheckoutEnabled: true, TrialDays: 21, TaxBehavior: "automatic",
		},
	}
	client := &billingClientFake{}
	svc := newBillingService(t, store, client)

	url, err := svc.StartCheckout(context.Background(), business.StartCheckoutInput{
		UserID: "user-1", OrgID: "org-1", PlanName: "pro", IdempotencyKey: "browser-operation-1",
	})
	require.NoError(t, err)
	require.Equal(t, "https://checkout.stripe.test/cs_test", url)
	require.Len(t, client.checkoutCalls, 1)

	got := client.checkoutCalls[0]
	require.Equal(t, "cus_existing", got.CustomerID)
	require.Equal(t, "price_server_owned", got.PriceID)
	require.Equal(t, "eur", got.Currency)
	require.Equal(t, 21, got.TrialDays)
	require.True(t, got.AutomaticTax)
	require.Equal(t, "https://app.example.com/admin/billing/success?session_id={CHECKOUT_SESSION_ID}", got.SuccessURL)
	require.Equal(t, "https://app.example.com/admin/billing", got.CancelURL)
	require.True(t, strings.HasPrefix(got.IdempotencyKey, "saas-starter:checkout:"))

	_, err = svc.StartCheckout(context.Background(), business.StartCheckoutInput{
		UserID: "user-1", OrgID: "org-1", PlanName: "pro", IdempotencyKey: "browser-operation-1",
	})
	require.NoError(t, err)
	require.Equal(t, got.IdempotencyKey, client.checkoutCalls[1].IdempotencyKey,
		"the same logical retry must reach Stripe with the same key")
}

func TestStartCheckoutUsesVerifiedCodeflyOrigin(t *testing.T) {
	store := &billingStoreFake{
		customerID: "cus_existing",
		plan: &business.PlanFull{
			Plan:            business.Plan{ID: "plan-id", Name: "pro", DisplayName: "Pro"},
			StripePriceID:   "price_server_owned",
			Currency:        "eur",
			CheckoutEnabled: true,
			TaxBehavior:     "automatic",
		},
	}
	client := &billingClientFake{}
	svc := newBillingService(t, store, client)
	ctx, err := auth.WithVerifiedPublicOrigin(context.Background(), "http://localhost:54321")
	require.NoError(t, err)

	_, err = svc.StartCheckout(ctx, business.StartCheckoutInput{
		UserID: "user-1", OrgID: "org-1", PlanName: "pro", IdempotencyKey: "codefly-origin",
	})
	require.NoError(t, err)
	require.Equal(t, "http://localhost:54321/admin/billing/success?session_id={CHECKOUT_SESSION_ID}", client.checkoutCalls[0].SuccessURL)
	require.Equal(t, "http://localhost:54321/admin/billing", client.checkoutCalls[0].CancelURL)
}

func TestStartCheckoutRejectsDisabledCatalogEntryBeforeStripe(t *testing.T) {
	store := &billingStoreFake{
		customerID: "cus_existing",
		plan: &business.PlanFull{
			Plan: business.Plan{Name: "free"}, StripePriceID: "price_should_not_be_used",
			CheckoutEnabled: false,
		},
	}
	client := &billingClientFake{}
	svc := newBillingService(t, store, client)

	_, err := svc.StartCheckout(context.Background(), business.StartCheckoutInput{
		UserID: "user-1", OrgID: "org-1", PlanName: "free", IdempotencyKey: "browser-operation-2",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not available for checkout")
	require.Empty(t, client.checkoutCalls)
}

func TestBillingMutationsRequireStableCallerKeyAndNamespaceOperations(t *testing.T) {
	store := &billingStoreFake{
		customerID: "cus_existing",
		plan: &business.PlanFull{
			Plan: business.Plan{Name: "pro"}, StripePriceID: "price_pro", CheckoutEnabled: true,
		},
	}
	client := &billingClientFake{}
	svc := newBillingService(t, store, client)

	_, err := svc.OpenBillingPortal(context.Background(), business.OpenBillingPortalInput{
		UserID: "user-1", OrgID: "org-1",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "Idempotency-Key is required")
	require.Empty(t, client.portalCalls)

	_, err = svc.OpenBillingPortal(context.Background(), business.OpenBillingPortalInput{
		UserID: "user-1", OrgID: "org-1", IdempotencyKey: "shared-browser-key",
	})
	require.NoError(t, err)
	require.Len(t, client.portalCalls, 1)
	require.Equal(t, "cus_existing", client.portalCalls[0].customerID)
	require.Equal(t, "https://app.example.com/admin/billing", client.portalCalls[0].returnURL)
	require.True(t, strings.HasPrefix(client.portalCalls[0].idempotencyKey, "saas-starter:portal:"))

	_, err = svc.StartCheckout(context.Background(), business.StartCheckoutInput{
		UserID: "user-1", OrgID: "org-1", PlanName: "pro", IdempotencyKey: "shared-browser-key",
	})
	require.NoError(t, err)
	require.NotEqual(t, client.portalCalls[0].idempotencyKey, client.checkoutCalls[0].IdempotencyKey,
		"one caller token must not collide across Stripe operation types")
}
