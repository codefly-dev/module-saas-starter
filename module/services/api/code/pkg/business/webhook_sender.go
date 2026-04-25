package business

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"time"

	"github.com/codefly-dev/core/wool"
)

// WebhookSender is the synchronous, user-initiated webhook send path.
// Distinct from AsyncWebhookDispatcher (which has a worker pool +
// retry loop for events fired off the audit log) — Test + Replay are
// invoked from a request handler, so they want inline result so the
// caller can show "delivered in 142ms / 503 Bad Gateway / connection
// refused" right after the click.
//
// Same wire format as the async path: HMAC-SHA256 sig in
// X-Webhook-Signature, X-Webhook-Delivery-ID, X-Webhook-Event headers.
// Response body captured up to 4KiB; status code / latency persisted.
type WebhookSender struct {
	store  Store
	client *http.Client
}

// NewWebhookSender constructs the sender. Reasonable timeouts so a
// dead endpoint doesn't tie up the request goroutine for long.
func NewWebhookSender(store Store) *WebhookSender {
	return &WebhookSender{
		store:  store,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Send fires payloadBytes at sub.URL signed by sub.Secret, persists
// the result on the (already-stored) delivery row, and returns. The
// caller created the WebhookDelivery record before calling this so a
// crash mid-send still leaves an audit trail. Errors are recorded on
// the row, never returned — the caller fetches the row afterwards if
// it needs the outcome.
func (s *WebhookSender) Send(
	ctx context.Context,
	sub *WebhookSubscription,
	delivery *WebhookDelivery,
	payloadBytes []byte,
) {
	w := wool.Get(ctx).In("WebhookSender.Send")

	mac := hmac.New(sha256.New, []byte(sub.Secret))
	mac.Write(payloadBytes)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(payloadBytes))
	if err != nil {
		s.markFailed(ctx, delivery, 0, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", signature)
	req.Header.Set("X-Webhook-Delivery-ID", delivery.ID)
	req.Header.Set("X-Webhook-Event", delivery.EventType)

	resp, err := s.client.Do(req)
	if err != nil {
		s.markFailed(ctx, delivery, 0, err.Error())
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
		delivery.Status = "failed"
	}

	if err := s.store.UpdateWebhookDelivery(ctx, delivery); err != nil {
		w.Debug("failed to update webhook delivery", wool.ErrField(err))
	}
}

func (s *WebhookSender) markFailed(ctx context.Context, delivery *WebhookDelivery, httpStatus int, responseBody string) {
	w := wool.Get(ctx).In("WebhookSender.markFailed")
	delivery.HTTPStatus = httpStatus
	delivery.ResponseBody = responseBody
	delivery.AttemptCount = 1
	delivery.Status = "failed"
	if err := s.store.UpdateWebhookDelivery(ctx, delivery); err != nil {
		w.Debug("failed to update webhook delivery on failure path", wool.ErrField(err))
	}
}
