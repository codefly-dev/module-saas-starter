package infra

import (
	"context"
	"errors"

	"accounts/pkg/business"
	"accounts/pkg/events"
	eventsv1 "accounts/pkg/gen/saas/events/v1"

	"github.com/jackc/pgx/v5"
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
// transaction.
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
) error {
	delivery, body, err := business.NewDomainEventWebhookDelivery(
		e.GetId(),
		e.GetType(),
		subscription.WebhookSubscriptionID,
		e.GetTime().AsTime(),
		e.GetData(),
	)
	if err != nil {
		return err
	}
	request, err := business.NewOutboundWebhookJobRequest(subscription.OrgID, delivery, body)
	if err != nil {
		return err
	}
	//nolint:staticcheck // the string key is the shared ctx-tx contract WithOrgTx defines
	txCtx := context.WithValue(ctx, "tx", tx)
	if err := r.store.CreateWebhookDelivery(txCtx, delivery); err != nil {
		// A replay re-fans out an event this endpoint already has history for.
		// At-least-once means the endpoint was already told, so the duplicate is
		// dropped here rather than failing the whole fan-out.
		if errors.Is(err, business.ErrWebhookDeliveryExists) {
			return nil
		}
		return err
	}
	return enqueueOne(ctx, tx, request)
}
