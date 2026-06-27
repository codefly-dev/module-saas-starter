package infra

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

func (s *PostgresStore) CreateMagicLink(ctx context.Context, ml *business.MagicLink) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO magic_links (id, email, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)`,
		ml.ID, ml.Email, ml.TokenHash, ml.ExpiresAt)
	return err
}

func (s *PostgresStore) GetMagicLinkByTokenHash(ctx context.Context, tokenHash string) (*business.MagicLink, error) {
	q := s.getQueryExecutor(ctx)

	var ml business.MagicLink
	var usedAt *time.Time

	err := q.QueryRow(ctx, `
		SELECT id, email, token_hash, expires_at, used_at, created_at
		FROM magic_links
		WHERE token_hash = $1`, tokenHash).Scan(
		&ml.ID, &ml.Email, &ml.TokenHash, &ml.ExpiresAt, &usedAt, &ml.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	ml.UsedAt = usedAt
	return &ml, nil
}

func (s *PostgresStore) MarkMagicLinkUsed(ctx context.Context, id string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE magic_links SET used_at = NOW()
		WHERE id = $1 AND used_at IS NULL`, id)
	return err
}
