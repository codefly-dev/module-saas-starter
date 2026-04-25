package infra

import (
	"context"

	"github.com/jackc/pgx/v5"

	"api/pkg/business"
)

func (s *PostgresStore) CreateWebhookSubscription(ctx context.Context, sub *business.WebhookSubscription) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO webhook_subscriptions (id, org_id, url, secret, events, description, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sub.ID, sub.OrgID, sub.URL, sub.Secret, sub.Events, sub.Description, sub.Active)
	return err
}

// GetWebhookSubscription loads one subscription by primary key. Returns
// (nil, nil) on not-found so callers can distinguish DB errors from
// missing rows without parsing pgx.ErrNoRows.
func (s *PostgresStore) GetWebhookSubscription(ctx context.Context, id string) (*business.WebhookSubscription, error) {
	q := s.getQueryExecutor(ctx)
	var sub business.WebhookSubscription
	err := q.QueryRow(ctx, `
		SELECT id, org_id, url, secret, events, description, active, created_at, updated_at
		FROM webhook_subscriptions
		WHERE id = $1`, id).Scan(
		&sub.ID, &sub.OrgID, &sub.URL, &sub.Secret, &sub.Events,
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
		SET url = $2, secret = $3, events = $4, description = $5, active = $6, updated_at = NOW()
		WHERE id = $1`,
		sub.ID, sub.URL, sub.Secret, sub.Events, sub.Description, sub.Active)
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
		SELECT id, org_id, url, secret, events, description, active, created_at, updated_at
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
		err := rows.Scan(&sub.ID, &sub.OrgID, &sub.URL, &sub.Secret, &sub.Events,
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
		SELECT id, org_id, url, secret, events, description, active, created_at, updated_at
		FROM webhook_subscriptions
		WHERE active = true AND $1 = ANY(events)`, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*business.WebhookSubscription
	for rows.Next() {
		var sub business.WebhookSubscription
		err := rows.Scan(&sub.ID, &sub.OrgID, &sub.URL, &sub.Secret, &sub.Events,
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
	_, err := q.Exec(ctx, `
		INSERT INTO webhook_deliveries (id, subscription_id, event_type, payload, status, attempts)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		delivery.ID, delivery.SubscriptionID, delivery.EventType,
		delivery.Payload, delivery.Status, delivery.AttemptCount)
	return err
}

func (s *PostgresStore) UpdateWebhookDelivery(ctx context.Context, delivery *business.WebhookDelivery) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE webhook_deliveries
		SET status = $2, http_status = $3, response_body = $4,
		    attempts = $5, next_retry_at = $6, delivered_at = $7
		WHERE id = $1`,
		delivery.ID, delivery.Status, delivery.HTTPStatus, delivery.ResponseBody,
		delivery.AttemptCount, delivery.NextRetryAt, delivery.DeliveredAt)
	return err
}

// GetWebhookDelivery loads one delivery by primary key. Returns
// (nil, nil) on not-found. Used by GetDelivery + ReplayDelivery to
// reload the row after the synchronous send mutates it.
func (s *PostgresStore) GetWebhookDelivery(ctx context.Context, id string) (*business.WebhookDelivery, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT id, subscription_id, event_type, payload, status, http_status,
		       response_body, attempts, next_retry_at, created_at, delivered_at
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
		SELECT id, subscription_id, event_type, payload, status, http_status,
		       response_body, attempts, next_retry_at, created_at, delivered_at
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

func (s *PostgresStore) GetPendingDeliveries(ctx context.Context, limit int) ([]*business.WebhookDelivery, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT id, subscription_id, event_type, payload, status, http_status,
		       response_body, attempts, next_retry_at, created_at, delivered_at
		FROM webhook_deliveries
		WHERE status = 'retrying' AND (next_retry_at IS NULL OR next_retry_at <= NOW())
		ORDER BY created_at ASC
		LIMIT $1`, limit)
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

		err := rows.Scan(&d.ID, &d.SubscriptionID, &d.EventType, &d.Payload, &d.Status,
			&httpStatus, &responseBody, &d.AttemptCount, &d.NextRetryAt, &d.CreatedAt, &d.DeliveredAt)
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
