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

	// RLS: write goes through WithOrgTx so the policy WITH CHECK
	// passes (org_id matches the current_org_id setting).
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.CreateWebhookSubscription(ctx, sub)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create webhook subscription")
	}

	s.emit(ctx, orgID, "system", "webhook.created", "webhook_subscription", sub.ID, orgID)

	return sub, nil
}

// DeleteSubscription verifies ownership and deletes a webhook subscription.
//
// RLS does the ownership check for us: the DELETE inside WithOrgTx
// only finds rows belonging to this org. If the id is from a
// different tenant, the DELETE simply affects 0 rows and we report
// "not found" without leaking the existence of cross-tenant data.
func (s *Service) DeleteSubscription(ctx context.Context, orgID, id string) error {
	w := wool.Get(ctx).In("DeleteSubscription")

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		// Confirm row exists in THIS org first — RLS makes the lookup
		// safe; a cross-tenant id returns nil, not the other org's row.
		sub, err := s.store.GetWebhookSubscription(ctx, id)
		if err != nil {
			return w.Wrapf(err, "cannot load subscription")
		}
		if sub == nil {
			return w.NewError("webhook subscription not found")
		}
		if err := s.store.DeleteWebhookSubscription(ctx, id); err != nil {
			return w.Wrapf(err, "cannot delete webhook subscription")
		}
		return nil
	}); err != nil {
		return err
	}

	s.emit(ctx, orgID, "system", "webhook.deleted", "webhook_subscription", id, orgID)

	return nil
}

// ListSubscriptions returns all active webhook subscriptions for an org.
func (s *Service) ListSubscriptions(ctx context.Context, orgID string) ([]*WebhookSubscription, error) {
	var out []*WebhookSubscription
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		subs, err := s.store.ListWebhookSubscriptions(ctx, orgID)
		out = subs
		return err
	})
	return out, err
}

// ListDeliveries returns recent deliveries for a webhook subscription.
//
// orgID is the caller's org from the auth context. Required: under
// RLS, ListWebhookDeliveries needs the tx's app.current_org_id set
// or it returns zero rows. The subscription_id alone isn't enough —
// the policy on webhook_deliveries joins to webhook_subscriptions to
// check the parent's org_id matches.
func (s *Service) ListDeliveries(ctx context.Context, orgID, subscriptionID string, pageSize int) ([]*WebhookDelivery, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	var out []*WebhookDelivery
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		ds, err := s.store.ListWebhookDeliveries(ctx, subscriptionID, pageSize)
		out = ds
		return err
	})
	return out, err
}

// TestWebhook fires a synthetic event at the subscription's URL and
// returns the resulting delivery row (delivered / failed status set
// inline before return — caller doesn't need to poll).
//
// eventType picks the event name on the envelope; empty defaults to
// "webhook.test". The payload data is server-generated sample JSON
// matching the same envelope the async dispatcher uses, so consumer-
// side validation logic can treat tests identically to real events.
// TestWebhook fires a synthetic event at the subscription's URL.
// orgID is the caller's tenant — required so the lookup, the
// delivery insert, and the row re-load all happen inside WithOrgTx
// and the RLS policies on webhook_subscriptions / webhook_deliveries
// scope to the right tenant. A subscription_id from a different
// tenant returns "not found" via the policy, never the cross-tenant
// row.
//
// The HTTP-send to the customer's endpoint runs OUTSIDE the
// WithOrgTx (after we exit the closure) — keeping a tx open during
// a network round-trip would tie up a connection for up to 10s.
// A separate WithOrgTx wraps the post-send re-load.
func (s *Service) TestWebhook(ctx context.Context, orgID, subscriptionID, eventType string) (*WebhookDelivery, error) {
	w := wool.Get(ctx).In("TestWebhook")

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

	var sub *WebhookSubscription
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		s2, err := s.store.GetWebhookSubscription(ctx, subscriptionID)
		if err != nil {
			return w.Wrapf(err, "cannot load subscription")
		}
		if s2 == nil {
			return w.NewError("subscription not found")
		}
		sub = s2
		return s.store.CreateWebhookDelivery(ctx, delivery)
	}); err != nil {
		return nil, err
	}

	// Send (HTTP) outside the tx — long-running.
	s.webhookSenderOrDefault().Send(ctx, sub, delivery, payloadBytes)

	// Re-load the row (Send mutates in-place, but DB has the source of
	// truth post-round-trip).
	var updated *WebhookDelivery
	_ = s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		u, err := s.store.GetWebhookDelivery(ctx, deliveryID)
		updated = u
		return err
	})
	if updated != nil {
		return updated, nil
	}
	return delivery, nil
}

// GetWebhookDelivery fetches a single delivery row including the
// captured response body and retry timing. Used by the Stripe-style
// "deliveries" detail panel.
//
// orgID gates the lookup via RLS — a delivery from another tenant
// returns "not found" instead of leaking.
func (s *Service) GetWebhookDelivery(ctx context.Context, orgID, deliveryID string) (*WebhookDelivery, error) {
	w := wool.Get(ctx).In("GetWebhookDelivery")
	var d *WebhookDelivery
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		got, err := s.store.GetWebhookDelivery(ctx, deliveryID)
		d = got
		return err
	})
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
func (s *Service) ReplayWebhookDelivery(ctx context.Context, orgID, originalID string) (*WebhookDelivery, error) {
	w := wool.Get(ctx).In("ReplayWebhookDelivery")

	var original *WebhookDelivery
	var sub *WebhookSubscription
	replay := &WebhookDelivery{
		ID:           NewIDString(),
		Status:       "pending",
		AttemptCount: 0,
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		o, err := s.store.GetWebhookDelivery(ctx, originalID)
		if err != nil {
			return w.Wrapf(err, "cannot load original delivery")
		}
		if o == nil {
			return w.NewError("original delivery not found")
		}
		original = o

		sb, err := s.store.GetWebhookSubscription(ctx, o.SubscriptionID)
		if err != nil {
			return w.Wrapf(err, "cannot load subscription")
		}
		if sb == nil {
			return w.NewError("subscription not found (deleted?)")
		}
		sub = sb

		replay.SubscriptionID = o.SubscriptionID
		replay.EventType = o.EventType
		replay.Payload = o.Payload
		return s.store.CreateWebhookDelivery(ctx, replay)
	}); err != nil {
		return nil, err
	}

	s.webhookSenderOrDefault().Send(ctx, sub, replay, []byte(original.Payload))

	var updated *WebhookDelivery
	_ = s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		u, err := s.store.GetWebhookDelivery(ctx, replay.ID)
		updated = u
		return err
	})

	s.emit(ctx, sub.OrgID, "system", "webhook.replayed", "webhook_delivery", replay.ID, sub.OrgID)
	if updated != nil {
		return updated, nil
	}
	return replay, nil
}

// RotateWebhookSecret generates a new HMAC secret for the
// subscription, stores it, and returns the plaintext to the caller
// ONCE. The old secret is dropped immediately on the server side;
// the caller already has both old and new at this moment (the API
// just returned the new one), so they can flip their verifier at
// their own pace.
func (s *Service) RotateWebhookSecret(ctx context.Context, orgID, subscriptionID string) (string, error) {
	w := wool.Get(ctx).In("RotateWebhookSecret")

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", w.Wrapf(err, "rng failure")
	}
	newSecret := "whsec_" + base64.RawURLEncoding.EncodeToString(raw)

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		sub, err := s.store.GetWebhookSubscription(ctx, subscriptionID)
		if err != nil {
			return w.Wrapf(err, "cannot load subscription")
		}
		if sub == nil {
			return w.NewError("subscription not found")
		}
		sub.Secret = newSecret
		return s.store.UpdateWebhookSubscription(ctx, sub)
	}); err != nil {
		return "", err
	}

	s.emit(ctx, orgID, "system", "webhook.secret_rotated", "webhook_subscription", subscriptionID, orgID)
	return newSecret, nil
}


