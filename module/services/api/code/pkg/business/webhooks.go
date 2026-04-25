package business

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"time"

	"github.com/codefly-dev/core/wool"
)

// WebhookSubscription is the domain representation of a webhook subscription.
type WebhookSubscription struct {
	ID          string
	OrgID       string
	URL         string
	Secret      string
	Events      []string // event types to subscribe to, e.g. "user.registered", "org.created"
	Description string
	Active      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// WebhookDelivery tracks a single delivery attempt for a webhook.
type WebhookDelivery struct {
	ID             string
	SubscriptionID string
	EventType      string
	Payload        string // JSON payload
	Status         string // "pending", "delivered", "failed", "retrying"
	HTTPStatus     int
	ResponseBody   string
	AttemptCount   int
	NextRetryAt    *time.Time
	CreatedAt      time.Time
	DeliveredAt    *time.Time
}

// CreateSubscription validates and stores a new webhook subscription.
func (s *Service) CreateSubscription(ctx context.Context, orgID, rawURL, secret string, events []string, description string) (*WebhookSubscription, error) {
	w := wool.Get(ctx).In("CreateSubscription")

	// Validate URL
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, w.NewError("invalid webhook URL: %s", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, w.NewError("webhook URL must use http or https scheme")
	}
	if u.Host == "" {
		return nil, w.NewError("webhook URL must have a host")
	}

	if len(events) == 0 {
		return nil, w.NewError("at least one event type is required")
	}

	sub := &WebhookSubscription{
		ID:          NewIDString(),
		OrgID:       orgID,
		URL:         rawURL,
		Secret:      secret,
		Events:      events,
		Description: description,
		Active:      true,
	}

	if err := s.store.CreateWebhookSubscription(ctx, sub); err != nil {
		return nil, w.Wrapf(err, "cannot create webhook subscription")
	}

	s.emit(ctx, orgID, "system", "webhook.created", "webhook_subscription", sub.ID, orgID)

	return sub, nil
}

// DeleteSubscription verifies ownership and deletes a webhook subscription.
func (s *Service) DeleteSubscription(ctx context.Context, orgID, id string) error {
	w := wool.Get(ctx).In("DeleteSubscription")

	// Verify ownership by listing org subscriptions and checking membership
	subs, err := s.store.ListWebhookSubscriptions(ctx, orgID)
	if err != nil {
		return w.Wrapf(err, "cannot list subscriptions")
	}

	found := false
	for _, sub := range subs {
		if sub.ID == id {
			found = true
			break
		}
	}
	if !found {
		return w.NewError("webhook subscription not found or does not belong to this organization")
	}

	if err := s.store.DeleteWebhookSubscription(ctx, id); err != nil {
		return w.Wrapf(err, "cannot delete webhook subscription")
	}

	s.emit(ctx, orgID, "system", "webhook.deleted", "webhook_subscription", id, orgID)

	return nil
}

// ListSubscriptions returns all active webhook subscriptions for an org.
func (s *Service) ListSubscriptions(ctx context.Context, orgID string) ([]*WebhookSubscription, error) {
	return s.store.ListWebhookSubscriptions(ctx, orgID)
}

// ListDeliveries returns recent deliveries for a webhook subscription.
func (s *Service) ListDeliveries(ctx context.Context, subscriptionID string, pageSize int) ([]*WebhookDelivery, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	return s.store.ListWebhookDeliveries(ctx, subscriptionID, pageSize)
}

// TestWebhook fires a synthetic event at the subscription's URL and
// returns the resulting delivery row (delivered / failed status set
// inline before return — caller doesn't need to poll).
//
// eventType picks the event name on the envelope; empty defaults to
// "webhook.test". The payload data is server-generated sample JSON
// matching the same envelope the async dispatcher uses, so consumer-
// side validation logic can treat tests identically to real events.
func (s *Service) TestWebhook(ctx context.Context, subscriptionID, eventType string) (*WebhookDelivery, error) {
	w := wool.Get(ctx).In("TestWebhook")

	sub, err := s.store.GetWebhookSubscription(ctx, subscriptionID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot load subscription")
	}
	if sub == nil {
		return nil, w.NewError("subscription not found")
	}

	if eventType == "" {
		eventType = "webhook.test"
	}

	deliveryID := NewIDString()
	envelope := webhookPayload{
		EventType:  eventType,
		Data:       json.RawMessage(`{"message":"This is a test webhook delivery.","test":true}`),
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		DeliveryID: deliveryID,
	}
	payloadBytes, err := json.Marshal(envelope)
	if err != nil {
		return nil, w.Wrapf(err, "cannot marshal test payload")
	}

	delivery := &WebhookDelivery{
		ID:             deliveryID,
		SubscriptionID: subscriptionID,
		EventType:      eventType,
		Payload:        string(payloadBytes),
		Status:         "pending",
		AttemptCount:   0,
	}
	if err := s.store.CreateWebhookDelivery(ctx, delivery); err != nil {
		return nil, w.Wrapf(err, "cannot create test webhook delivery")
	}

	s.webhookSenderOrDefault().Send(ctx, sub, delivery, payloadBytes)

	// Re-load — Send() mutates the row in-place but the store has the
	// most reliable answer after the round trip.
	updated, gerr := s.store.GetWebhookDelivery(ctx, deliveryID)
	if gerr != nil || updated == nil {
		return delivery, nil // best-effort fallback to the in-memory copy
	}
	return updated, nil
}

// GetWebhookDelivery fetches a single delivery row including the
// captured response body and retry timing. Used by the Stripe-style
// "deliveries" detail panel; the list endpoint returns the same
// shape but the FE pulls the full record again on row click to get
// the latest state.
func (s *Service) GetWebhookDelivery(ctx context.Context, deliveryID string) (*WebhookDelivery, error) {
	w := wool.Get(ctx).In("GetWebhookDelivery")
	d, err := s.store.GetWebhookDelivery(ctx, deliveryID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot load delivery")
	}
	if d == nil {
		return nil, w.NewError("delivery not found")
	}
	return d, nil
}

// ReplayWebhookDelivery re-fires an existing delivery's payload at
// the subscription's CURRENT url + secret (so a delivery that
// originally 503'd against an old endpoint can be replayed against
// the fixed one). Creates a NEW delivery row so the audit trail
// captures both attempts; the original row is unchanged.
//
// Authz expectation: caller must already be org-admin on the
// subscription's org — enforced at the adapter layer.
func (s *Service) ReplayWebhookDelivery(ctx context.Context, originalID string) (*WebhookDelivery, error) {
	w := wool.Get(ctx).In("ReplayWebhookDelivery")

	original, err := s.store.GetWebhookDelivery(ctx, originalID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot load original delivery")
	}
	if original == nil {
		return nil, w.NewError("original delivery not found")
	}

	sub, err := s.store.GetWebhookSubscription(ctx, original.SubscriptionID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot load subscription")
	}
	if sub == nil {
		return nil, w.NewError("subscription not found (deleted?)")
	}

	replay := &WebhookDelivery{
		ID:             NewIDString(),
		SubscriptionID: original.SubscriptionID,
		EventType:      original.EventType,
		Payload:        original.Payload,
		Status:         "pending",
		AttemptCount:   0,
	}
	if err := s.store.CreateWebhookDelivery(ctx, replay); err != nil {
		return nil, w.Wrapf(err, "cannot create replay row")
	}

	s.webhookSenderOrDefault().Send(ctx, sub, replay, []byte(original.Payload))

	updated, gerr := s.store.GetWebhookDelivery(ctx, replay.ID)
	if gerr != nil || updated == nil {
		return replay, nil
	}

	s.emit(ctx, sub.OrgID, "system", "webhook.replayed", "webhook_delivery", replay.ID, sub.OrgID)
	return updated, nil
}

// RotateWebhookSecret generates a new HMAC secret for the
// subscription, stores it, and returns the plaintext to the caller
// ONCE. The old secret is dropped immediately on the server side;
// the caller already has both old and new at this moment (the API
// just returned the new one), so they can flip their verifier at
// their own pace.
func (s *Service) RotateWebhookSecret(ctx context.Context, subscriptionID string) (string, error) {
	w := wool.Get(ctx).In("RotateWebhookSecret")

	sub, err := s.store.GetWebhookSubscription(ctx, subscriptionID)
	if err != nil {
		return "", w.Wrapf(err, "cannot load subscription")
	}
	if sub == nil {
		return "", w.NewError("subscription not found")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", w.Wrapf(err, "rng failure")
	}
	newSecret := "whsec_" + base64.RawURLEncoding.EncodeToString(raw)

	sub.Secret = newSecret
	if err := s.store.UpdateWebhookSubscription(ctx, sub); err != nil {
		return "", w.Wrapf(err, "cannot persist new secret")
	}

	s.emit(ctx, sub.OrgID, "system", "webhook.secret_rotated", "webhook_subscription", sub.ID, sub.OrgID)
	return newSecret, nil
}

