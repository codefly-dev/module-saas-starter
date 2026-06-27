package billing_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/billing"
)

// ============================================================================
// In-memory Store fake
// ============================================================================

type fakeStore struct {
	mu          sync.Mutex
	events      map[string]webhookRow
	plans       map[string]billing.PlanRef // keyed by stripe price id
	orgs        map[string]string          // stripe customer id → internal org id
	subs        []billing.SubscriptionUpsert
	recordErr   error
	upsertErr   error
	planNotFound bool
	orgNotFound  bool
}

type webhookRow struct {
	eventType   string
	processedAt *time.Time
	err         string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		events: map[string]webhookRow{},
		plans:  map[string]billing.PlanRef{},
		orgs:   map[string]string{},
	}
}

func (f *fakeStore) WebhookAlreadyProcessed(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.events[id]
	return ok, nil
}

func (f *fakeStore) RecordWebhook(_ context.Context, id, t string) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.events[id]; exists {
		return errors.New("duplicate")
	}
	f.events[id] = webhookRow{eventType: t}
	return nil
}

func (f *fakeStore) MarkWebhookProcessed(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.events[id]
	now := time.Now()
	row.processedAt = &now
	f.events[id] = row
	return nil
}

func (f *fakeStore) MarkWebhookFailed(_ context.Context, id, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.events[id]
	row.err = msg
	f.events[id] = row
	return nil
}

func (f *fakeStore) PlanByStripePriceID(_ context.Context, priceID string) (*billing.PlanRef, error) {
	if f.planNotFound {
		return nil, billing.ErrPlanNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.plans[priceID]
	if !ok {
		return nil, billing.ErrPlanNotFound
	}
	return &p, nil
}

func (f *fakeStore) OrgByStripeCustomerID(_ context.Context, customerID string) (string, error) {
	if f.orgNotFound {
		return "", billing.ErrOrgNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.orgs[customerID]
	if !ok {
		return "", billing.ErrOrgNotFound
	}
	return id, nil
}

func (f *fakeStore) UpsertSubscription(_ context.Context, sub billing.SubscriptionUpsert) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, sub)
	return nil
}

func (f *fakeStore) OwnerEmailByStripeCustomerID(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (f *fakeStore) subsForOrg(orgID string) []billing.SubscriptionUpsert {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []billing.SubscriptionUpsert
	for _, s := range f.subs {
		if s.OrgID == orgID {
			out = append(out, s)
		}
	}
	return out
}

// ============================================================================
// Helpers
// ============================================================================

const webhookSecret = "whsec_test_fake_secret"

func signBody(t *testing.T, body []byte) string {
	t.Helper()
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func postEvent(t *testing.T, srv *httptest.Server, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", signBody(t, body))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func newTestHandler(t *testing.T, fs *fakeStore) *httptest.Server {
	t.Helper()
	h := billing.NewHandler(billing.HandlerDeps{
		Store:         fs,
		WebhookSecret: webhookSecret,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func subCreatedBody(eventID, customer, priceID string) []byte {
	body := fmt.Sprintf(`{
		"id": %q,
		"type": "customer.subscription.created",
		"data": {
			"object": {
				"id": "sub_01ABC",
				"status": "active",
				"customer": %q,
				"current_period_start": 1700000000,
				"current_period_end": 1702592000,
				"items": {"data": [{"price": {"id": %q, "product": "prod_01"}}]}
			}
		}
	}`, eventID, customer, priceID)
	return []byte(body)
}

// ============================================================================
// Happy path — subscription.created creates a local row
// ============================================================================

func TestHandler_SubscriptionCreated_Happy(t *testing.T) {
	fs := newFakeStore()
	fs.plans["price_pro"] = billing.PlanRef{ID: "plan-uuid-pro", Name: "pro"}
	fs.orgs["cus_01ABC"] = "org-uuid-acme"

	srv := newTestHandler(t, fs)

	resp := postEvent(t, srv, subCreatedBody("evt_01", "cus_01ABC", "price_pro"))
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	subs := fs.subsForOrg("org-uuid-acme")
	require.Len(t, subs, 1)
	require.Equal(t, "plan-uuid-pro", subs[0].PlanID)
	require.Equal(t, "active", subs[0].Status)
	require.Equal(t, "sub_01ABC", subs[0].StripeSubscriptionID)
	require.NotNil(t, subs[0].CurrentPeriodStart)
	require.NotNil(t, subs[0].CurrentPeriodEnd)
}

// ============================================================================
// Idempotency — Stripe retries MUST NOT double-process
// ============================================================================

func TestHandler_Idempotency_SecondCallIsNoOp(t *testing.T) {
	fs := newFakeStore()
	fs.plans["price_pro"] = billing.PlanRef{ID: "plan-uuid-pro", Name: "pro"}
	fs.orgs["cus_01"] = "org-1"

	srv := newTestHandler(t, fs)
	body := subCreatedBody("evt_same_id", "cus_01", "price_pro")

	resp1 := postEvent(t, srv, body)
	defer resp1.Body.Close()
	require.Equal(t, 200, resp1.StatusCode)

	resp2 := postEvent(t, srv, body)
	defer resp2.Body.Close()
	require.Equal(t, 200, resp2.StatusCode)

	var decoded map[string]string
	body2, _ := io.ReadAll(resp2.Body)
	require.NoError(t, json.Unmarshal(body2, &decoded))
	require.Equal(t, "duplicate", decoded["status"])

	// Only one subscription row despite two webhook POSTs.
	require.Len(t, fs.subsForOrg("org-1"), 1)
}

// ============================================================================
// Signature verification — tampered bodies are rejected, NO dispatch
// ============================================================================

func TestHandler_TamperedSignature_Rejected_NoDispatch(t *testing.T) {
	fs := newFakeStore()
	fs.plans["price_pro"] = billing.PlanRef{ID: "plan-uuid-pro"}
	fs.orgs["cus_01"] = "org-1"

	srv := newTestHandler(t, fs)

	body := subCreatedBody("evt_01", "cus_01", "price_pro")
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", "t=1,v1=deadbeef")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 400, resp.StatusCode)
	require.Empty(t, fs.subsForOrg("org-1"),
		"tampered webhook must not touch any state")
}

func TestHandler_MissingSignature_Rejected(t *testing.T) {
	srv := newTestHandler(t, newFakeStore())
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(`{}`)))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 400, resp.StatusCode)
}

// ============================================================================
// subscription.updated maps to the same upsert path
// ============================================================================

func TestHandler_SubscriptionUpdated_RewritesRow(t *testing.T) {
	fs := newFakeStore()
	fs.plans["price_pro"] = billing.PlanRef{ID: "plan-uuid-pro"}
	fs.plans["price_enterprise"] = billing.PlanRef{ID: "plan-uuid-enterprise"}
	fs.orgs["cus_01"] = "org-1"
	srv := newTestHandler(t, fs)

	body := []byte(`{
		"id": "evt_upd",
		"type": "customer.subscription.updated",
		"data": {"object": {
			"id": "sub_01",
			"status": "active",
			"customer": "cus_01",
			"current_period_start": 1700000000,
			"current_period_end": 1702592000,
			"items": {"data": [{"price": {"id": "price_enterprise"}}]}
		}}
	}`)

	resp := postEvent(t, srv, body)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	subs := fs.subsForOrg("org-1")
	require.Len(t, subs, 1)
	require.Equal(t, "plan-uuid-enterprise", subs[0].PlanID,
		"updated event must reflect new plan")
}

// ============================================================================
// subscription.deleted forces status=canceled
// ============================================================================

func TestHandler_SubscriptionDeleted_ForcesCanceled(t *testing.T) {
	fs := newFakeStore()
	fs.plans["price_pro"] = billing.PlanRef{ID: "plan-uuid-pro"}
	fs.orgs["cus_01"] = "org-1"
	srv := newTestHandler(t, fs)

	body := []byte(`{
		"id": "evt_del",
		"type": "customer.subscription.deleted",
		"data": {"object": {
			"id": "sub_01",
			"status": "active",
			"customer": "cus_01",
			"current_period_start": 1700000000,
			"current_period_end": 1702592000,
			"items": {"data": [{"price": {"id": "price_pro"}}]}
		}}
	}`)

	resp := postEvent(t, srv, body)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	subs := fs.subsForOrg("org-1")
	require.Len(t, subs, 1)
	require.Equal(t, "canceled", subs[0].Status,
		"deleted event must force canceled regardless of Stripe-reported status")
}

// ============================================================================
// invoice.payment_failed → status=past_due without touching plan/periods
// ============================================================================

func TestHandler_InvoicePaymentFailed_MarksPastDue(t *testing.T) {
	fs := newFakeStore()
	fs.orgs["cus_01"] = "org-1"
	srv := newTestHandler(t, fs)

	body := []byte(`{
		"id": "evt_invfail",
		"type": "invoice.payment_failed",
		"data": {"object": {
			"subscription": "sub_01",
			"customer": "cus_01"
		}}
	}`)

	resp := postEvent(t, srv, body)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	subs := fs.subsForOrg("org-1")
	require.Len(t, subs, 1)
	require.Equal(t, "past_due", subs[0].Status)
	require.Equal(t, "sub_01", subs[0].StripeSubscriptionID)
	require.Empty(t, subs[0].PlanID,
		"payment_failed must not touch plan (next subscription.updated will)")
}

// ============================================================================
// Unknown event type — ack'd 200 but no state change
// ============================================================================

func TestHandler_UnknownEventType_Ignored(t *testing.T) {
	fs := newFakeStore()
	srv := newTestHandler(t, fs)

	body := []byte(`{
		"id": "evt_unknown",
		"type": "customer.tax_id.created",
		"data": {"object": {}}
	}`)

	resp := postEvent(t, srv, body)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	require.Empty(t, fs.subs)
}

// ============================================================================
// Unroutable events (unknown plan / unknown customer) return 200 so Stripe
// stops retrying — ops handles reconciliation manually.
// ============================================================================

func TestHandler_UnknownCustomer_ReturnsUnroutable(t *testing.T) {
	fs := newFakeStore() // no orgs registered
	srv := newTestHandler(t, fs)

	body := subCreatedBody("evt_01", "cus_unknown", "price_pro")
	resp := postEvent(t, srv, body)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode,
		"unroutable events return 200 so Stripe stops retrying")

	var decoded map[string]string
	b, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(b, &decoded))
	require.Equal(t, "unroutable", decoded["status"])
	require.Empty(t, fs.subs)
}

func TestHandler_UnknownPlan_ReturnsUnroutable(t *testing.T) {
	fs := newFakeStore()
	fs.orgs["cus_01"] = "org-1"
	// no plans mapped
	srv := newTestHandler(t, fs)

	body := subCreatedBody("evt_01", "cus_01", "price_unknown")
	resp := postEvent(t, srv, body)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var decoded map[string]string
	b, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(b, &decoded))
	require.Equal(t, "unroutable", decoded["status"])
}

// ============================================================================
// Internal errors → 500 so Stripe retries
// ============================================================================

func TestHandler_DBError_Returns500(t *testing.T) {
	fs := newFakeStore()
	fs.plans["price_pro"] = billing.PlanRef{ID: "plan-uuid"}
	fs.orgs["cus_01"] = "org-1"
	fs.upsertErr = errors.New("connection refused")
	srv := newTestHandler(t, fs)

	resp := postEvent(t, srv, subCreatedBody("evt_01", "cus_01", "price_pro"))
	defer resp.Body.Close()
	require.Equal(t, 500, resp.StatusCode,
		"transient DB failures must return 500 so Stripe retries")
}

// ============================================================================
// GET is rejected — Stripe uses POST only
// ============================================================================

func TestHandler_GETRejected(t *testing.T) {
	srv := newTestHandler(t, newFakeStore())
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 405, resp.StatusCode)
}
