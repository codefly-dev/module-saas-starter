package infra

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"accounts/pkg/eventcatalog"
	"accounts/pkg/events"
	eventsv1 "accounts/pkg/gen/saas/events/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// eventsRelayQueue is the reserved queue name of the relay worker. Nothing else
// may claim it and no subscription may name it (enforced by the
// event_subscriptions queue CHECK). The relay's work source is the
// domain_events relation, not claimed jobs on this queue; the name identifies
// the workload and reserves the namespace.
const eventsRelayQueue = "events.relay"

const eventDeliveryMaxAttempts = 5

// maxClaimBatch bounds one Claim to the jobs platform's per-request limit, so a
// caller asking for more does not trip generated-command validation.
const maxClaimBatch = 100

// relayBatchSize bounds one relay transaction. The relay loops until it drains
// every unpublished event, one bounded batch per transaction so a large backlog
// never opens an unbounded transaction.
const relayBatchSize = 100

// relayMaxBatchesPerDrain bounds one RelayOnce call. Without it a single drain
// runs until the whole system-wide outbox is empty, so one tenant's large
// backlog holds the shared relay worker (and, on the inline publish path, the
// caller's request) for as long as it takes to clear. Capping the drain hands
// control back after a bounded amount of work; the worker's next tick — 250ms
// later — resumes where this one stopped, and because the scan is in global seq
// order no partition can monopolize the batches it does get.
const relayMaxBatchesPerDrain = 20

// eventRelayMaxAttempts is the relay's fan-out retry budget for one event, and
// is deliberately distinct from eventDeliveryMaxAttempts (which bounds the
// *delivered job's* own retries once fan-out has succeeded). A fan-out that
// fails is retried on later ticks, at most once per drain; on the attempt that
// reaches this cap the event is parked in dead_lettered_at, which unblocks its
// partition. Without a cap a deterministically-failing event is re-selected
// first on every tick forever and, because a failed ordered event holds its
// partition back, silently stalls that tenant's whole stream.
const eventRelayMaxAttempts = 5

// replayPageSize bounds one Replay page. Replay is an operator-triggered
// re-fan-out over durable history, so its result set is as large as the
// retained history matching the selector — unbounded for a wide selector such as
// a platform-scope replay of every tenant. Paging by seq keeps both the
// materialized slice and each transaction bounded, the same discipline the relay
// path already applies. It is a var only so a test can shrink it and exercise
// the multi-page walk without materializing a page's worth of history.
var replayPageSize = 500

// PostgresEventTransport is the reference events.Transport: it maps the
// CloudEvents envelope onto the durable jobs platform with no schema change.
// Publishing inserts one row into the durable domain_events relation — the
// event-of-record — inside the producer's transaction. Fan-out is separate: the
// relay reads unpublished events in seq order, resolves matching non-revoked
// event_subscriptions, and enqueues one ordinary inbox job per subscription, so
// claiming, acking, nacking, and heartbeating are the unchanged jobs lease
// lifecycle and replay re-fans-out from domain_events. seq is global publish
// order, so the scan is FIFO across partitions (no partition can starve another)
// while still yielding each partition's own events in order.
type PostgresEventTransport struct {
	store         *PostgresJobStore
	pool          *pgxpool.Pool
	workerID      string
	leaseDuration time.Duration
	webhooks      WebhookRelay
}

// WebhookRelay dispatches one event to one outbound endpoint on the relay's own
// transaction. It is a seam rather than a direct call so the transport keeps
// knowing only about envelopes and subscriptions, and a deployment with no
// webhook dispatcher wired simply relays nothing to endpoints.
type WebhookRelay interface {
	// Deliver reports whether an outbound delivery was created. False with a nil
	// error means there was nothing to deliver — the endpoint already has history
	// for this event, or its registration is gone.
	Deliver(ctx context.Context, tx pgx.Tx, e *eventsv1.EventEnvelope, subscription events.Subscription) (bool, error)
}

// WithWebhookRelay wires the outbound dispatcher into the relay, which is what
// makes a delivery = webhook subscription deliver.
func WithWebhookRelay(relay WebhookRelay) func(*PostgresEventTransport) {
	return func(p *PostgresEventTransport) { p.webhooks = relay }
}

// RequireWebhookRelay fails when no outbound dispatcher is wired. A process that
// serves webhook registrations must be able to deliver to them, and the relay is
// the only thing that does; without this, dropping WithWebhookRelay would surface
// far downstream as events that fan out to module queues and never to endpoints.
// Startup asserts it so the mis-wiring cannot reach a fan-out at all. Tests that
// publish no external type never need a dispatcher and so never call it.
func (p *PostgresEventTransport) RequireWebhookRelay() error {
	if p.webhooks == nil {
		return errors.New("events: no outbound webhook dispatcher is wired; webhook subscriptions would never be delivered")
	}
	return nil
}

func NewPostgresEventTransport(
	store *PostgresJobStore,
	pool *pgxpool.Pool,
	workerID string,
	leaseDuration time.Duration,
	opts ...func(*PostgresEventTransport),
) *PostgresEventTransport {
	transport := &PostgresEventTransport{
		store:         store,
		pool:          pool,
		workerID:      workerID,
		leaseDuration: leaseDuration,
	}
	for _, opt := range opts {
		opt(transport)
	}
	return transport
}

var _ events.Transport = (*PostgresEventTransport)(nil)

// Publish inserts the event-of-record into domain_events, keyed on the envelope
// id. With a caller transaction the insert joins it — the transactional-outbox
// rule — and the asynchronous relay worker fans out after commit. Without one
// (unit tests and simple callers) the insert commits on its own and the relay is
// drained inline, so a single-shot publish then claim observes the delivery.
func (p *PostgresEventTransport) Publish(ctx context.Context, tx events.TxHandle, e *eventsv1.EventEnvelope) error {
	if e.GetId() == "" || e.GetType() == "" || e.GetSource() == "" {
		return events.ErrInvalidEnvelope
	}
	fingerprint, err := eventFingerprint(e)
	if err != nil {
		return err
	}
	if pgtx, ok := tx.(pgx.Tx); ok {
		return p.insertDomainEvent(ctx, pgtx, e, fingerprint)
	}
	if err := pgx.BeginFunc(ctx, p.pool, func(pgtx pgx.Tx) error {
		return p.insertDomainEvent(ctx, pgtx, e, fingerprint)
	}); err != nil {
		return err
	}
	_, err = p.RelayOnce(ctx)
	return err
}

// eventFingerprint is the idempotency witness stored beside the event id: two
// publishes of one id must carry the same fact, and publish_domain_event rejects
// a re-publish whose fingerprint disagrees with the stored one. The digest must
// therefore cover the event's semantic fact only — not the per-attempt transport
// and trace metadata, which legitimately varies between two publishes of the
// same logical event (a retry mints a fresh event_time and W3C traceparent, and
// may re-thread correlation/causation ids). Hashing those would turn an ordinary
// retry into a spurious ErrIdempotencyConflict. We clone the envelope, clear
// exactly those volatile fields, and hash the rest with a deterministic proto
// encoding so the digest is stable across processes and languages. Every other
// field stays in the witness, so a genuine same-id redefinition of the fact is
// still caught.
func eventFingerprint(e *eventsv1.EventEnvelope) ([]byte, error) {
	core, ok := proto.Clone(e).(*eventsv1.EventEnvelope)
	if !ok {
		return nil, events.ErrInvalidEnvelope
	}
	core.Time = nil
	core.Traceparent = ""
	core.CorrelationId = ""
	core.CausationId = ""
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(core)
	if err != nil {
		return nil, events.ErrInvalidEnvelope
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

const publishDomainEventSQL = `
	SELECT event_id, stored_fingerprint, inserted
	FROM public.publish_domain_event(
		$1::uuid, $2, $3, $4, $5::timestamptz, $6, $7, $8, $9::bytea, $10::uuid,
		$11, $12, $13, $14, $15, $16, $17, $18::int, $19::bytea
	)`

func (p *PostgresEventTransport) insertDomainEvent(ctx context.Context, tx pgx.Tx, e *eventsv1.EventEnvelope, fingerprint []byte) error {
	var eventTime any
	if e.GetTime() != nil {
		eventTime = e.GetTime().AsTime().UTC()
	}
	var eventID string
	var stored []byte
	var inserted bool
	err := tx.QueryRow(ctx, publishDomainEventSQL,
		e.GetId(), e.GetType(), e.GetSource(), e.GetSubject(), eventTime,
		e.GetSpecversion(), e.GetDatacontenttype(), e.GetDataschema(), e.GetData(),
		nullableUUID(e.GetTenantId()), e.GetBoundaryId(), e.GetPartitionKey(),
		e.GetCorrelationId(), e.GetCausationId(), e.GetActorPrincipalId(),
		e.GetOwnerPrincipalId(), e.GetTraceparent(), int32(eventSchemaVersion(e)),
		fingerprint,
	).Scan(&eventID, &stored, &inserted)
	if err != nil {
		return fmt.Errorf("events: publish domain event: %w", err)
	}
	if !inserted && !bytes.Equal(stored, fingerprint) {
		return events.ErrIdempotencyConflict
	}
	return nil
}

// RelayOnce drains unpublished events, one bounded batch per transaction, and
// returns how many events it relayed. The asynchronous relay worker calls it on
// a tick; Publish calls it inline for the no-transaction path.
//
// The drain is bounded twice over. relayMaxBatchesPerDrain caps how much work
// one call does, so a large backlog is cleared across ticks instead of holding
// the worker (or an inline publisher) indefinitely. And every event whose
// fan-out fails is remembered for the rest of this drain and excluded from later
// batches: without that, the very next batch would re-select the failed event
// first — it is still unpublished and still has the lowest seq — and burn its
// whole retry budget inside one drain, parking an event that failed for a
// transient reason. One drain therefore costs an event at most one attempt.
func (p *PostgresEventTransport) RelayOnce(ctx context.Context) (int, error) {
	relayed := 0
	drain := &relayDrain{blocked: map[string]bool{}}
	for batch := 0; batch < relayMaxBatchesPerDrain; batch++ {
		failedBefore := len(drain.failed)
		processed, err := p.relayBatch(ctx, drain)
		if err != nil {
			return relayed, err
		}
		relayed += processed
		// No forward progress and nothing newly failed: the outbox is drained, or
		// everything left is blocked, parked, or held by another relay's SKIP
		// LOCKED. Either way another batch would do the same work again.
		if processed == 0 && len(drain.failed) == failedBefore {
			return relayed, nil
		}
	}
	return relayed, nil
}

// relayDrain is the state one RelayOnce accumulates across its batches. Both
// fields must outlive a single batch, and for opposite reasons.
//
// failed keeps an event that has already failed out of this drain's later
// batches. Without it the next batch re-selects that event immediately — it is
// still unpublished and still holds the lowest seq — and one drain spends the
// event's whole retry budget, parking something that failed for a transient
// reason.
//
// blocked keeps the partitions that failure held back. It has to be carried for
// exactly the same reason failed does: once the failed event is skipped by the
// scan, nothing in a later batch would otherwise recall that its partition is
// still waiting, and the very next event of that partition would be published
// ahead of the one that could not be delivered — silently breaking the ordering
// the skip list was introduced to leave undisturbed.
type relayDrain struct {
	failed  []string
	blocked map[string]bool
}

const relaySelectUnpublishedSQL = `
	SELECT id, type, source, subject, event_time, specversion, datacontenttype,
	       dataschema, data, tenant_id, boundary_id, partition_key, correlation_id,
	       causation_id, actor_principal_id, owner_principal_id, traceparent,
	       schema_version, seq
	FROM public.domain_events
	WHERE published_at IS NULL
	  AND dead_lettered_at IS NULL
	  AND NOT (id = ANY($2::uuid[]))
	ORDER BY seq
	LIMIT $1
	FOR UPDATE SKIP LOCKED`

const relayMarkPublishedSQL = `UPDATE public.domain_events SET published_at = NOW() WHERE id = $1::uuid`

// relayRecordFailureSQL charges one failed fan-out attempt to the event and
// parks it once the budget is spent, reporting whether this attempt was the one
// that parked it. It runs on the batch transaction rather than the rolled-back
// savepoint, so the count survives the failure that caused it.
const relayRecordFailureSQL = `
	UPDATE public.domain_events
	SET relay_attempts   = relay_attempts + 1,
	    last_relay_error = $2,
	    dead_lettered_at = CASE WHEN relay_attempts + 1 >= $3 THEN NOW() ELSE NULL END
	WHERE id = $1::uuid
	RETURNING dead_lettered_at IS NOT NULL`

// relayBatch locks up to one batch of unpublished, not-yet-parked events with
// SKIP LOCKED so concurrent relays never fan the same event out twice, then fans
// each event out to its matching non-revoked subscriptions and marks it
// published. It returns how many it relayed, and records the failures and the
// partitions they held back on the shared drain state.
//
// Each event's fan-out runs inside its own savepoint: a poison event (one whose
// enqueue keeps failing) is rolled back and left unpublished instead of dragging
// the whole batch — and every other event's deliveries — back with it. A failed
// attempt is then charged to the event on the batch transaction, and once the
// budget is spent the event is parked, which is what stops a permanently-bad row
// from stalling its partition for good.
//
// Holding back the rest of a partition is what keeps ordered delivery ordered,
// but it is owed only to events that actually have an ordered subscriber. An
// event that no ordered subscription matches — or that carries no partition key,
// which makes it unordered by construction — promises nothing about its position
// relative to its neighbours, so a failure on it must not hold the events behind
// it. Blocking those would be head-of-line blocking imposed on a delivery mode
// whose entire contract is that order does not matter.
func (p *PostgresEventTransport) relayBatch(ctx context.Context, drain *relayDrain) (int, error) {
	processed := 0
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, relaySelectUnpublishedSQL, relayBatchSize, skipList(drain.failed))
		if err != nil {
			return fmt.Errorf("events: select unpublished: %w", err)
		}
		pending, _, err := scanDomainEvents(rows)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		subscriptions, err := liveSubscriptions(ctx, tx, eventTypesOf(pending))
		if err != nil {
			return err
		}
		for _, e := range pending {
			partition := e.GetPartitionKey()
			// An earlier ordered event in this partition failed to fan out; holding
			// the rest of the partition keeps ordered delivery ordered — this event
			// waits for the tick that clears the poison ahead of it.
			if partition != "" && drain.blocked[partition] {
				continue
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return fmt.Errorf("events: open savepoint: %w", err)
			}
			if ferr := p.relayEvent(ctx, sp, e, subscriptions); ferr != nil {
				if rbErr := sp.Rollback(ctx); rbErr != nil {
					// The savepoint rollback itself failed: the outer transaction is no
					// longer usable, so abort the batch rather than press on blindly.
					return fmt.Errorf("events: rollback poison event %s: %w", e.GetId(), rbErr)
				}
				// Poison event isolated: its partial fan-out is undone and it stays
				// unpublished. Charge the attempt on the batch transaction, which
				// survives the savepoint rollback above.
				parked, rerr := p.recordRelayFailure(ctx, tx, e, ferr)
				if rerr != nil {
					return rerr
				}
				drain.failed = append(drain.failed, e.GetId())
				// A parked event is out of the relay's way for good, so nothing is
				// waiting behind it and its partition proceeds. An event still in the
				// retry budget holds its partition only if it owes ordered delivery.
				if !parked && owesOrderedDelivery(e, subscriptions) {
					drain.blocked[partition] = true
				}
				continue
			}
			if err := sp.Commit(ctx); err != nil {
				return fmt.Errorf("events: release savepoint for event %s: %w", e.GetId(), err)
			}
			processed++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return processed, nil
}

// recordRelayFailure charges one failed attempt to the event and reports whether
// that attempt exhausted the budget and parked it. A parked event keeps its row
// — Replay is how an operator re-delivers it once the cause is fixed — but it no
// longer appears in the relay's scan and no longer holds its partition.
func (p *PostgresEventTransport) recordRelayFailure(ctx context.Context, tx pgx.Tx, e *eventsv1.EventEnvelope, cause error) (bool, error) {
	var parked bool
	if err := tx.QueryRow(ctx, relayRecordFailureSQL,
		e.GetId(), nackMessage(cause), eventRelayMaxAttempts,
	).Scan(&parked); err != nil {
		return false, fmt.Errorf("events: record relay failure for event %s: %w", e.GetId(), err)
	}
	if parked {
		wool.Get(ctx).In("events.relay").Warn("domain event dead-lettered after exhausting relay attempts",
			wool.Field("event_id", e.GetId()),
			wool.Field("event_type", e.GetType()),
			wool.Field("partition_key", e.GetPartitionKey()),
			wool.Field("attempts", eventRelayMaxAttempts),
			wool.ErrField(cause))
	}
	return parked, nil
}

// owesOrderedDelivery reports whether this event has a live subscriber that was
// promised per-partition order, which is the only reason to hold the rest of its
// partition back when its fan-out fails. An event with no partition key is
// unordered by construction (eventOrdering yields no ordering key for it), and
// an internal-visibility event is never delivered at all, so neither owes
// anything to the events behind it.
func owesOrderedDelivery(e *eventsv1.EventEnvelope, subscriptions []events.Subscription) bool {
	if e.GetPartitionKey() == "" || eventcatalog.IsInternalPublished(e.GetType()) {
		return false
	}
	for _, subscription := range subscriptions {
		if subscription.Delivery != events.DeliveryOrdered {
			continue
		}
		if events.Matches(subscription.TypePattern, e.GetType()) {
			return true
		}
	}
	return false
}

// skipList adapts the drain's failed-event set to the uuid[] parameter of the
// relay scan. pgx encodes a nil slice as NULL, and `id = ANY(NULL)` is NULL
// rather than false, which would discard every row; an empty non-nil slice
// encodes as the empty array and matches nothing, which is what "skip nothing"
// must mean.
func skipList(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// relayEvent fans one event out to its matching subscriptions and marks it
// published, all on the given (savepoint) transaction so the caller can isolate
// a failure. Internal-visibility events are intra-platform and must never reach a
// subscriber: both subscription-creation paths refuse an internal type, but a
// wildcard subscription created before the type was registered (or before it
// became internal) can still match here — a TOCTOU the subscribe-time gate
// cannot close. Re-checking visibility at fan-out time marks such an event
// published with no delivery.
func (p *PostgresEventTransport) relayEvent(ctx context.Context, tx pgx.Tx, e *eventsv1.EventEnvelope, subscriptions []events.Subscription) error {
	if eventcatalog.IsInternalPublished(e.GetType()) {
		if _, err := tx.Exec(ctx, relayMarkPublishedSQL, e.GetId()); err != nil {
			return fmt.Errorf("events: mark published: %w", err)
		}
		return nil
	}
	skipped := 0
	for _, subscription := range subscriptions {
		if !events.Matches(subscription.TypePattern, e.GetType()) {
			continue
		}
		if subscription.Delivery == events.DeliveryWebhook {
			delivered, err := p.relayToWebhook(ctx, tx, e, subscription)
			if err != nil {
				return err
			}
			if !delivered && subscription.OrgID == e.GetTenantId() &&
				eventcatalog.IsExternalPublished(e.GetType()) {
				skipped++
			}
			continue
		}
		request := p.delivery(e, subscription, e.GetId()+":"+subscription.ID)
		if err := enqueueOne(ctx, tx, request); err != nil {
			return mapEnqueueError(err)
		}
	}
	// A replay re-fans out events endpoints already hold history for, and the
	// dedupe drops every one of them. Reporting the count is what keeps a replay
	// from looking like it re-delivered to endpoints when it delivered to none:
	// ReplayDelivery is the RPC that actually re-sends one.
	if skipped > 0 {
		wool.Get(ctx).In("events.relay").Info("webhook deliveries skipped as already present",
			wool.Field("event_id", e.GetId()),
			wool.Field("event_type", e.GetType()),
			wool.Field("skipped", skipped))
	}
	if _, err := tx.Exec(ctx, relayMarkPublishedSQL, e.GetId()); err != nil {
		return fmt.Errorf("events: mark published: %w", err)
	}
	return nil
}

// relayToWebhook dispatches one event to one endpoint registration. Two gates
// stand between a matching pattern and an outbound request. The type must be
// declared external, because visibility is what makes a fact eligible to leave
// the platform and a wildcard subscription can match a type registered later or
// reclassified since. The event's tenant must be the subscription's, because a
// pattern says nothing about ownership and the relay runs with RLS bypassed —
// this is the only thing standing between one tenant's event and another
// tenant's endpoint.
//
// Past those, the work is the dispatcher's existing contract: a pending
// webhook_deliveries row holding the exact bytes that will be signed, and the
// same OutboundWebhookJob the audit emitter used to enqueue, both on the relay's
// transaction so a failed fan-out leaves neither behind.
func (p *PostgresEventTransport) relayToWebhook(
	ctx context.Context,
	tx pgx.Tx,
	e *eventsv1.EventEnvelope,
	subscription events.Subscription,
) (bool, error) {
	if !eventcatalog.IsExternalPublished(e.GetType()) {
		return false, nil
	}
	if subscription.OrgID == "" || subscription.OrgID != e.GetTenantId() {
		return false, nil
	}
	// Only a subscription past both gates owes a delivery, so only here is a
	// missing dispatcher a misconfiguration rather than an empty result — before
	// the tenant gate it would also fire for another tenant's endpoint. Returning
	// nil would mark the event published and discard the delivery with nothing to
	// distinguish it from an event nobody subscribed to. Production cannot reach
	// it: work.go asserts the dispatcher at startup.
	if p.webhooks == nil {
		return false, errors.New("events: webhook subscription matched but no dispatcher is wired")
	}
	return p.webhooks.Deliver(ctx, tx, e, subscription)
}

func enqueueOne(ctx context.Context, tx pgx.Tx, request *jobsv1.EnqueueJobRequest) error {
	prepared, err := prepareJobEnqueue(request)
	if err != nil {
		return err
	}
	_, err = enqueuePreparedJob(ctx, tx, prepared)
	return err
}

func mapEnqueueError(err error) error {
	if errors.Is(err, jobs.ErrIdempotencyConflict) {
		return events.ErrIdempotencyConflict
	}
	return err
}

// A webhook subscription is live only while its endpoint registration is active,
// which is the same predicate the audit emitter's inline fan-out applied before
// webhooks moved onto subscriptions. Deactivating an endpoint therefore stops
// delivery without rewriting any subscription row.
//
// The scan is confined to the types actually being fanned out. It has to be:
// with outbound webhooks converged onto this relation it holds one row per
// (endpoint, subscribed event name) across every tenant, and this query runs
// once per relay batch. Loading all of it to match in Go would make each tick
// cost the whole table. An exact pattern is selected by equality; a wildcard
// pattern cannot be, so those rows are always loaded and matched in Go — they
// are only ever created by a runtime module Subscribe, and a webhook
// subscription is always an exact type.
const liveSubscriptionsSQL = `
	SELECT subscription.id, subscription.subscriber_principal_id, subscription.type_pattern,
	       subscription.queue, subscription.delivery, subscription.org_id,
	       subscription.webhook_subscription_id
	FROM public.event_subscriptions AS subscription
	LEFT JOIN public.webhook_subscriptions AS endpoint
	       ON endpoint.id = subscription.webhook_subscription_id
	WHERE subscription.revoked_at IS NULL
	  AND (subscription.webhook_subscription_id IS NULL OR endpoint.active)
	  AND (subscription.type_pattern = ANY($1::text[])
	       OR subscription.type_pattern LIKE '%.*')`

// eventTypesOf is the distinct type set of one fan-out batch, the selector that
// bounds the subscription scan above.
func eventTypesOf(events []*eventsv1.EventEnvelope) []string {
	seen := make(map[string]struct{}, len(events))
	types := make([]string, 0, len(events))
	for _, e := range events {
		if _, duplicate := seen[e.GetType()]; duplicate {
			continue
		}
		seen[e.GetType()] = struct{}{}
		types = append(types, e.GetType())
	}
	return types
}

func liveSubscriptions(ctx context.Context, tx pgx.Tx, types []string) ([]events.Subscription, error) {
	rows, err := tx.Query(ctx, liveSubscriptionsSQL, types)
	if err != nil {
		return nil, fmt.Errorf("events: load subscriptions: %w", err)
	}
	defer rows.Close()
	var subscriptions []events.Subscription
	for rows.Next() {
		var id, pattern, queue, delivery string
		var principal, orgID, webhookSubscriptionID *string
		if err := rows.Scan(&id, &principal, &pattern, &queue, &delivery, &orgID, &webhookSubscriptionID); err != nil {
			return nil, fmt.Errorf("events: scan subscription: %w", err)
		}
		subscriptions = append(subscriptions, events.Subscription{
			ID:                    id,
			SubscriberPrincipalID: derefString(principal),
			TypePattern:           pattern,
			Queue:                 queue,
			Delivery:              events.Delivery(delivery),
			OrgID:                 derefString(orgID),
			WebhookSubscriptionID: derefString(webhookSubscriptionID),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: read subscriptions: %w", err)
	}
	return subscriptions, nil
}

func (p *PostgresEventTransport) delivery(e *eventsv1.EventEnvelope, subscription events.Subscription, idempotencyKey string) *jobsv1.EnqueueJobRequest {
	var ordering *jobsv1.JobOrderingKey
	if subscription.Delivery == events.DeliveryOrdered {
		ordering = eventOrdering(e.GetPartitionKey())
	}
	return &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:          eventScope(e.GetTenantId()),
		Queue:          subscription.Queue,
		Topic:          e.GetType(),
		Source:         e.GetSource(),
		IdempotencyKey: idempotencyKey,
		Ordering:       ordering,
		SchemaVersion:  eventSchemaVersion(e),
		Payload:        e.GetData(),
		ContentType:    eventContentType(e),
		Attributes:     eventAttributes(e),
		MaxAttempts:    eventDeliveryMaxAttempts,
	}}
}

func (p *PostgresEventTransport) Claim(ctx context.Context, queue string, max int) ([]events.Leased, error) {
	if max < 1 {
		return nil, nil
	}
	if max > maxClaimBatch {
		max = maxClaimBatch
	}
	response, err := p.store.Claim(ctx, &jobsv1.ClaimJobsRequest{
		Queue:         queue,
		WorkerId:      p.workerID,
		Limit:         uint32(max),
		LeaseDuration: durationpb.New(p.leaseDuration),
	})
	if err != nil {
		return nil, err
	}
	leased := make([]events.Leased, 0, len(response.GetJobs()))
	for _, job := range response.GetJobs() {
		leased = append(leased, events.Leased{Token: leaseToken(job), Envelope: envelopeFromJob(job)})
	}
	return leased, nil
}

func (p *PostgresEventTransport) Heartbeat(ctx context.Context, token string) error {
	lease, ok := p.leaseReference(token)
	if !ok {
		return events.ErrLeaseLost
	}
	_, err := p.store.Heartbeat(ctx, &jobsv1.HeartbeatJobRequest{
		Lease:     lease,
		Extension: durationpb.New(p.leaseDuration),
	})
	return mapLeaseError(err)
}

func (p *PostgresEventTransport) Ack(ctx context.Context, token string) error {
	lease, ok := p.leaseReference(token)
	if !ok {
		return events.ErrLeaseLost
	}
	return mapLeaseError(p.store.Complete(ctx, &jobsv1.CompleteJobRequest{Lease: lease}))
}

func (p *PostgresEventTransport) Nack(ctx context.Context, token string, cause error, permanent bool) error {
	lease, ok := p.leaseReference(token)
	if !ok {
		return events.ErrLeaseLost
	}
	failure := &jobsv1.JobFailure{Code: "events_nack", Message: nackMessage(cause)}
	if permanent {
		return mapLeaseError(p.store.DeadLetter(ctx, &jobsv1.DeadLetterJobRequest{Lease: lease, Failure: failure}))
	}
	_, err := p.store.Retry(ctx, &jobsv1.RetryJobRequest{
		Lease:   lease,
		Failure: failure,
		RetryAt: timestamppb.New(time.Now()),
	})
	return mapLeaseError(err)
}

// replayDomainEventsSQL reads one page of replay history. The keyset cursor is
// seq, which is unique and ascending, so paging cannot skip or repeat a row the
// way an OFFSET over a concurrently-written table can. Ordering by seq alone
// (rather than by partition_key first) is what makes the cursor a single column,
// and it still yields any one partition's events in their own order, because seq
// is monotonic within a partition. Parked events are included on purpose: replay
// is how an operator re-delivers an event the relay had to dead-letter.
const replayDomainEventsSQL = `
	SELECT id, type, source, subject, event_time, specversion, datacontenttype,
	       dataschema, data, tenant_id, boundary_id, partition_key, correlation_id,
	       causation_id, actor_principal_id, owner_principal_id, traceparent,
	       schema_version, seq
	FROM public.domain_events
	WHERE ($1::text IS NULL OR type = $1)
	  AND ($2::uuid IS NULL OR tenant_id = $2)
	  AND ($3::timestamptz IS NULL OR created_at >= $3)
	  AND seq > $4
	ORDER BY seq
	LIMIT $5`

// Replay re-fans-out the durable events matching the selector so a consumer that
// attaches later receives history up to retention. Each redelivery carries a
// fresh nonce in its idempotency key so it is never deduped against the original
// delivery; the returned count is the number of events replayed, which is at
// least one even when no subscription matches (the event is still durable).
//
// History is walked one bounded page per transaction. A wide selector — a
// platform-scope replay, or a tenant with a long retained history — would
// otherwise materialize every matching row at once and hold a single transaction
// open for the whole fan-out. The cost is that a failure part-way leaves earlier
// pages already enqueued: acceptable, because replay is at-least-once
// re-delivery by construction (that is what the nonce is for) and re-running it
// is safe, whereas an unbounded transaction is not.
func (p *PostgresEventTransport) Replay(ctx context.Context, sel events.ReplaySelector) (int, error) {
	replayed := 0
	var cursor int64
	for {
		rows, err := p.pool.Query(ctx, replayDomainEventsSQL,
			nullableString(sel.Type),
			nullableUUID(sel.TenantID),
			nullableTime(sel.Since),
			cursor,
			replayPageSize,
		)
		if err != nil {
			return replayed, fmt.Errorf("events: query replay source: %w", err)
		}
		page, lastSeq, err := scanDomainEvents(rows)
		if err != nil {
			return replayed, err
		}
		if len(page) == 0 {
			return replayed, nil
		}
		if err := p.replayPage(ctx, sel, page); err != nil {
			return replayed, err
		}
		replayed += len(page)
		cursor = lastSeq
		if len(page) < replayPageSize {
			return replayed, nil
		}
	}
}

// replayPage fans one page of replay history out inside a single transaction.
func (p *PostgresEventTransport) replayPage(ctx context.Context, sel events.ReplaySelector, page []*eventsv1.EventEnvelope) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		subscriptions, err := liveSubscriptions(ctx, tx, eventTypesOf(page))
		if err != nil {
			return err
		}
		for _, e := range page {
			// An internal-visibility event is never delivered to a subscriber, on
			// replay no less than on the live path (see relayBatch): skip its
			// fan-out. It still counts toward the replayed total — the event is
			// durable, it simply has no eligible subscriber.
			if eventcatalog.IsInternalPublished(e.GetType()) {
				continue
			}
			nonce := ":replay:" + uuid.NewString()
			for _, subscription := range subscriptions {
				if sel.SubscriberPrincipalID != "" && subscription.SubscriberPrincipalID != sel.SubscriberPrincipalID {
					continue
				}
				if !events.Matches(subscription.TypePattern, e.GetType()) {
					continue
				}
				request := p.delivery(e, subscription, e.GetId()+":"+subscription.ID+nonce)
				if err := enqueueOne(ctx, tx, request); err != nil {
					return mapEnqueueError(err)
				}
			}
		}
		return nil
	})
}

// scanDomainEvents reconstructs the envelope from its stored columns for every
// row, closing the cursor before returning so the transaction is free for the
// follow-on writes of a relay or replay. It also returns the seq of the last row
// scanned, which is the keyset cursor Replay pages on; both callers order by
// seq ascending, so that is the highest seq in the result.
func scanDomainEvents(rows pgx.Rows) ([]*eventsv1.EventEnvelope, int64, error) {
	defer rows.Close()
	var out []*eventsv1.EventEnvelope
	var lastSeq int64
	for rows.Next() {
		var (
			id, eventType, source, subject                       string
			specversion, datacontenttype, dataschema             string
			boundaryID, partitionKey, correlationID, causationID string
			actorPrincipalID, ownerPrincipalID, traceparent      string
			data                                                 []byte
			tenantID                                             *string
			eventTime                                            *time.Time
			schemaVersion                                        int32
			seq                                                  int64
		)
		if err := rows.Scan(
			&id, &eventType, &source, &subject, &eventTime, &specversion,
			&datacontenttype, &dataschema, &data, &tenantID, &boundaryID,
			&partitionKey, &correlationID, &causationID, &actorPrincipalID,
			&ownerPrincipalID, &traceparent, &schemaVersion, &seq,
		); err != nil {
			return nil, 0, fmt.Errorf("events: scan domain event: %w", err)
		}
		lastSeq = seq
		e := &eventsv1.EventEnvelope{
			Id:               id,
			Type:             eventType,
			Source:           source,
			Subject:          subject,
			Specversion:      specversion,
			Datacontenttype:  datacontenttype,
			Dataschema:       dataschema,
			Data:             data,
			BoundaryId:       boundaryID,
			PartitionKey:     partitionKey,
			CorrelationId:    correlationID,
			CausationId:      causationID,
			ActorPrincipalId: actorPrincipalID,
			OwnerPrincipalId: ownerPrincipalID,
			Traceparent:      traceparent,
			SchemaVersion:    uint32(schemaVersion),
		}
		if tenantID != nil {
			e.TenantId = *tenantID
		}
		if eventTime != nil {
			e.Time = timestamppb.New(eventTime.UTC())
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("events: read domain events: %w", err)
	}
	return out, lastSeq, nil
}

// leaseToken carries the job id and its fencing token in the opaque lease token
// the caller holds, so finalizers reconstruct the full lease reference without
// per-transport state. A finalizer for an expired or superseded token names a
// fencing value the database no longer holds and is rejected as a lost lease.
func leaseToken(job *jobsv1.JobEnvelope) string {
	return job.GetId() + "|" + job.GetLease().GetToken()
}

func (p *PostgresEventTransport) leaseReference(token string) (*jobsv1.JobLeaseReference, bool) {
	jobID, fencingToken, ok := strings.Cut(token, "|")
	if !ok {
		return nil, false
	}
	return &jobsv1.JobLeaseReference{JobId: jobID, WorkerId: p.workerID, LeaseToken: fencingToken}, true
}

func mapLeaseError(err error) error {
	if errors.Is(err, jobs.ErrLeaseLost) {
		return events.ErrLeaseLost
	}
	return err
}

func nackMessage(cause error) string {
	if cause == nil {
		return "nack"
	}
	message := cause.Error()
	if len(message) <= 4096 {
		return message
	}
	truncated := message[:4096]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func eventScope(tenantID string) *jobsv1.JobScope {
	if tenantID == "" {
		return &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}}
	}
	return &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: tenantID}}
}

func eventOrdering(partitionKey string) *jobsv1.JobOrderingKey {
	if partitionKey == "" {
		return nil
	}
	return &jobsv1.JobOrderingKey{Namespace: "events", Components: []string{partitionKey}}
}

func eventSchemaVersion(e *eventsv1.EventEnvelope) uint32 {
	if e.GetSchemaVersion() == 0 {
		return 1
	}
	return e.GetSchemaVersion()
}

func eventContentType(e *eventsv1.EventEnvelope) string {
	if e.GetDatacontenttype() == "" {
		return "application/protobuf"
	}
	return e.GetDatacontenttype()
}

const (
	attrID               = "id"
	attrType             = "type"
	attrSource           = "source"
	attrSubject          = "subject"
	attrSpecversion      = "specversion"
	attrDatacontenttype  = "datacontenttype"
	attrDataschema       = "dataschema"
	attrTime             = "time"
	attrTenantID         = "tenant_id"
	attrBoundaryID       = "boundary_id"
	attrPartitionKey     = "partition_key"
	attrCorrelationID    = "correlation_id"
	attrCausationID      = "causation_id"
	attrActorPrincipalID = "actor_principal_id"
	attrOwnerPrincipalID = "owner_principal_id"
	attrTraceparent      = "traceparent"
	attrSchemaVersion    = "schema_version"
)

func eventAttributes(e *eventsv1.EventEnvelope) map[string]string {
	attributes := map[string]string{}
	putAttribute(attributes, attrID, e.GetId())
	putAttribute(attributes, attrType, e.GetType())
	putAttribute(attributes, attrSource, e.GetSource())
	putAttribute(attributes, attrSubject, e.GetSubject())
	putAttribute(attributes, attrSpecversion, e.GetSpecversion())
	putAttribute(attributes, attrDatacontenttype, e.GetDatacontenttype())
	putAttribute(attributes, attrDataschema, e.GetDataschema())
	putAttribute(attributes, attrTenantID, e.GetTenantId())
	putAttribute(attributes, attrBoundaryID, e.GetBoundaryId())
	putAttribute(attributes, attrPartitionKey, e.GetPartitionKey())
	putAttribute(attributes, attrCorrelationID, e.GetCorrelationId())
	putAttribute(attributes, attrCausationID, e.GetCausationId())
	putAttribute(attributes, attrActorPrincipalID, e.GetActorPrincipalId())
	putAttribute(attributes, attrOwnerPrincipalID, e.GetOwnerPrincipalId())
	putAttribute(attributes, attrTraceparent, e.GetTraceparent())
	if e.GetTime() != nil {
		attributes[attrTime] = e.GetTime().AsTime().UTC().Format(time.RFC3339Nano)
	}
	if e.GetSchemaVersion() != 0 {
		attributes[attrSchemaVersion] = strconv.FormatUint(uint64(e.GetSchemaVersion()), 10)
	}
	return attributes
}

func envelopeFromJob(job *jobsv1.JobEnvelope) *eventsv1.EventEnvelope {
	return envelopeFromAttributes(job.GetAttributes(), job.GetPayload())
}

func envelopeFromAttributes(attributes map[string]string, payload []byte) *eventsv1.EventEnvelope {
	e := &eventsv1.EventEnvelope{
		Id:               attributes[attrID],
		Type:             attributes[attrType],
		Source:           attributes[attrSource],
		Subject:          attributes[attrSubject],
		Specversion:      attributes[attrSpecversion],
		Datacontenttype:  attributes[attrDatacontenttype],
		Dataschema:       attributes[attrDataschema],
		Data:             payload,
		TenantId:         attributes[attrTenantID],
		BoundaryId:       attributes[attrBoundaryID],
		PartitionKey:     attributes[attrPartitionKey],
		CorrelationId:    attributes[attrCorrelationID],
		CausationId:      attributes[attrCausationID],
		ActorPrincipalId: attributes[attrActorPrincipalID],
		OwnerPrincipalId: attributes[attrOwnerPrincipalID],
		Traceparent:      attributes[attrTraceparent],
	}
	if raw, ok := attributes[attrTime]; ok {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			e.Time = timestamppb.New(parsed)
		}
	}
	if raw, ok := attributes[attrSchemaVersion]; ok {
		if parsed, err := strconv.ParseUint(raw, 10, 32); err == nil {
			e.SchemaVersion = uint32(parsed)
		}
	}
	return e
}

func putAttribute(attributes map[string]string, key, value string) {
	if value != "" {
		attributes[key] = value
	}
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// nullableUUID passes an empty id through as SQL NULL for a uuid parameter; a
// non-empty id is sent as text and cast to uuid by the query.
func nullableUUID(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
