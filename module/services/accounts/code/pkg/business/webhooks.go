package business

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"
)

var canonicalWebhookEventType = regexp.MustCompile(`^[a-z][a-z0-9._-]*$`)

// ErrWebhookDeliveryExists reports that this endpoint already has history for
// this event. It is the delivery-side half of the at-least-once contract: a
// replayed event re-enters fan-out with the id it was published under, and the
// endpoint has already been told about it.
var ErrWebhookDeliveryExists = errors.New("webhooks: delivery history already exists for this event and subscription")

// WebhookSubscription is the domain representation of a webhook subscription.
type WebhookSubscription struct {
	ID                      string
	OrgID                   string
	URL                     string
	SecretEncrypted         string
	PreviousSecretEncrypted string
	PreviousSecretExpiresAt *time.Time
	SecretReveal            string   // transient: populated only by create
	Events                  []string // event types to subscribe to, e.g. "saas.user.registered", "saas.org.created"
	Description             string
	Active                  bool
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// WebhookDelivery is customer-visible delivery history. Generic job records,
// not this projection, own scheduling, leases, retries, and dead letters.
type WebhookDelivery struct {
	ID             string
	SubscriptionID string
	EventID        string
	OutboxEventID  string
	EventType      string
	Payload        string // JSON payload
	Status         string // "pending", "delivered", "failed"
	HTTPStatus     int
	ResponseBody   string
	AttemptCount   int
	LastAttemptAt  *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeliveredAt    *time.Time
}

// webhookPayload is serialized once and the exact bytes are persisted. Every
// attempt and replay signs those stored bytes rather than re-marshalling data.
type webhookPayload struct {
	EventID    string          `json:"id"`
	EventType  string          `json:"event_type"`
	Data       json.RawMessage `json:"data"`
	Timestamp  string          `json:"timestamp"`
	DeliveryID string          `json:"delivery_id"`
}

// AuditEventWebhookData is the `data` member of the webhook body for one audit
// record. It is the domain event's payload: the emitter publishes these bytes as
// the envelope data, and the relay wraps them in the delivery envelope below, so
// the body an endpoint receives is unchanged by the move to subscriptions.
func AuditEventWebhookData(entry AuditEntry) ([]byte, error) {
	return json.Marshal(map[string]any{
		"event_type": string(entry.EventType),
		// The registered version of this event's contract. A subscriber reads
		// actor_id and actor_type out of this envelope, so when a field's
		// meaning is revised under a name that cannot change (saas.webhook.*
		// v2: actor_id became the initiating user, not the organization) this
		// is the only in-band way to tell which contract a delivery follows.
		"schema_version":  entry.SchemaVersion,
		"resource":        entry.Resource,
		"resource_id":     entry.ResourceID,
		"actor_id":        entry.ActorID,
		"actor_type":      entry.ActorType,
		"organization_id": entry.OrgID,
		"payload":         RedactPayload(entry.EventType, entry.Payload),
	})
}

// NewDomainEventWebhookDelivery renders the pending delivery and the exact bytes
// that will be signed for one event delivered to one endpoint. Every attempt and
// every replay signs the persisted bytes rather than re-marshalling, so this is
// the only place the body is built.
//
// eventID is the envelope id, which is also the audit record's id: it is the
// X-Webhook-Event-ID an endpoint deduplicates on, so it stays stable across the
// move from inline fan-out to relay fan-out.
func NewDomainEventWebhookDelivery(
	eventID, eventType, subscriptionID string,
	occurred time.Time,
	data []byte,
) (*WebhookDelivery, []byte, error) {
	deliveryID := NewIDString()
	payload, err := json.Marshal(webhookPayload{
		EventID:    eventID,
		EventType:  eventType,
		Data:       data,
		Timestamp:  occurred.UTC().Format(time.RFC3339Nano),
		DeliveryID: deliveryID,
	})
	if err != nil {
		return nil, nil, err
	}
	return &WebhookDelivery{
		ID:             deliveryID,
		SubscriptionID: subscriptionID,
		EventID:        eventID,
		OutboxEventID:  eventID,
		EventType:      eventType,
		Payload:        string(payload),
		Status:         "pending",
	}, payload, nil
}

// CreateSubscription validates and stores a new webhook subscription. actor is
// the verified caller the audit trail records as having configured the
// destination.
func (s *Service) CreateSubscription(ctx context.Context, actor AuditActor, orgID, rawURL string, events []string, description string) (*WebhookSubscription, error) {
	w := wool.Get(ctx).In("CreateSubscription")

	if err := actor.validate(); err != nil {
		return nil, w.Wrapf(err, "cannot attribute webhook subscription")
	}
	if s.webhookCipher == nil {
		return nil, w.NewError("webhook secret cipher is not configured")
	}
	policy := s.webhookPolicy.ensureDefaults()
	normalizedURL, err := policy.NormalizeAndValidate(ctx, rawURL)
	if err != nil {
		return nil, w.Wrapf(err, "unsafe webhook URL")
	}

	if len(events) == 0 || len(events) > 64 {
		return nil, w.NewError("at least one event type is required")
	}
	for _, event := range events {
		if event = strings.TrimSpace(event); len(event) > 128 || !canonicalWebhookEventType.MatchString(event) {
			return nil, w.NewError("webhook event types must use lowercase letters, digits, dots, underscores, or hyphens")
		}
	}
	if len(description) > 500 {
		return nil, w.NewError("webhook description must not exceed 500 bytes")
	}

	subscriptionID := NewIDString()
	secret, err := generateWebhookSecret()
	if err != nil {
		return nil, w.Wrapf(err, "cannot generate webhook secret")
	}
	encryptedSecret, err := s.webhookCipher.EncryptSecret(ctx, WebhookSecretPurpose(subscriptionID), secret)
	if err != nil {
		return nil, w.Wrapf(err, "cannot encrypt webhook secret")
	}

	sub := &WebhookSubscription{
		ID:              subscriptionID,
		OrgID:           orgID,
		URL:             normalizedURL,
		SecretEncrypted: encryptedSecret,
		SecretReveal:    secret,
		Events:          events,
		Description:     description,
		Active:          true,
	}

	// RLS: write goes through WithOrgTx so the policy WITH CHECK
	// passes (org_id matches the current_org_id setting).
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		if err := s.store.CreateWebhookSubscription(ctx, sub); err != nil {
			return err
		}
		// The registration is what a customer edits; its subscriptions are derived
		// from it in the same transaction, so an endpoint is never registered
		// without the rows the relay fans out over.
		if err := s.store.SyncWebhookEventSubscriptions(ctx, orgID, sub.ID, sub.Events); err != nil {
			return err
		}
		return s.emitTx(ctx, actor.ID, actor.Type, EventWebhookCreated, "webhook_subscription", sub.ID, orgID, actor.provenance())
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create webhook subscription")
	}

	return sub, nil
}

// DeleteSubscription verifies ownership and deletes a webhook subscription.
//
// RLS does the ownership check for us: the DELETE inside WithOrgTx
// only finds rows belonging to this org. If the id is from a
// different tenant, the DELETE simply affects 0 rows and we report
// "not found" without leaking the existence of cross-tenant data.
func (s *Service) DeleteSubscription(ctx context.Context, actor AuditActor, orgID, id string) error {
	w := wool.Get(ctx).In("DeleteSubscription")

	if err := actor.validate(); err != nil {
		return w.Wrapf(err, "cannot attribute webhook subscription deletion")
	}
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
		return s.emitTx(ctx, actor.ID, actor.Type, EventWebhookDeleted, "webhook_subscription", id, orgID, actor.provenance())
	}); err != nil {
		return err
	}

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

// TestWebhook atomically creates a synthetic delivery and its generated
// generic outbox job. It returns the pending delivery immediately; the same
// leased worker, signing path, retry policy, and history projection used by
// production events perform the network request.
func (s *Service) TestWebhook(ctx context.Context, orgID, subscriptionID, eventType string) (*WebhookDelivery, error) {
	w := wool.Get(ctx).In("TestWebhook")

	if eventType == "" {
		eventType = "webhook.test"
	}
	deliveryID := NewIDString()
	envelope := webhookPayload{
		EventID:    deliveryID,
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
		EventID:        deliveryID,
		EventType:      eventType,
		Payload:        string(payloadBytes),
		Status:         "pending",
		AttemptCount:   0,
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		subscription, err := s.store.GetWebhookSubscription(ctx, subscriptionID)
		if err != nil {
			return w.Wrapf(err, "cannot load subscription")
		}
		if subscription == nil {
			return w.NewError("subscription not found")
		}
		return createOutboundWebhookDelivery(
			ctx, s.store, s.webhookJobs, orgID, delivery, payloadBytes,
		)
	}); err != nil {
		return nil, err
	}
	return delivery, nil
}

// GetWebhookDelivery fetches a single delivery row including the captured
// response body and latest attempt metadata.
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

// ReplayWebhookDelivery creates a new history row and generated outbox job for
// an existing delivery's exact payload. The worker resolves the subscription's
// current URL and secret when it executes; the original row remains immutable.
//
// Authz expectation: caller must already be org-admin on the
// subscription's org — enforced at the adapter layer.
func (s *Service) ReplayWebhookDelivery(ctx context.Context, actor AuditActor, orgID, originalID string) (*WebhookDelivery, error) {
	w := wool.Get(ctx).In("ReplayWebhookDelivery")

	if err := actor.validate(); err != nil {
		return nil, w.Wrapf(err, "cannot attribute webhook delivery replay")
	}
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
		sb, err := s.store.GetWebhookSubscription(ctx, o.SubscriptionID)
		if err != nil {
			return w.Wrapf(err, "cannot load subscription")
		}
		if sb == nil {
			return w.NewError("subscription not found (deleted?)")
		}
		sub = sb

		replay.SubscriptionID = o.SubscriptionID
		replay.EventID = o.EventID
		if replay.EventID == "" {
			replay.EventID = o.ID
		}
		replay.EventType = o.EventType
		replay.Payload = o.Payload
		if err := createOutboundWebhookDelivery(
			ctx, s.store, s.webhookJobs, orgID, replay, []byte(o.Payload),
		); err != nil {
			return err
		}
		return s.emitTx(ctx, actor.ID, actor.Type, EventWebhookReplayed, "webhook_delivery", replay.ID, sub.OrgID, actor.provenance())
	}); err != nil {
		return nil, err
	}
	return replay, nil
}

// RotateWebhookSecret generates and encrypts a new signing secret. During the
// requested overlap, deliveries carry signatures from both the new and prior
// keys so consumers can deploy the new verifier without an outage.
func (s *Service) RotateWebhookSecret(ctx context.Context, actor AuditActor, orgID, subscriptionID string, gracePeriod time.Duration) (string, *time.Time, error) {
	w := wool.Get(ctx).In("RotateWebhookSecret")
	if err := actor.validate(); err != nil {
		return "", nil, w.Wrapf(err, "cannot attribute webhook secret rotation")
	}
	if s.webhookCipher == nil {
		return "", nil, w.NewError("webhook secret cipher is not configured")
	}
	if gracePeriod < 0 || gracePeriod > 7*24*time.Hour {
		return "", nil, w.NewError("webhook secret grace period must be between 0 and 7 days")
	}

	newSecret, err := generateWebhookSecret()
	if err != nil {
		return "", nil, w.Wrapf(err, "rng failure")
	}
	encryptedSecret, err := s.webhookCipher.EncryptSecret(ctx, WebhookSecretPurpose(subscriptionID), newSecret)
	if err != nil {
		return "", nil, w.Wrapf(err, "cannot encrypt webhook secret")
	}
	var oldSecretExpiresAt *time.Time

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		sub, err := s.store.GetWebhookSubscription(ctx, subscriptionID)
		if err != nil {
			return w.Wrapf(err, "cannot load subscription")
		}
		if sub == nil {
			return w.NewError("subscription not found")
		}
		if gracePeriod > 0 {
			expiresAt := time.Now().UTC().Add(gracePeriod)
			sub.PreviousSecretEncrypted = sub.SecretEncrypted
			sub.PreviousSecretExpiresAt = &expiresAt
			oldSecretExpiresAt = &expiresAt
		} else {
			sub.PreviousSecretEncrypted = ""
			sub.PreviousSecretExpiresAt = nil
		}
		sub.SecretEncrypted = encryptedSecret
		if err := s.store.UpdateWebhookSubscription(ctx, sub); err != nil {
			return err
		}
		return s.emitTx(ctx, actor.ID, actor.Type, EventWebhookSecretRotated, "webhook_subscription", subscriptionID, orgID, actor.provenance())
	}); err != nil {
		return "", nil, err
	}
	return newSecret, oldSecretExpiresAt, nil
}

func generateWebhookSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("read cryptographic random bytes: %w", err)
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(raw), nil
}
