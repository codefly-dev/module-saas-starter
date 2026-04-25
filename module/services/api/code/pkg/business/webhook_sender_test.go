package business_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"api/pkg/business"
)

// fakeStore is a minimal in-memory shim that satisfies the only
// methods WebhookSender touches: UpdateWebhookDelivery (called once
// after the HTTP round-trip). Anything else panics so we catch
// accidental coupling to other Store methods.
type fakeStore struct {
	business.Store // embed the interface to inherit "panic on call" defaults
	mu             sync.Mutex
	updates        []business.WebhookDelivery
}

func (f *fakeStore) UpdateWebhookDelivery(_ context.Context, d *business.WebhookDelivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Snapshot — sender mutates the same row across paths.
	f.updates = append(f.updates, *d)
	return nil
}

func TestWebhookSender_DeliveredOn200(t *testing.T) {
	var got struct {
		body      []byte
		signature string
		event     string
		deliveryID string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.body, _ = io.ReadAll(r.Body)
		got.signature = r.Header.Get("X-Webhook-Signature")
		got.event = r.Header.Get("X-Webhook-Event")
		got.deliveryID = r.Header.Get("X-Webhook-Delivery-ID")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	store := &fakeStore{}
	sender := business.NewWebhookSender(store)

	sub := &business.WebhookSubscription{
		ID:     "sub-1",
		OrgID:  "org-1",
		URL:    srv.URL,
		Secret: "whsec_test",
	}
	delivery := &business.WebhookDelivery{
		ID:             "del-1",
		SubscriptionID: "sub-1",
		EventType:      "user.registered",
		Payload:        `{"event_type":"user.registered"}`,
	}

	sender.Send(context.Background(), sub, delivery, []byte(delivery.Payload))

	// Signature must verify under the subscription's secret — proves we
	// signed with the right key and the consumer can verify the payload.
	mac := hmac.New(sha256.New, []byte(sub.Secret))
	mac.Write([]byte(delivery.Payload))
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got.signature != expected {
		t.Errorf("signature mismatch:\n got  %q\n want %q", got.signature, expected)
	}

	if got.event != "user.registered" {
		t.Errorf("X-Webhook-Event = %q, want user.registered", got.event)
	}
	if got.deliveryID != "del-1" {
		t.Errorf("X-Webhook-Delivery-ID = %q, want del-1", got.deliveryID)
	}
	if string(got.body) != delivery.Payload {
		t.Errorf("body mismatch:\n got  %s\n want %s", got.body, delivery.Payload)
	}

	if delivery.Status != "delivered" {
		t.Errorf("Status = %q, want delivered", delivery.Status)
	}
	if delivery.HTTPStatus != 200 {
		t.Errorf("HTTPStatus = %d, want 200", delivery.HTTPStatus)
	}
	if delivery.DeliveredAt == nil {
		t.Errorf("DeliveredAt should be set on success")
	}
	if delivery.ResponseBody != `{"ok":true}` {
		t.Errorf("ResponseBody = %q, want {\"ok\":true}", delivery.ResponseBody)
	}

	// And the row should have been persisted exactly once.
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.updates) != 1 {
		t.Errorf("UpdateWebhookDelivery calls = %d, want 1", len(store.updates))
	}
}

func TestWebhookSender_FailedOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("oops"))
	}))
	defer srv.Close()

	store := &fakeStore{}
	sender := business.NewWebhookSender(store)
	sub := &business.WebhookSubscription{ID: "s", URL: srv.URL, Secret: "k"}
	d := &business.WebhookDelivery{ID: "d", SubscriptionID: "s", EventType: "e", Payload: `{"k":1}`}

	sender.Send(context.Background(), sub, d, []byte(d.Payload))

	if d.Status != "failed" {
		t.Errorf("Status = %q, want failed", d.Status)
	}
	if d.HTTPStatus != 500 {
		t.Errorf("HTTPStatus = %d, want 500", d.HTTPStatus)
	}
	if d.DeliveredAt != nil {
		t.Errorf("DeliveredAt should be nil on failure")
	}
	if d.ResponseBody != "oops" {
		t.Errorf("ResponseBody = %q, want oops", d.ResponseBody)
	}
}

func TestWebhookSender_FailedOnConnectionError(t *testing.T) {
	store := &fakeStore{}
	sender := business.NewWebhookSender(store)

	// Point at a port that's almost certainly closed — exercises the
	// "URL didn't connect" branch where HTTPStatus stays 0.
	sub := &business.WebhookSubscription{ID: "s", URL: "http://127.0.0.1:1", Secret: "k"}
	d := &business.WebhookDelivery{ID: "d", SubscriptionID: "s", EventType: "e", Payload: "{}"}

	// Bound the test — the sender uses a 10s default but we want this
	// test to fail fast if the local connection refusal isn't immediate.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sender.Send(ctx, sub, d, []byte("{}"))

	if d.Status != "failed" {
		t.Errorf("Status = %q, want failed", d.Status)
	}
	if d.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d, want 0 (no response)", d.HTTPStatus)
	}
	if d.ResponseBody == "" {
		t.Errorf("ResponseBody should carry the connection error message")
	}
}
