package infra

import (
	"context"
	"time"

	"accounts/pkg/business"
)

func (s *PostgresStore) GetRetentionPolicies(ctx context.Context) ([]*business.RetentionPolicy, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT id, resource_type, retention_days, created_at
		FROM data_retention_policies
		ORDER BY resource_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var policies []*business.RetentionPolicy
	for rows.Next() {
		var p business.RetentionPolicy
		if err := rows.Scan(&p.ID, &p.ResourceType, &p.RetentionDays, &p.CreatedAt); err != nil {
			return nil, err
		}
		policies = append(policies, &p)
	}
	return policies, rows.Err()
}

func (s *PostgresStore) DeleteOldAuditEvents(ctx context.Context, before time.Time) (int64, error) {
	q := s.getQueryExecutor(ctx)
	tag, err := q.Exec(ctx, `DELETE FROM audit_events WHERE created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresStore) DeleteOldSessions(ctx context.Context, before time.Time) (int64, error) {
	q := s.getQueryExecutor(ctx)
	tag, err := q.Exec(ctx, `DELETE FROM sessions WHERE created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresStore) DeleteOldWebhookDeliveries(ctx context.Context, before time.Time) (int64, error) {
	q := s.getQueryExecutor(ctx)
	tag, err := q.Exec(ctx, `DELETE FROM webhook_deliveries WHERE created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresStore) DeleteOldNotifications(ctx context.Context, before time.Time) (int64, error) {
	q := s.getQueryExecutor(ctx)
	tag, err := q.Exec(ctx, `DELETE FROM notifications WHERE created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
