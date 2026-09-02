package infra

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/business"
)

// =====================================================================
// work_context_replay — Postgres adapter (issue #420)
// =====================================================================
//
// Implements business.WorkContextReplayStore. A SINGLE_USE Work Context is
// redeemable exactly once: the consumer claims the token's nonce here after
// verifying the capability, and the (org_id, context_id) primary key admits one
// claim. Retention reclaims markers of expired capabilities under the control
// plane.

func (s *PostgresStore) ConsumeSingleUseWorkContext(
	ctx context.Context,
	orgID string,
	contextID string,
	expiresAt time.Time,
) error {
	if orgID == "" || contextID == "" {
		return errors.New("work context replay requires org id and context id")
	}
	return s.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		tag, err := s.getQueryExecutor(ctx).Exec(ctx, `
			INSERT INTO work_context_replay (org_id, context_id, expires_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (org_id, context_id) DO NOTHING`,
			orgID, contextID, expiresAt.UTC(),
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return business.ErrWorkContextAlreadyConsumed
		}
		return nil
	})
}

func (s *PostgresStore) PurgeExpiredWorkContextReplays(
	ctx context.Context,
	now time.Time,
) (int64, error) {
	tag, err := s.getQueryExecutor(ctx).Exec(ctx,
		`DELETE FROM work_context_replay WHERE expires_at < $1`, now.UTC())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

var _ business.WorkContextReplayStore = (*PostgresStore)(nil)
