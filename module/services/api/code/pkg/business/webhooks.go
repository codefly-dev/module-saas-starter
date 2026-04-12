package business

import (
	"context"
	"fmt"
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

// TestWebhook creates a test delivery with a sample payload for the given subscription.
func (s *Service) TestWebhook(ctx context.Context, subscriptionID string) (*WebhookDelivery, error) {
	w := wool.Get(ctx).In("TestWebhook")

	delivery := &WebhookDelivery{
		ID:             NewIDString(),
		SubscriptionID: subscriptionID,
		EventType:      "webhook.test",
		Payload:        fmt.Sprintf(`{"event_type":"webhook.test","delivery_id":"%s","timestamp":"%s","data":{"message":"This is a test webhook delivery."}}`, NewIDString(), time.Now().UTC().Format(time.RFC3339)),
		Status:         "pending",
		AttemptCount:   0,
	}

	if err := s.store.CreateWebhookDelivery(ctx, delivery); err != nil {
		return nil, w.Wrapf(err, "cannot create test webhook delivery")
	}

	return delivery, nil
}
