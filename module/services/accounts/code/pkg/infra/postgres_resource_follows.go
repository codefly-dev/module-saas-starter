package infra

import (
	"github.com/jackc/pgx/v5"

	"context"

	"accounts/pkg/business"
)

// CreateResourceFollow records one person's intent to follow one resource
// instance, converging on the live row when the caller already follows it. The
// partial unique index is the idempotency key, so following twice is a no-op and
// overlapping follows can never produce two deliveries for one change.
func (s *PostgresStore) CreateResourceFollow(ctx context.Context, follow *business.ResourceFollow) error {
	q := s.getQueryExecutor(ctx)
	err := q.QueryRow(ctx, `
		INSERT INTO public.resource_follows (id, org_id, user_id, resource_type, resource_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id, user_id, resource_type, resource_id) WHERE revoked_at IS NULL
		DO NOTHING
		RETURNING id`,
		follow.ID, follow.OrgID, follow.UserID, follow.ResourceType, follow.ResourceID,
	).Scan(&follow.ID)
	if err == nil {
		return nil
	}
	if err != pgx.ErrNoRows {
		return err
	}
	return q.QueryRow(ctx, `
		SELECT id FROM public.resource_follows
		WHERE org_id = $1 AND user_id = $2 AND resource_type = $3 AND resource_id = $4
		  AND revoked_at IS NULL`,
		follow.OrgID, follow.UserID, follow.ResourceType, follow.ResourceID,
	).Scan(&follow.ID)
}

// RevokeResourceFollow soft-revokes the caller's live follow. Revocation is a
// fact with a time because the delivery suppression rule is defined against it;
// a delete would lose that. Revoking a follow that is absent or already revoked
// is a no-op, so an unfollow never reports whether the follow existed.
func (s *PostgresStore) RevokeResourceFollow(ctx context.Context, orgID, userID, resourceType, resourceID string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE public.resource_follows SET revoked_at = NOW()
		WHERE org_id = $1 AND user_id = $2 AND resource_type = $3 AND resource_id = $4
		  AND revoked_at IS NULL`,
		orgID, userID, resourceType, resourceID)
	return err
}
