package billing_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/billing"
)

// fakeStripe is a minimal Stripe API stand-in. It exposes the three
// endpoints the client uses and verifies the request shape + auth.
type fakeStripe struct {
	apiKey             string
	server             *httptest.Server
	lastForm           url.Values
	lastPath           string
	lastIdempotencyKey string
	nextStatus         int
	nextBody           any
}

func newFakeStripe(t *testing.T) *fakeStripe {
	t.Helper()
	fs := &fakeStripe{apiKey: "sk_test_fake"}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/customers", fs.handle)
	mux.HandleFunc("/v1/checkout/sessions", fs.handle)
	mux.HandleFunc("/v1/billing_portal/sessions", fs.handle)
	mux.HandleFunc("/v1/subscriptions/", fs.handle) // trailing slash for subpath
	fs.server = httptest.NewServer(mux)
	t.Cleanup(fs.server.Close)
	return fs
}

func (f *fakeStripe) handle(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+f.apiKey {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
		return
	}
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(400)
		return
	}
	f.lastForm = r.PostForm
	f.lastPath = r.URL.Path
	f.lastIdempotencyKey = r.Header.Get("Idempotency-Key")

	status := f.nextStatus
	if status == 0 {
		status = 200
	}
	body := f.nextBody
	if body == nil {
		body = map[string]any{"id": "default_id"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func newClient(t *testing.T, fs *fakeStripe) *billing.Client {
	t.Helper()
	c, err := billing.New(billing.Config{
		APIKey:  fs.apiKey,
		BaseURL: fs.server.URL,
	})
	require.NoError(t, err)
	return c
}

// ============================================================================
// Construction
// ============================================================================

func TestNew_RequiresAPIKey(t *testing.T) {
	_, err := billing.New(billing.Config{})
	require.Error(t, err)
}

func TestNew_DefaultsBaseURL(t *testing.T) {
	c, err := billing.New(billing.Config{APIKey: "sk_test"})
	require.NoError(t, err)
	require.NotNil(t, c)
}

// ============================================================================
// Customer
// ============================================================================

func TestCreateCustomer_PostsExpectedForm(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStripe(t)
	fs.nextBody = map[string]any{"id": "cus_01ABC", "email": "org@acme.com"}

	c := newClient(t, fs)
	out, err := c.CreateCustomer(ctx, "org@acme.com", "internal-org-7", "customer-key")
	require.NoError(t, err)
	require.Equal(t, "cus_01ABC", out.ID)
	require.Equal(t, "org@acme.com", out.Email)
	require.Equal(t, "/v1/customers", fs.lastPath)
	require.Equal(t, "org@acme.com", fs.lastForm.Get("email"))
	require.Equal(t, "internal-org-7", fs.lastForm.Get("metadata[org_id]"))
	require.Equal(t, "customer-key", fs.lastIdempotencyKey)
}

func TestCreateCustomer_AuthFailure(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStripe(t)
	c, err := billing.New(billing.Config{
		APIKey:  "wrong_key",
		BaseURL: fs.server.URL,
	})
	require.NoError(t, err)

	_, err = c.CreateCustomer(ctx, "a@b.com", "o1", "customer-key")
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")
}

// ============================================================================
// Checkout
// ============================================================================

func TestCreateCheckoutSession_Happy(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStripe(t)
	fs.nextBody = map[string]any{
		"id":       "cs_01XYZ",
		"url":      "https://checkout.stripe.com/c/pay/cs_01XYZ",
		"customer": "cus_01ABC",
	}

	c := newClient(t, fs)
	out, err := c.CreateCheckoutSession(ctx, billing.CheckoutParams{
		CustomerID:     "cus_01ABC",
		PriceID:        "price_pro_monthly",
		SuccessURL:     "https://app.acme.com/billing/success",
		CancelURL:      "https://app.acme.com/billing/cancel",
		OrgID:          "org-uuid",
		TrialDays:      14,
		AutomaticTax:   true,
		IdempotencyKey: "checkout-key",
	})
	require.NoError(t, err)
	require.Equal(t, "cs_01XYZ", out.ID)
	require.True(t, strings.HasPrefix(out.URL, "https://checkout.stripe.com"))

	require.Equal(t, "subscription", fs.lastForm.Get("mode"))
	require.Equal(t, "cus_01ABC", fs.lastForm.Get("customer"))
	require.Equal(t, "price_pro_monthly", fs.lastForm.Get("line_items[0][price]"))
	require.Equal(t, "1", fs.lastForm.Get("line_items[0][quantity]"))
	require.Equal(t, "org-uuid", fs.lastForm.Get("metadata[org_id]"))
	require.Equal(t, "org-uuid", fs.lastForm.Get("subscription_data[metadata][org_id]"))
	require.Equal(t, "14", fs.lastForm.Get("subscription_data[trial_period_days]"))
	require.Equal(t, "true", fs.lastForm.Get("automatic_tax[enabled]"))
	require.Equal(t, "checkout-key", fs.lastIdempotencyKey)
}

func TestCreateCheckoutSession_Validation(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStripe(t)
	c := newClient(t, fs)

	_, err := c.CreateCheckoutSession(ctx, billing.CheckoutParams{})
	require.Error(t, err)

	_, err = c.CreateCheckoutSession(ctx, billing.CheckoutParams{
		CustomerID: "cus_x",
		PriceID:    "price_y",
		// missing urls
	})
	require.Error(t, err)

	_, err = c.CreateCheckoutSession(ctx, billing.CheckoutParams{
		CustomerID: "cus_x",
		PriceID:    "price_y",
		SuccessURL: "https://app.example/success",
		CancelURL:  "https://app.example/cancel",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "idempotency key")
}

// ============================================================================
// Billing portal
// ============================================================================

func TestCreateBillingPortalSession_Happy(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStripe(t)
	fs.nextBody = map[string]any{
		"id":  "bps_01",
		"url": "https://billing.stripe.com/p/session/bps_01",
	}

	c := newClient(t, fs)
	out, err := c.CreateBillingPortalSession(ctx,
		"cus_01ABC", "https://app.acme.com/settings/billing", "portal-key")
	require.NoError(t, err)
	require.Equal(t, "bps_01", out.ID)
	require.Contains(t, out.URL, "billing.stripe.com")
	require.Equal(t, "cus_01ABC", fs.lastForm.Get("customer"))
	require.Equal(t, "https://app.acme.com/settings/billing", fs.lastForm.Get("return_url"))
	require.Equal(t, "portal-key", fs.lastIdempotencyKey)
}

func TestCreateBillingPortalSession_Validation(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStripe(t)
	c := newClient(t, fs)

	_, err := c.CreateBillingPortalSession(ctx, "", "https://x", "portal-key")
	require.Error(t, err)
	_, err = c.CreateBillingPortalSession(ctx, "cus_x", "", "portal-key")
	require.Error(t, err)

	_, err = c.CreateBillingPortalSession(ctx, "cus_x", "https://x", "")
	require.Error(t, err)
}

// ============================================================================
// Subscription fetch + PrimaryPriceID helper
// ============================================================================

func TestGetSubscription_Happy(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStripe(t)
	fs.nextBody = map[string]any{
		"id":                   "sub_01",
		"status":               "active",
		"customer":             "cus_01",
		"current_period_start": 1_700_000_000,
		"current_period_end":   1_702_592_000,
		"cancel_at_period_end": false,
		"items": map[string]any{
			"data": []map[string]any{
				{
					"price": map[string]any{
						"id":      "price_pro",
						"product": "prod_pro",
					},
				},
			},
		},
	}

	c := newClient(t, fs)
	sub, err := c.GetSubscription(ctx, "sub_01")
	require.NoError(t, err)
	require.Equal(t, "sub_01", sub.ID)
	require.Equal(t, "active", sub.Status)
	require.Equal(t, "price_pro", sub.PrimaryPriceID())
	require.Equal(t, "/v1/subscriptions/sub_01", fs.lastPath)
}

func TestPrimaryPriceID_EmptyItems(t *testing.T) {
	s := billing.Subscription{}
	require.Equal(t, "", s.PrimaryPriceID())
}
