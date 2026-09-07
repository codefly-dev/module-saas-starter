package infra

import (
	"context"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// event_subscriptions is a control-plane-owned platform relation (no RLS): only
// app_control_plane may write it and only app_job_worker/app_control_plane may
// read it. These three methods therefore assume the caller has already opened a
// WithControlPlane transaction — getQueryExecutor picks that tx up from ctx.

const eventSubscriptionColumns = `id, subscriber_principal_id, type_pattern, queue, delivery,
	COALESCE(created_by::text, ''), created_at`

func scanEventSubscription(row pgx.Row) (*business.EventSubscription, error) {
	var sub business.EventSubscription
	if err := row.Scan(
		&sub.ID, &sub.SubscriberPrincipalID, &sub.TypePattern, &sub.Queue,
		&sub.Delivery, &sub.CreatedBy, &sub.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &sub, nil
}

// CreateEventSubscription inserts a subscription and is idempotent on the active
// unique index (subscriber_principal_id, type_pattern, queue) WHERE revoked_at
// IS NULL: a second subscribe of the same shape returns the existing live row
// with inserted=false rather than a duplicate. An empty Delivery defaults to
// 'unordered' and an empty CreatedBy stores NULL.
func (s *PostgresStore) CreateEventSubscription(ctx context.Context, sub *business.EventSubscription) (*business.EventSubscription, bool, error) {
	q := s.getQueryExecutor(ctx)
	out, err := scanEventSubscription(q.QueryRow(ctx, `
		INSERT INTO public.event_subscriptions
			(subscriber_principal_id, type_pattern, queue, delivery, created_by)
		VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'unordered'), NULLIF($5, '')::uuid)
		ON CONFLICT (subscriber_principal_id, type_pattern, queue) WHERE revoked_at IS NULL
		DO NOTHING
		RETURNING `+eventSubscriptionColumns,
		sub.SubscriberPrincipalID, sub.TypePattern, sub.Queue, sub.Delivery, sub.CreatedBy,
	))
	if err == nil {
		return out, true, nil
	}
	if err != pgx.ErrNoRows {
		return nil, false, err
	}

	// The conflicting active row already carries this shape; return it unchanged.
	out, err = scanEventSubscription(q.QueryRow(ctx, `
		SELECT `+eventSubscriptionColumns+`
		FROM public.event_subscriptions
		WHERE subscriber_principal_id = $1 AND type_pattern = $2 AND queue = $3
		  AND revoked_at IS NULL`,
		sub.SubscriberPrincipalID, sub.TypePattern, sub.Queue,
	))
	if err != nil {
		return nil, false, err
	}
	return out, false, nil
}

// RevokeEventSubscription marks a live subscription revoked, scoped by
// subscriber_principal_id so a principal can only revoke its own. The bool
// reports whether a live row was actually revoked (false = not found, already
// revoked, or owned by another principal).
func (s *PostgresStore) RevokeEventSubscription(ctx context.Context, subscriptionID, subscriberPrincipalID string) (bool, error) {
	q := s.getQueryExecutor(ctx)
	tag, err := q.Exec(ctx, `
		UPDATE public.event_subscriptions
		SET revoked_at = NOW()
		WHERE id = $1 AND subscriber_principal_id = $2 AND revoked_at IS NULL`,
		subscriptionID, subscriberPrincipalID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListEventSubscriptions returns the principal's live (non-revoked) rows,
// oldest first.
func (s *PostgresStore) ListEventSubscriptions(ctx context.Context, subscriberPrincipalID string) ([]*business.EventSubscription, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT `+eventSubscriptionColumns+`
		FROM public.event_subscriptions
		WHERE subscriber_principal_id = $1 AND revoked_at IS NULL
		ORDER BY created_at`,
		subscriberPrincipalID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*business.EventSubscription
	for rows.Next() {
		sub, err := scanEventSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}
