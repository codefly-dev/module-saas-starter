package infra

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

func (s *PostgresStore) CreateWebhookSubscription(ctx context.Context, sub *business.WebhookSubscription) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO webhook_subscriptions (
			id, org_id, url, secret_encrypted, previous_secret_encrypted,
			previous_secret_expires_at, events, description, active
		) VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9)`,
		sub.ID, sub.OrgID, sub.URL, sub.SecretEncrypted,
		nilIfEmpty(sub.PreviousSecretEncrypted), sub.PreviousSecretExpiresAt,
		sub.Events, sub.Description, sub.Active)
	return err
}

// GetWebhookSubscription loads one subscription by primary key. Returns
// (nil, nil) on not-found so callers can distinguish DB errors from
// missing rows without parsing pgx.ErrNoRows.
func (s *PostgresStore) GetWebhookSubscription(ctx context.Context, id string) (*business.WebhookSubscription, error) {
	q := s.getQueryExecutor(ctx)
	var sub business.WebhookSubscription
	err := q.QueryRow(ctx, `
		SELECT id, org_id, url, secret_encrypted,
		       COALESCE(previous_secret_encrypted, ''), previous_secret_expires_at,
		       events, description, active, created_at, updated_at
		FROM webhook_subscriptions
		WHERE id = $1`, id).Scan(
		&sub.ID, &sub.OrgID, &sub.URL, &sub.SecretEncrypted,
		&sub.PreviousSecretEncrypted, &sub.PreviousSecretExpiresAt, &sub.Events,
		&sub.Description, &sub.Active, &sub.CreatedAt, &sub.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// UpdateWebhookSubscription persists changes to URL / secret / events
// / active / description. ID + OrgID are immutable; created_at is
// untouched. Used by RotateWebhookSecret + (future) UpdateSubscription.
func (s *PostgresStore) UpdateWebhookSubscription(ctx context.Context, sub *business.WebhookSubscription) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE webhook_subscriptions
		SET url = $2,
		    secret_encrypted = $3,
		    previous_secret_encrypted = NULLIF($4, ''),
		    previous_secret_expires_at = $5,
		    events = $6,
		    description = $7,
		    active = $8,
		    updated_at = NOW()
		WHERE id = $1`,
		sub.ID, sub.URL, sub.SecretEncrypted, sub.PreviousSecretEncrypted,
		sub.PreviousSecretExpiresAt, sub.Events, sub.Description, sub.Active)
	return err
}

func (s *PostgresStore) DeleteWebhookSubscription(ctx context.Context, id string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) ListWebhookSubscriptions(ctx context.Context, orgID string) ([]*business.WebhookSubscription, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT id, org_id, url, secret_encrypted,
		       COALESCE(previous_secret_encrypted, ''), previous_secret_expires_at,
		       events, description, active, created_at, updated_at
		FROM webhook_subscriptions
		WHERE org_id = $1
		ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*business.WebhookSubscription
	for rows.Next() {
		var sub business.WebhookSubscription
		err := rows.Scan(&sub.ID, &sub.OrgID, &sub.URL, &sub.SecretEncrypted,
			&sub.PreviousSecretEncrypted, &sub.PreviousSecretExpiresAt, &sub.Events,
			&sub.Description, &sub.Active, &sub.CreatedAt, &sub.UpdatedAt)
		if err != nil {
			return nil, err
		}
		subs = append(subs, &sub)
	}
	return subs, nil
}

func (s *PostgresStore) GetActiveWebhookSubscriptions(ctx context.Context, eventType string) ([]*business.WebhookSubscription, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT id, org_id, url, secret_encrypted,
		       COALESCE(previous_secret_encrypted, ''), previous_secret_expires_at,
		       events, description, active, created_at, updated_at
		FROM webhook_subscriptions
		WHERE active = true AND $1 = ANY(events)`, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*business.WebhookSubscription
	for rows.Next() {
		var sub business.WebhookSubscription
		err := rows.Scan(&sub.ID, &sub.OrgID, &sub.URL, &sub.SecretEncrypted,
			&sub.PreviousSecretEncrypted, &sub.PreviousSecretExpiresAt, &sub.Events,
			&sub.Description, &sub.Active, &sub.CreatedAt, &sub.UpdatedAt)
		if err != nil {
			return nil, err
		}
		subs = append(subs, &sub)
	}
	return subs, nil
}

func (s *PostgresStore) CreateWebhookDelivery(ctx context.Context, delivery *business.WebhookDelivery) error {
	q := s.getQueryExecutor(ctx)
	if delivery.EventID == "" {
		delivery.EventID = delivery.ID
	}
	result, err := q.Exec(ctx, `
		INSERT INTO webhook_deliveries (
			id, subscription_id, event_id, outbox_event_id, event_type,
			payload, status, attempts
		) VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8)
		ON CONFLICT (subscription_id, outbox_event_id)
			WHERE outbox_event_id IS NOT NULL
		DO NOTHING`,
		delivery.ID, delivery.SubscriptionID, delivery.EventID,
		delivery.OutboxEventID, delivery.EventType, delivery.Payload,
		delivery.Status, delivery.AttemptCount)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("webhooks: delivery history already exists for this event and subscription")
	}
	return nil
}

// GetWebhookDelivery loads one delivery by primary key. Returns (nil, nil) on
// not-found. Request-scoped Get and Replay use this immutable history lookup.
func (s *PostgresStore) GetWebhookDelivery(ctx context.Context, id string) (*business.WebhookDelivery, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT id, subscription_id, event_id, COALESCE(outbox_event_id::text, ''),
		       event_type, payload, status, http_status, response_body,
		       attempts, last_attempt_at,
		       created_at, updated_at, delivered_at
		FROM webhook_deliveries
		WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanWebhookDeliveries(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out[0], nil
}

func (s *PostgresStore) ListWebhookDeliveries(ctx context.Context, subscriptionID string, pageSize int) ([]*business.WebhookDelivery, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT id, subscription_id, event_id, COALESCE(outbox_event_id::text, ''),
		       event_type, payload, status, http_status, response_body,
		       attempts, last_attempt_at,
		       created_at, updated_at, delivered_at
		FROM webhook_deliveries
		WHERE subscription_id = $1
		ORDER BY created_at DESC
		LIMIT $2`, subscriptionID, pageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanWebhookDeliveries(rows)
}

func scanWebhookDeliveries(rows pgx.Rows) ([]*business.WebhookDelivery, error) {
	var deliveries []*business.WebhookDelivery
	for rows.Next() {
		var d business.WebhookDelivery
		var httpStatus *int
		var responseBody *string

		err := rows.Scan(
			&d.ID, &d.SubscriptionID, &d.EventID, &d.OutboxEventID,
			&d.EventType, &d.Payload, &d.Status, &httpStatus, &responseBody,
			&d.AttemptCount, &d.LastAttemptAt,
			&d.CreatedAt, &d.UpdatedAt, &d.DeliveredAt,
		)
		if err != nil {
			return nil, err
		}
		if httpStatus != nil {
			d.HTTPStatus = *httpStatus
		}
		if responseBody != nil {
			d.ResponseBody = *responseBody
		}
		deliveries = append(deliveries, &d)
	}
	return deliveries, nil
}
