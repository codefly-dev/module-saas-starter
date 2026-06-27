package infra

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// GetUserConsent reads the (terms_version, terms_accepted_at) pair
// off the users row. NULL columns map to ("", nil) — the canonical
// "never accepted" shape the consent service compares against
// CurrentTermsVersion to decide whether to show the banner.
func (s *PostgresStore) GetUserConsent(
	ctx context.Context,
	userID string,
) (string, *time.Time, error) {
	q := s.getQueryExecutor(ctx)
	var version *string
	var acceptedAt *time.Time
	err := q.QueryRow(ctx,
		`SELECT terms_version, terms_accepted_at FROM users WHERE uuid = $1`,
		userID,
	).Scan(&version, &acceptedAt)
	if err == pgx.ErrNoRows {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	v := ""
	if version != nil {
		v = *version
	}
	return v, acceptedAt, nil
}

// SetUserConsent records that userID accepted version at acceptedAt.
// Idempotent — re-accepting the same version refreshes the timestamp.
func (s *PostgresStore) SetUserConsent(
	ctx context.Context,
	userID, version string,
	acceptedAt time.Time,
) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx,
		`UPDATE users SET terms_version = $2, terms_accepted_at = $3
		 WHERE uuid = $1`,
		userID, version, acceptedAt)
	return err
}
