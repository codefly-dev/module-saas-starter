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

	"accounts/pkg/events"
	eventsv1 "accounts/pkg/gen/saas/events/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

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

// PostgresEventTransport is the reference events.Transport: it maps the
// CloudEvents envelope onto the durable jobs platform with no schema change.
// Publishing inserts one row into the durable domain_events relation — the
// event-of-record — inside the producer's transaction. Fan-out is separate: the
// relay reads unpublished events in (partition_key, seq) order, resolves matching
// non-revoked event_subscriptions, and enqueues one ordinary inbox job per
// subscription, so claiming, acking, nacking, and heartbeating are the unchanged
// jobs lease lifecycle and replay re-fans-out from domain_events.
type PostgresEventTransport struct {
	store         *PostgresJobStore
	pool          *pgxpool.Pool
	workerID      string
	leaseDuration time.Duration
}

func NewPostgresEventTransport(
	store *PostgresJobStore,
	pool *pgxpool.Pool,
	workerID string,
	leaseDuration time.Duration,
) *PostgresEventTransport {
	return &PostgresEventTransport{
		store:         store,
		pool:          pool,
		workerID:      workerID,
		leaseDuration: leaseDuration,
	}
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
// publishes of one id must carry the same fact. A deterministic proto encoding
// makes the digest stable across processes and languages.
func eventFingerprint(e *eventsv1.EventEnvelope) ([]byte, error) {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(e)
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

// RelayOnce drains every unpublished event, one bounded batch per transaction,
// and returns how many events it relayed. The asynchronous relay worker calls it
// on a tick; Publish calls it inline for the no-transaction path.
func (p *PostgresEventTransport) RelayOnce(ctx context.Context) (int, error) {
	relayed := 0
	for {
		batch, err := p.relayBatch(ctx)
		if err != nil {
			return relayed, err
		}
		relayed += batch
		if batch == 0 {
			return relayed, nil
		}
	}
}

const relaySelectUnpublishedSQL = `
	SELECT id, type, source, subject, event_time, specversion, datacontenttype,
	       dataschema, data, tenant_id, boundary_id, partition_key, correlation_id,
	       causation_id, actor_principal_id, owner_principal_id, traceparent,
	       schema_version
	FROM public.domain_events
	WHERE published_at IS NULL
	ORDER BY partition_key, seq
	LIMIT $1
	FOR UPDATE SKIP LOCKED`

const relayMarkPublishedSQL = `UPDATE public.domain_events SET published_at = NOW() WHERE id = $1::uuid`

// relayBatch locks up to one batch of unpublished events with SKIP LOCKED so
// concurrent relays never fan the same event out twice, enqueues one inbox
// delivery per matching non-revoked subscription, and marks each event
// published — all in one transaction, so a delivery failure rolls the whole
// batch back and the events stay unpublished for the next tick.
func (p *PostgresEventTransport) relayBatch(ctx context.Context) (int, error) {
	processed := 0
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, relaySelectUnpublishedSQL, relayBatchSize)
		if err != nil {
			return fmt.Errorf("events: select unpublished: %w", err)
		}
		pending, err := scanDomainEvents(rows)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		subscriptions, err := liveSubscriptions(ctx, tx)
		if err != nil {
			return err
		}
		for _, e := range pending {
			for _, subscription := range subscriptions {
				if !events.Matches(subscription.TypePattern, e.GetType()) {
					continue
				}
				request := p.delivery(e, subscription, e.GetId()+":"+subscription.ID)
				if err := enqueueOne(ctx, tx, request); err != nil {
					return mapEnqueueError(err)
				}
			}
			if _, err := tx.Exec(ctx, relayMarkPublishedSQL, e.GetId()); err != nil {
				return fmt.Errorf("events: mark published: %w", err)
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

const liveSubscriptionsSQL = `
	SELECT id, subscriber_principal_id, type_pattern, queue, delivery
	FROM public.event_subscriptions
	WHERE revoked_at IS NULL`

func liveSubscriptions(ctx context.Context, tx pgx.Tx) ([]events.Subscription, error) {
	rows, err := tx.Query(ctx, liveSubscriptionsSQL)
	if err != nil {
		return nil, fmt.Errorf("events: load subscriptions: %w", err)
	}
	defer rows.Close()
	var subscriptions []events.Subscription
	for rows.Next() {
		var id, principal, pattern, queue, delivery string
		if err := rows.Scan(&id, &principal, &pattern, &queue, &delivery); err != nil {
			return nil, fmt.Errorf("events: scan subscription: %w", err)
		}
		subscriptions = append(subscriptions, events.Subscription{
			ID:                    id,
			SubscriberPrincipalID: principal,
			TypePattern:           pattern,
			Queue:                 queue,
			Delivery:              events.Delivery(delivery),
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

const replayDomainEventsSQL = `
	SELECT id, type, source, subject, event_time, specversion, datacontenttype,
	       dataschema, data, tenant_id, boundary_id, partition_key, correlation_id,
	       causation_id, actor_principal_id, owner_principal_id, traceparent,
	       schema_version
	FROM public.domain_events
	WHERE ($1::text IS NULL OR type = $1)
	  AND ($2::uuid IS NULL OR tenant_id = $2)
	  AND ($3::timestamptz IS NULL OR created_at >= $3)
	ORDER BY partition_key, seq`

// Replay re-fans-out the durable events matching the selector so a consumer that
// attaches later receives history up to retention. Each redelivery carries a
// fresh nonce in its idempotency key so it is never deduped against the original
// delivery; the returned count is the number of events replayed, which is at
// least one even when no subscription matches (the event is still durable).
func (p *PostgresEventTransport) Replay(ctx context.Context, sel events.ReplaySelector) (int, error) {
	rows, err := p.pool.Query(ctx, replayDomainEventsSQL,
		nullableString(sel.Type),
		nullableUUID(sel.TenantID),
		nullableTime(sel.Since),
	)
	if err != nil {
		return 0, fmt.Errorf("events: query replay source: %w", err)
	}
	replay, err := scanDomainEvents(rows)
	if err != nil {
		return 0, err
	}
	if len(replay) == 0 {
		return 0, nil
	}

	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		subscriptions, err := liveSubscriptions(ctx, tx)
		if err != nil {
			return err
		}
		for _, e := range replay {
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
	if err != nil {
		return 0, err
	}
	return len(replay), nil
}

// scanDomainEvents reconstructs the envelope from its stored columns for every
// row, closing the cursor before returning so the transaction is free for the
// follow-on writes of a relay or replay.
func scanDomainEvents(rows pgx.Rows) ([]*eventsv1.EventEnvelope, error) {
	defer rows.Close()
	var out []*eventsv1.EventEnvelope
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
		)
		if err := rows.Scan(
			&id, &eventType, &source, &subject, &eventTime, &specversion,
			&datacontenttype, &dataschema, &data, &tenantID, &boundaryID,
			&partitionKey, &correlationID, &causationID, &actorPrincipalID,
			&ownerPrincipalID, &traceparent, &schemaVersion,
		); err != nil {
			return nil, fmt.Errorf("events: scan domain event: %w", err)
		}
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
		return nil, fmt.Errorf("events: read domain events: %w", err)
	}
	return out, nil
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
