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

func (s *PostgresStore) DeleteExpiredAuthenticationCeremonies(
	ctx context.Context, before time.Time,
) (webauthn, mfaLogin, clientCodes int64, err error) {
	q := s.getQueryExecutor(ctx)
	webauthnTag, err := q.Exec(ctx, `DELETE FROM webauthn_ceremonies WHERE expires_at < $1`, before)
	if err != nil {
		return 0, 0, 0, err
	}
	loginTag, err := q.Exec(ctx, `DELETE FROM mfa_login_transactions WHERE expires_at < $1`, before)
	if err != nil {
		return 0, 0, 0, err
	}
	// A client authorization code is the same kind of hand-off: dead a minute
	// after it is issued, whether or not it was redeemed, and nothing else
	// deletes it. Without this the table grows by one row per client sign-in
	// forever, each pinning a sessions row through its foreign key.
	codeTag, err := q.Exec(ctx, `DELETE FROM client_authorization_codes WHERE expires_at < $1`, before)
	if err != nil {
		return 0, 0, 0, err
	}
	return webauthnTag.RowsAffected(), loginTag.RowsAffected(), codeTag.RowsAffected(), nil
}
