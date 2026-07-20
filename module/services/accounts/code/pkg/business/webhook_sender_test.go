package business

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type testCipher struct{}

func (testCipher) EncryptSecret(_ context.Context, _, plaintext string) (string, error) {
	return plaintext, nil
}

func (testCipher) DecryptSecret(_ context.Context, _, envelope string) (string, error) {
	return envelope, nil
}

func TestWebhookSender_DeliveredOn200(t *testing.T) {
	var got struct {
		body       []byte
		signature  string
		event      string
		deliveryID string
		eventID    string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.body, _ = io.ReadAll(r.Body)
		got.signature = r.Header.Get("X-Webhook-Signature")
		got.event = r.Header.Get("X-Webhook-Event")
		got.deliveryID = r.Header.Get("X-Webhook-Delivery-ID")
		got.eventID = r.Header.Get("X-Webhook-Event-ID")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	sender := NewWebhookSenderWithClient(testCipher{}, srv.Client())

	sub := &WebhookSubscription{
		ID:              "sub-1",
		OrgID:           "org-1",
		URL:             srv.URL,
		SecretEncrypted: "whsec_test",
	}
	delivery := &WebhookDelivery{
		ID:             "del-1",
		SubscriptionID: "sub-1",
		EventType:      "user.registered",
		Payload:        `{"event_type":"user.registered"}`,
	}

	result, err := sender.attempt(context.Background(), sub, delivery, []byte(delivery.Payload))
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}

	// Signature covers timestamp + stable event ID + the exact body bytes.
	parts := strings.Split(got.signature, ",")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "t=") || !strings.HasPrefix(parts[1], "v1=") {
		t.Fatalf("signature format = %q", got.signature)
	}
	timestamp, err := strconv.ParseInt(strings.TrimPrefix(parts[0], "t="), 10, 64)
	if err != nil {
		t.Fatalf("parse signature timestamp: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(sub.SecretEncrypted))
	_, _ = fmt.Fprintf(mac, "%d.%s.%s", timestamp, "del-1", delivery.Payload)
	expected := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if parts[1] != expected {
		t.Errorf("signature mismatch:\n got  %q\n want %q", parts[1], expected)
	}

	if got.event != "user.registered" {
		t.Errorf("X-Webhook-Event = %q, want user.registered", got.event)
	}
	if got.deliveryID != "del-1" {
		t.Errorf("X-Webhook-Delivery-ID = %q, want del-1", got.deliveryID)
	}
	if got.eventID != "del-1" {
		t.Errorf("X-Webhook-Event-ID = %q, want del-1", got.eventID)
	}
	if string(got.body) != delivery.Payload {
		t.Errorf("body mismatch:\n got  %s\n want %s", got.body, delivery.Payload)
	}

	if result.HTTPStatus != http.StatusOK || result.ResponseBody != `{"ok":true}` {
		t.Errorf("result = %+v, want HTTP 200 response", result)
	}
}

func TestWebhookSender_FailedOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("oops"))
	}))
	defer srv.Close()

	sender := NewWebhookSenderWithClient(testCipher{}, srv.Client())
	sub := &WebhookSubscription{ID: "s", URL: srv.URL, SecretEncrypted: "k"}
	d := &WebhookDelivery{ID: "d", SubscriptionID: "s", EventType: "e", Payload: `{"k":1}`}

	result, err := sender.attempt(context.Background(), sub, d, []byte(d.Payload))
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if result.HTTPStatus != http.StatusInternalServerError || result.ResponseBody != "oops" {
		t.Errorf("result = %+v, want HTTP 500 response", result)
	}
}

func TestWebhookSender_RotationOverlapSignsWithBothKeys(t *testing.T) {
	var signature string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signature = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	sender := NewWebhookSenderWithClient(testCipher{}, srv.Client())
	expires := time.Now().Add(time.Hour)
	sub := &WebhookSubscription{
		ID: "sub-rotation", OrgID: "org-1", URL: srv.URL,
		SecretEncrypted:         "new-key",
		PreviousSecretEncrypted: "old-key",
		PreviousSecretExpiresAt: &expires,
	}
	delivery := &WebhookDelivery{
		ID: "delivery-rotation", EventID: "event-stable", SubscriptionID: sub.ID,
		EventType: "user.updated", Payload: `{"version":2}`,
	}
	if _, err := sender.attempt(t.Context(), sub, delivery, []byte(delivery.Payload)); err != nil {
		t.Fatalf("attempt: %v", err)
	}

	parts := strings.Split(signature, ",")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "t=") ||
		!strings.HasPrefix(parts[1], "v1=") || !strings.HasPrefix(parts[2], "v1=") {
		t.Fatalf("rotation signature = %q, want timestamp and two v1 digests", signature)
	}
	if parts[1] == parts[2] {
		t.Fatal("old and new key signatures unexpectedly match")
	}
}

func TestWebhookSender_FailedOnConnectionError(t *testing.T) {
	sender := NewWebhookSenderWithClient(testCipher{}, &http.Client{Timeout: 2 * time.Second})

	// Point at a port that's almost certainly closed — exercises the
	// "URL didn't connect" branch where HTTPStatus stays 0.
	sub := &WebhookSubscription{ID: "s", URL: "http://127.0.0.1:1", SecretEncrypted: "k"}
	d := &WebhookDelivery{ID: "d", SubscriptionID: "s", EventType: "e", Payload: "{}"}

	// Bound the test — the sender uses a 10s default but we want this
	// test to fail fast if the local connection refusal isn't immediate.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sender.attempt(ctx, sub, d, []byte("{}"))
	if err == nil {
		t.Fatal("attempt succeeded against a closed port")
	}
	if result.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d, want 0 (no response)", result.HTTPStatus)
	}
}
