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
	done   chan struct{} // closed on Close() to stop the retry ticker
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
		ch:   make(chan dispatchWork, bufferSize),
		done: make(chan struct{}),
	}
	for i := 0; i < workers; i++ {
		go d.worker()
	}
	go d.retryLoop()
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

	// Cross-tenant scan: find every org's subscriptions matching
	// this event. RLS would otherwise hide them all (no
	// app.current_org_id set on a worker context). WithBypass
	// elevates to session_user for the read.
	var subs []*WebhookSubscription
	if err := d.store.WithBypass(ctx, func(ctx context.Context) error {
		s, err := d.store.GetActiveWebhookSubscriptions(ctx, entry.Action)
		subs = s
		return err
	}); err != nil {
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

	// Per-tenant write — wrap in WithOrgTx so RLS lets the insert
	// through (webhook_deliveries' policy joins to webhook_subscriptions
	// for org_id; the policy passes when app.current_org_id matches).
	if err := d.store.WithOrgTx(ctx, sub.OrgID, func(ctx context.Context) error {
		return d.store.CreateWebhookDelivery(ctx, delivery)
	}); err != nil {
		w.Debug("failed to create webhook delivery record", wool.ErrField(err))
		return
	}

	d.dispatchHTTP(ctx, sub, delivery, payloadBytes)
}

// dispatchHTTP performs the actual HTTP POST + result write for a
// previously-created delivery row. Extracted so deliver / TestWebhook
// / ReplayDelivery share the exact transport semantics — same headers,
// same signature, same response capture rules. The delivery row MUST
// already exist in the store (created by the caller) so a partial
// failure leaves an audit trail.
func (d *AsyncWebhookDispatcher) dispatchHTTP(
	ctx context.Context,
	sub *WebhookSubscription,
	delivery *WebhookDelivery,
	payloadBytes []byte,
) {
	w := wool.Get(ctx).In("WebhookDispatcher.dispatchHTTP")

	mac := hmac.New(sha256.New, []byte(sub.Secret))
	mac.Write(payloadBytes)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(payloadBytes))
	if err != nil {
		d.markFailed(ctx, sub, delivery, 0, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", signature)
	req.Header.Set("X-Webhook-Delivery-ID", delivery.ID)
	req.Header.Set("X-Webhook-Event", delivery.EventType)

	resp, err := d.client.Do(req)
	if err != nil {
		d.markFailed(ctx, sub, delivery, 0, err.Error())
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
		d.markFailed(ctx, sub, delivery, resp.StatusCode, string(body))
		return
	}

	if err := d.store.WithOrgTx(ctx, sub.OrgID, func(ctx context.Context) error {
		return d.store.UpdateWebhookDelivery(ctx, delivery)
	}); err != nil {
		w.Debug("failed to update webhook delivery", wool.ErrField(err))
	}
}

// SendOnce is the public entry point used by TestWebhook + ReplayDelivery.
// Caller pre-builds the WebhookDelivery row (which must already exist in
// the store) and the raw payload bytes; we sign + POST + record the
// result inline (no goroutine, returns when the HTTP round-trip finishes).
func (d *AsyncWebhookDispatcher) SendOnce(
	ctx context.Context,
	sub *WebhookSubscription,
	delivery *WebhookDelivery,
	payloadBytes []byte,
) {
	d.dispatchHTTP(ctx, sub, delivery, payloadBytes)
}

// retryBackoffs defines the delay before each retry attempt.
// attempt 1 → 1min, 2 → 5min, 3 → 30min, 4 → 2h, 5 → fail permanently.
var retryBackoffs = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
}

const maxAttempts = 5

// markFailed sets the delivery as retrying with exponential backoff, or failed
// if max attempts reached. sub provides the orgID for the WithOrgTx
// scope so the RLS-policied UPDATE on webhook_deliveries lets the
// write through.
func (d *AsyncWebhookDispatcher) markFailed(ctx context.Context, sub *WebhookSubscription, delivery *WebhookDelivery, httpStatus int, responseBody string) {
	w := wool.Get(ctx).In("WebhookDispatcher.markFailed")

	delivery.HTTPStatus = httpStatus
	delivery.ResponseBody = responseBody
	delivery.AttemptCount++

	if delivery.AttemptCount >= maxAttempts {
		delivery.Status = "failed"
	} else {
		delivery.Status = "retrying"
		idx := delivery.AttemptCount - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(retryBackoffs) {
			idx = len(retryBackoffs) - 1
		}
		nextRetry := time.Now().Add(retryBackoffs[idx])
		delivery.NextRetryAt = &nextRetry
	}

	if err := d.store.WithOrgTx(ctx, sub.OrgID, func(ctx context.Context) error {
		return d.store.UpdateWebhookDelivery(ctx, delivery)
	}); err != nil {
		w.Debug("failed to update failed webhook delivery", wool.ErrField(err))
	}
}

// retryLoop runs RetryPending every 30 seconds until the dispatcher is closed.
func (d *AsyncWebhookDispatcher) retryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = d.RetryPending(context.Background(), 100)
		case <-d.done:
			return
		}
	}
}

// Close stops the dispatcher and drains remaining work.
func (d *AsyncWebhookDispatcher) Close() {
	close(d.done)
	close(d.ch)
}

// RetryPending fetches pending/retrying deliveries and re-attempts delivery.
// Intended to be called periodically (e.g. every minute) from a background ticker.
func (d *AsyncWebhookDispatcher) RetryPending(ctx context.Context, limit int) error {
	w := wool.Get(ctx).In("WebhookDispatcher.RetryPending")

	// Cross-tenant scan: GetPendingDeliveries returns deliveries
	// across all orgs. WithBypass elevates so RLS doesn't filter
	// the worker's poll.
	var deliveries []*WebhookDelivery
	if err := d.store.WithBypass(ctx, func(ctx context.Context) error {
		ds, err := d.store.GetPendingDeliveries(ctx, limit)
		deliveries = ds
		return err
	}); err != nil {
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

	// Cross-tenant lookup: we don't yet know the delivery's tenant
	// (it could be any org). Bypass to find the matching subscription
	// (which carries org_id), then per-tenant ops use sub.OrgID.
	var subs []*WebhookSubscription
	if err := d.store.WithBypass(ctx, func(ctx context.Context) error {
		s, err := d.store.GetActiveWebhookSubscriptions(ctx, delivery.EventType)
		subs = s
		return err
	}); err != nil {
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
			d.markFailed(ctx, sub, delivery, 0, err.Error())
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Webhook-Signature", signature)
		req.Header.Set("X-Webhook-Delivery-ID", delivery.ID)

		resp, err := d.client.Do(req)
		if err != nil {
			d.markFailed(ctx, sub, delivery, 0, err.Error())
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
			if err := d.store.WithOrgTx(ctx, sub.OrgID, func(ctx context.Context) error {
				return d.store.UpdateWebhookDelivery(ctx, delivery)
			}); err != nil {
				w.Debug("failed to update delivery after retry", wool.ErrField(err))
			}
		} else {
			d.markFailed(ctx, sub, delivery, resp.StatusCode, string(body))
		}
		return
	}

	// Subscription no longer active — mark as failed via bypass since
	// we don't have a sub.OrgID to scope to.
	delivery.Status = "failed"
	delivery.ResponseBody = "subscription no longer active"
	_ = d.store.WithBypass(ctx, func(ctx context.Context) error {
		return d.store.UpdateWebhookDelivery(ctx, delivery)
	})
}
