package infra

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

func (s *PostgresStore) GetUserConsent(
	ctx context.Context,
	userID string,
) (string, *time.Time, error) {
	var version *string
	var acceptedAt *time.Time
	err := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT terms_version, terms_accepted_at FROM users WHERE uuid = $1`,
		userID,
	).Scan(&version, &acceptedAt)
	if err == pgx.ErrNoRows {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if version == nil {
		return "", acceptedAt, nil
	}
	return *version, acceptedAt, nil
}

func (s *PostgresStore) SetUserConsent(
	ctx context.Context,
	userID, version string,
	acceptedAt time.Time,
) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE users
		SET terms_version = $2, terms_accepted_at = $3
		WHERE uuid = $1`,
		userID, version, acceptedAt)
	return err
}

func (s *PostgresStore) GetUserConsentPreferences(
	ctx context.Context,
	userID string,
) ([]*business.ConsentPreference, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		SELECT purpose, granted, policy_version, updated_at, withdrawn_at
		FROM user_consent_preferences
		WHERE user_id = $1
		ORDER BY purpose`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var preferences []*business.ConsentPreference
	for rows.Next() {
		var preference business.ConsentPreference
		if err := rows.Scan(
			&preference.Purpose,
			&preference.Granted,
			&preference.PolicyVersion,
			&preference.UpdatedAt,
			&preference.WithdrawnAt,
		); err != nil {
			return nil, err
		}
		preferences = append(preferences, &preference)
	}
	return preferences, rows.Err()
}

func (s *PostgresStore) SetUserConsentPreferences(
	ctx context.Context,
	userID string,
	preferences []*business.ConsentPreference,
	region, consentContext string,
) error {
	for _, preference := range preferences {
		var withdrawnAt *time.Time
		if !preference.Granted && preference.Purpose != "necessary" {
			withdrawnAt = &preference.UpdatedAt
		}
		if _, err := s.getQueryExecutor(ctx).Exec(ctx, `
			INSERT INTO user_consent_preferences (
				user_id, purpose, granted, policy_version, region, context,
				updated_at, withdrawn_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (user_id, purpose) DO UPDATE SET
				granted = EXCLUDED.granted,
				policy_version = EXCLUDED.policy_version,
				region = EXCLUDED.region,
				context = EXCLUDED.context,
				updated_at = EXCLUDED.updated_at,
				withdrawn_at = CASE
					WHEN user_consent_preferences.granted AND NOT EXCLUDED.granted
					THEN EXCLUDED.updated_at
					WHEN EXCLUDED.granted THEN NULL
					ELSE user_consent_preferences.withdrawn_at
				END`,
			userID,
			preference.Purpose,
			preference.Granted,
			preference.PolicyVersion,
			region,
			consentContext,
			preference.UpdatedAt,
			withdrawnAt,
		); err != nil {
			return err
		}
		if _, err := s.getQueryExecutor(ctx).Exec(ctx, `
			INSERT INTO user_consent_events (
				id, user_id, purpose, granted, policy_version, region, context, recorded_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			business.NewIDString(),
			userID,
			preference.Purpose,
			preference.Granted,
			preference.PolicyVersion,
			region,
			consentContext,
			preference.UpdatedAt,
		); err != nil {
			return err
		}
	}
	return nil
}
