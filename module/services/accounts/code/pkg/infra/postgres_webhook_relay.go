package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"
	"accounts/pkg/events"
	eventsv1 "accounts/pkg/gen/saas/events/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PostgresWebhookRelay turns one matching webhook subscription into one durable
// outbound delivery. It is the whole of what "webhooks are a subscriber kind"
// costs: everything below it — signing, secret rotation, the attempt schedule,
// delivery history, the SSRF-guarded sender — is the dispatcher that already
// existed, reached through the same job the audit emitter used to enqueue
// inline.
type PostgresWebhookRelay struct {
	store *PostgresStore
}

func NewPostgresWebhookRelay(store *PostgresStore) *PostgresWebhookRelay {
	return &PostgresWebhookRelay{store: store}
}

var _ WebhookRelay = (*PostgresWebhookRelay)(nil)

// Deliver writes the pending delivery and its dispatch job on the relay's
// transaction, reporting whether an outbound delivery was actually created.
//
// The delivery row must be written through the store rather than by raw SQL
// here, because the store owns the projection's column authority; the relay's
// pgx transaction is handed to it on the context under the same key WithOrgTx
// uses, so the two writes and the relay's "mark published" land in one
// transaction and a fan-out failure leaves no orphan history row.
//
// The event's data is already the delivery body's `data` member — the emitter
// published exactly the bytes the inline fan-out used to marshal — so the body
// an endpoint receives, and the signature over it, are unchanged.
func (r *PostgresWebhookRelay) Deliver(
	ctx context.Context,
	tx pgx.Tx,
	e *eventsv1.EventEnvelope,
	subscription events.Subscription,
) (bool, error) {
	delivery, body, err := business.NewDomainEventWebhookDelivery(
		e.GetId(),
		e.GetType(),
		subscription.WebhookSubscriptionID,
		e.GetTime().AsTime(),
		e.GetData(),
	)
	if err != nil {
		return false, err
	}
	request, err := business.NewOutboundWebhookJobRequest(subscription.OrgID, delivery, body)
	if err != nil {
		return false, err
	}
	// Both writes go inside a savepoint of their own. The endpoint can be deleted
	// between the scan that resolved this subscription and this insert, and the
	// resulting foreign-key violation aborts whatever (sub)transaction issued it
	// — so without a savepoint here, discarding that error in Go would leave the
	// event's savepoint poisoned and take every other subscriber of the same
	// event down with it.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("webhooks: open delivery savepoint: %w", err)
	}
	//nolint:staticcheck // the string key is the shared ctx-tx contract WithOrgTx defines
	spCtx := context.WithValue(ctx, "tx", sp)
	if err := r.deliverOn(spCtx, sp, delivery, request); err != nil {
		if rbErr := sp.Rollback(ctx); rbErr != nil {
			return false, fmt.Errorf("webhooks: roll back delivery: %w", rbErr)
		}
		// The endpoint was deleted while this event was being fanned out, or it
		// already has history for this event. Neither is a transport fault and
		// neither leaves anything to deliver, so the rest of the fan-out proceeds.
		if errors.Is(err, business.ErrWebhookDeliveryExists) || isForeignKeyViolation(err) {
			return false, nil
		}
		return false, err
	}
	return true, sp.Commit(ctx)
}

func (r *PostgresWebhookRelay) deliverOn(
	spCtx context.Context,
	sp pgx.Tx,
	delivery *business.WebhookDelivery,
	request *jobsv1.EnqueueJobRequest,
) error {
	if err := r.store.CreateWebhookDelivery(spCtx, delivery); err != nil {
		return err
	}
	return enqueueOne(spCtx, sp, request)
}

// isForeignKeyViolation reports whether err is SQLSTATE 23503, which on the
// delivery insert means the endpoint registration this subscription belonged to
// is already gone.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
