package business

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/codefly-dev/core/wool"
)

// webhookPayload is the JSON envelope sent to webhook endpoints.
type webhookPayload struct {
	EventType  string          `json:"event_type"`
	Data       json.RawMessage `json:"data"`
	Timestamp  string          `json:"timestamp"`
	DeliveryID string          `json:"delivery_id"`
}

// AsyncWebhookDispatcher listens for audit events and dispatches matching
// webhook deliveries asynchronously, similar to AsyncAuditEmitter.
type AsyncWebhookDispatcher struct {
	store  Store
	client *http.Client
	ch     chan dispatchWork
}

type dispatchWork struct {
	entry AuditEntry
}

// NewAsyncWebhookDispatcher creates a dispatcher with the given buffer size
// and number of worker goroutines.
func NewAsyncWebhookDispatcher(store Store, bufferSize, workers int) *AsyncWebhookDispatcher {
	d := &AsyncWebhookDispatcher{
		store: store,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		ch: make(chan dispatchWork, bufferSize),
	}
	for i := 0; i < workers; i++ {
		go d.worker()
	}
	return d
}

// Dispatch enqueues an audit entry for webhook processing. Non-blocking;
// drops events if the buffer is full.
func (d *AsyncWebhookDispatcher) Dispatch(entry AuditEntry) {
	select {
	case d.ch <- dispatchWork{entry: entry}:
	default:
		// Buffer full — drop rather than block business logic
	}
}

func (d *AsyncWebhookDispatcher) worker() {
	for w := range d.ch {
		d.processEntry(w.entry)
	}
}

func (d *AsyncWebhookDispatcher) processEntry(entry AuditEntry) {
	ctx := context.Background()
	w := wool.Get(ctx).In("WebhookDispatcher.processEntry")

	// Find active subscriptions matching this event type
	subs, err := d.store.GetActiveWebhookSubscriptions(ctx, entry.Action)
	if err != nil {
		w.Debug("failed to get webhook subscriptions", wool.ErrField(err))
		return
	}

	for _, sub := range subs {
		d.deliver(ctx, sub, entry)
	}
}

func (d *AsyncWebhookDispatcher) deliver(ctx context.Context, sub *WebhookSubscription, entry AuditEntry) {
	w := wool.Get(ctx).In("WebhookDispatcher.deliver")

	deliveryID := NewIDString()

	// Build payload
	data, _ := json.Marshal(map[string]string{
		"action":      entry.Action,
		"resource":    entry.Resource,
		"resource_id": entry.ResourceID,
		"actor_id":    entry.ActorID,
		"actor_type":  entry.ActorType,
		"org_id":      entry.OrgID,
	})

	payload := webhookPayload{
		EventType:  entry.Action,
		Data:       data,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		DeliveryID: deliveryID,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		w.Debug("failed to marshal webhook payload", wool.ErrField(err))
		return
	}

	delivery := &WebhookDelivery{
		ID:             deliveryID,
		SubscriptionID: sub.ID,
		EventType:      entry.Action,
		Payload:        string(payloadBytes),
		Status:         "pending",
		AttemptCount:   0,
	}

	if err := d.store.CreateWebhookDelivery(ctx, delivery); err != nil {
		w.Debug("failed to create webhook delivery record", wool.ErrField(err))
		return
	}

	// Compute HMAC-SHA256 signature
	mac := hmac.New(sha256.New, []byte(sub.Secret))
	mac.Write(payloadBytes)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// Send HTTP POST
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(payloadBytes))
	if err != nil {
		d.markFailed(ctx, delivery, 0, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", signature)
	req.Header.Set("X-Webhook-Delivery-ID", deliveryID)

	resp, err := d.client.Do(req)
	if err != nil {
		d.markFailed(ctx, delivery, 0, err.Error())
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	delivery.HTTPStatus = resp.StatusCode
	delivery.ResponseBody = string(body)
	delivery.AttemptCount = 1

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		now := time.Now()
		delivery.Status = "delivered"
		delivery.DeliveredAt = &now
	} else {
		d.markFailed(ctx, delivery, resp.StatusCode, string(body))
		return
	}

	if err := d.store.UpdateWebhookDelivery(ctx, delivery); err != nil {
		w.Debug("failed to update webhook delivery", wool.ErrField(err))
	}
}

// markFailed sets the delivery as retrying with exponential backoff, or failed
// if max attempts reached.
func (d *AsyncWebhookDispatcher) markFailed(ctx context.Context, delivery *WebhookDelivery, httpStatus int, responseBody string) {
	w := wool.Get(ctx).In("WebhookDispatcher.markFailed")

	delivery.HTTPStatus = httpStatus
	delivery.ResponseBody = responseBody
	delivery.AttemptCount++

	const maxAttempts = 5
	if delivery.AttemptCount >= maxAttempts {
		delivery.Status = "failed"
	} else {
		delivery.Status = "retrying"
		// Exponential backoff: 30s, 2m, 8m, 32m
		backoff := time.Duration(1<<uint(delivery.AttemptCount)) * 30 * time.Second
		nextRetry := time.Now().Add(backoff)
		delivery.NextRetryAt = &nextRetry
	}

	if err := d.store.UpdateWebhookDelivery(ctx, delivery); err != nil {
		w.Debug("failed to update failed webhook delivery", wool.ErrField(err))
	}
}

// Close stops the dispatcher and drains remaining work.
func (d *AsyncWebhookDispatcher) Close() {
	close(d.ch)
}

// RetryPending fetches pending/retrying deliveries and re-attempts delivery.
// Intended to be called periodically (e.g. every minute) from a background ticker.
func (d *AsyncWebhookDispatcher) RetryPending(ctx context.Context, limit int) error {
	w := wool.Get(ctx).In("WebhookDispatcher.RetryPending")

	deliveries, err := d.store.GetPendingDeliveries(ctx, limit)
	if err != nil {
		return fmt.Errorf("failed to get pending deliveries: %w", err)
	}

	for _, delivery := range deliveries {
		// Re-enqueue for delivery
		// We use the delivery payload directly instead of reconstructing
		d.redeliverFromRecord(ctx, delivery)
	}

	w.Debug("retried pending deliveries", wool.Field("count", len(deliveries)))
	return nil
}

func (d *AsyncWebhookDispatcher) redeliverFromRecord(ctx context.Context, delivery *WebhookDelivery) {
	w := wool.Get(ctx).In("WebhookDispatcher.redeliver")

	// Look up the subscription to get URL + secret
	subs, err := d.store.GetActiveWebhookSubscriptions(ctx, delivery.EventType)
	if err != nil {
		w.Debug("failed to get subscriptions for retry", wool.ErrField(err))
		return
	}

	for _, sub := range subs {
		if sub.ID != delivery.SubscriptionID {
			continue
		}

		payloadBytes := []byte(delivery.Payload)

		mac := hmac.New(sha256.New, []byte(sub.Secret))
		mac.Write(payloadBytes)
		signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(payloadBytes))
		if err != nil {
			d.markFailed(ctx, delivery, 0, err.Error())
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Webhook-Signature", signature)
		req.Header.Set("X-Webhook-Delivery-ID", delivery.ID)

		resp, err := d.client.Do(req)
		if err != nil {
			d.markFailed(ctx, delivery, 0, err.Error())
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

		delivery.HTTPStatus = resp.StatusCode
		delivery.ResponseBody = string(body)
		delivery.AttemptCount++

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			now := time.Now()
			delivery.Status = "delivered"
			delivery.DeliveredAt = &now
			if err := d.store.UpdateWebhookDelivery(ctx, delivery); err != nil {
				w.Debug("failed to update delivery after retry", wool.ErrField(err))
			}
		} else {
			d.markFailed(ctx, delivery, resp.StatusCode, string(body))
		}
		return
	}

	// Subscription no longer active — mark as failed
	delivery.Status = "failed"
	delivery.ResponseBody = "subscription no longer active"
	_ = d.store.UpdateWebhookDelivery(ctx, delivery)
}
