// Package billing implements a minimal Stripe client for checkout,
// billing portal, and webhook handling — just enough to run a
// subscription-based SaaS without pulling in the full Stripe Go SDK.
//
// Why stdlib: the Stripe Go SDK is ~40k lines and ships handcoded types
// for every Stripe resource. We need three endpoints; stdlib HTTP plus
// a handful of typed structs is 400 lines and tests cleanly against a
// fake server. Swap to the SDK later if you need advanced reporting or
// Connect flows — the pkg/billing interface doesn't bleed Stripe types
// out, so the swap is localized.
//
// The package is:
//
//   - Stateless: no DB access. Callers thread results into business.Service.
//   - Testable: all endpoints configurable via Config.BaseURL; tests use
//     httptest.
//   - Idempotent-safe: the WebhookHandler records event ids in the
//     stripe_webhook_events table before processing.
package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config is the minimum needed to talk to Stripe.
type Config struct {
	// APIKey is the Stripe secret key, typically sk_live_... or sk_test_...
	APIKey string
	// WebhookSecret is the signing secret for webhook verification
	// (whsec_...). Required for Handler.Verify to work.
	WebhookSecret string
	// BaseURL overrides the Stripe API base. Empty = "https://api.stripe.com".
	// Tests set this to a httptest server URL.
	BaseURL string
	// HTTPClient is used for all outgoing requests. Defaults to a client
	// with a 10-second timeout.
	HTTPClient *http.Client
}

// Client is the Stripe API client. Safe for concurrent use.
type Client struct {
	cfg     Config
	baseURL string
	http    *http.Client
}

// New constructs a Client. APIKey is required.
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("billing: Stripe API key is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.stripe.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{cfg: cfg, baseURL: strings.TrimRight(cfg.BaseURL, "/"), http: cfg.HTTPClient}, nil
}

// ============================================================================
// Typed resources — a tiny subset of Stripe's model
// ============================================================================

// Customer is a Stripe customer record. We store only the fields we care
// about for mapping to local orgs.
type Customer struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// CheckoutSession is what Stripe returns when we ask for a checkout URL.
type CheckoutSession struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	CustomerID string `json:"customer"`
}

// BillingPortalSession lets a customer manage their subscription
// self-service (cancel, update payment method, etc.).
type BillingPortalSession struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// Subscription is a slimmed-down view of a Stripe subscription,
// sufficient to reflect state into our local subscriptions table.
type Subscription struct {
	ID                 string `json:"id"`
	Status             string `json:"status"`
	CustomerID         string `json:"customer"`
	CurrentPeriodStart int64  `json:"current_period_start"`
	CurrentPeriodEnd   int64  `json:"current_period_end"`
	CancelAtPeriodEnd  bool   `json:"cancel_at_period_end"`
	Items              struct {
		Data []struct {
			Price struct {
				ID        string `json:"id"`
				ProductID string `json:"product"`
			} `json:"price"`
		} `json:"data"`
	} `json:"items"`
}

// PrimaryPriceID returns the first subscription item's price id, which
// is the canonical "current plan" under our single-item-per-subscription
// assumption.
func (s *Subscription) PrimaryPriceID() string {
	if len(s.Items.Data) == 0 {
		return ""
	}
	return s.Items.Data[0].Price.ID
}

// ============================================================================
// Customer create
// ============================================================================

// CreateCustomer creates a new Stripe Customer for an org. Call once per
// org the first time they attempt to subscribe; store the returned id on
// organizations.stripe_customer_id.
func (c *Client) CreateCustomer(ctx context.Context, email, orgID string) (*Customer, error) {
	form := url.Values{}
	form.Set("email", email)
	form.Set("metadata[org_id]", orgID)

	var out Customer
	if err := c.post(ctx, "/v1/customers", form, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ============================================================================
// Checkout
// ============================================================================

// CheckoutParams is the input for a subscription checkout session.
type CheckoutParams struct {
	CustomerID string // existing Stripe customer id (required)
	PriceID    string // Stripe Price id for the target plan (required)
	SuccessURL string // where Stripe redirects on success
	CancelURL  string // where Stripe redirects on cancel
	OrgID      string // our internal org id, threaded through metadata
	TrialDays  int    // optional trial days; 0 = no trial
	Currency   string // ISO 4217 currency code (e.g. "usd", "eur"); empty = plan default
}

// CreateCheckoutSession starts a Stripe Checkout flow for a subscription.
// The returned URL is what the frontend redirects the user to.
func (c *Client) CreateCheckoutSession(ctx context.Context, p CheckoutParams) (*CheckoutSession, error) {
	if p.CustomerID == "" || p.PriceID == "" || p.SuccessURL == "" || p.CancelURL == "" {
		return nil, fmt.Errorf("billing: CreateCheckoutSession requires CustomerID, PriceID, SuccessURL, CancelURL")
	}
	form := url.Values{}
	form.Set("mode", "subscription")
	form.Set("customer", p.CustomerID)
	form.Set("success_url", p.SuccessURL)
	form.Set("cancel_url", p.CancelURL)
	form.Set("line_items[0][price]", p.PriceID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("metadata[org_id]", p.OrgID)
	form.Set("subscription_data[metadata][org_id]", p.OrgID)
	if p.Currency != "" {
		form.Set("currency", p.Currency)
	}
	if p.TrialDays > 0 {
		form.Set("subscription_data[trial_period_days]", fmt.Sprintf("%d", p.TrialDays))
	}

	var out CheckoutSession
	if err := c.post(ctx, "/v1/checkout/sessions", form, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ============================================================================
// Billing portal
// ============================================================================

// CreateBillingPortalSession returns a URL the customer can visit to
// manage their subscription (update card, cancel, view invoices).
// ReturnURL is where Stripe sends them when they're done.
func (c *Client) CreateBillingPortalSession(ctx context.Context, customerID, returnURL string) (*BillingPortalSession, error) {
	if customerID == "" || returnURL == "" {
		return nil, fmt.Errorf("billing: CreateBillingPortalSession requires customerID and returnURL")
	}
	form := url.Values{}
	form.Set("customer", customerID)
	form.Set("return_url", returnURL)

	var out BillingPortalSession
	if err := c.post(ctx, "/v1/billing_portal/sessions", form, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ============================================================================
// Subscription fetch (for reconciliation)
// ============================================================================

// GetSubscription fetches a subscription by id. Used by the webhook
// handler to resolve stale references or by a reconciliation job that
// sweeps local state against Stripe on a schedule.
func (c *Client) GetSubscription(ctx context.Context, id string) (*Subscription, error) {
	if id == "" {
		return nil, fmt.Errorf("billing: subscription id is required")
	}
	var out Subscription
	if err := c.get(ctx, "/v1/subscriptions/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ============================================================================
// HTTP primitives
// ============================================================================

func (c *Client) post(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("billing: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("billing: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("billing: stripe %d: %s", resp.StatusCode, truncate(body, 500))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("billing: decode: %w", err)
		}
	}
	return nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
