package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const webhookProjectionDatabaseRole = "app_webhook_worker"

func NewWebhookProjectionPool(ctx context.Context) (*pgxpool.Pool, error) {
	w := wool.Get(ctx).In("NewWebhookProjectionPool")
	connection, err := codefly.For(ctx).Service("store").Secret("postgres", "read-write-connection")
	if err != nil {
		return nil, w.Wrapf(err, "failed to get connection string")
	}
	return NewWebhookProjectionPoolFromURL(ctx, connection)
}

func NewWebhookProjectionPoolFromURL(ctx context.Context, connectionURL string) (*pgxpool.Pool, error) {
	return newWorkerPoolFromURL(ctx, connectionURL, workerPoolConfig{
		role:            webhookProjectionDatabaseRole,
		applicationName: "accounts-webhook-projection",
		logScope:        "NewWebhookProjectionPool",
	})
}

// PostgresWebhookProjection is deliberately not a queue. It can load immutable
// delivery input and record customer-visible attempt outcomes only.
type PostgresWebhookProjection struct {
	pool *pgxpool.Pool
}

func NewPostgresWebhookProjection(pool *pgxpool.Pool) *PostgresWebhookProjection {
	return &PostgresWebhookProjection{pool: pool}
}

var _ business.OutboundWebhookProjection = (*PostgresWebhookProjection)(nil)

func (s *PostgresWebhookProjection) LoadOutboundWebhookDelivery(
	ctx context.Context,
	deliveryID string,
) (*business.WebhookDelivery, *business.WebhookSubscription, error) {
	delivery := &business.WebhookDelivery{}
	subscription := &business.WebhookSubscription{}
	var httpStatus *int
	var responseBody *string
	err := s.pool.QueryRow(ctx, `
		SELECT
			d.id, d.subscription_id, d.event_id,
			COALESCE(d.outbox_event_id::text, ''), d.event_type, d.payload,
			d.status, d.http_status, d.response_body, d.attempts,
			d.last_attempt_at, d.created_at, d.updated_at, d.delivered_at,
			s.id, s.org_id, s.url, s.secret_encrypted,
			COALESCE(s.previous_secret_encrypted, ''),
			s.previous_secret_expires_at, s.events, s.description, s.active,
			s.created_at, s.updated_at
		FROM webhook_deliveries d
		JOIN webhook_subscriptions s ON s.id = d.subscription_id
		WHERE d.id = $1`, deliveryID,
	).Scan(
		&delivery.ID, &delivery.SubscriptionID, &delivery.EventID,
		&delivery.OutboxEventID, &delivery.EventType, &delivery.Payload,
		&delivery.Status, &httpStatus, &responseBody, &delivery.AttemptCount,
		&delivery.LastAttemptAt, &delivery.CreatedAt, &delivery.UpdatedAt,
		&delivery.DeliveredAt,
		&subscription.ID, &subscription.OrgID, &subscription.URL,
		&subscription.SecretEncrypted, &subscription.PreviousSecretEncrypted,
		&subscription.PreviousSecretExpiresAt, &subscription.Events,
		&subscription.Description, &subscription.Active,
		&subscription.CreatedAt, &subscription.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if httpStatus != nil {
		delivery.HTTPStatus = *httpStatus
	}
	if responseBody != nil {
		delivery.ResponseBody = *responseBody
	}
	return delivery, subscription, nil
}

func (s *PostgresWebhookProjection) RecordOutboundWebhookAttempt(
	ctx context.Context,
	attempt business.OutboundWebhookAttempt,
) error {
	if attempt.DeliveryID == "" || attempt.Attempt == 0 {
		return errors.New("webhooks: delivery id and positive attempt are required")
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET status = CASE WHEN $5::timestamptz IS NULL THEN 'failed' ELSE 'delivered' END,
		    http_status = NULLIF($3, 0),
		    response_body = NULLIF($4, ''),
		    attempts = GREATEST(attempts, $2),
		    last_attempt_at = NOW(),
		    delivered_at = $5,
		    updated_at = NOW()
		WHERE id = $1`,
		attempt.DeliveryID, attempt.Attempt, attempt.HTTPStatus,
		truncateWebhookProjectionValue(attempt.ResponseBody, 4096), attempt.DeliveredAt,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("webhooks: delivery projection %s is unavailable", attempt.DeliveryID)
	}
	return nil
}

func truncateWebhookProjectionValue(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
