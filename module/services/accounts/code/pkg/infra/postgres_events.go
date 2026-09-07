package infra

import (
	"context"
	"encoding/json"
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
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// eventsRelayQueue holds the durable, replayable event-of-record row for every
// published envelope. No consumer claims it in P1; the relay worker that drains
// it into per-subscription deliveries is P2. Publishing writes this row even
// with zero subscribers, so a zero-subscriber event stays durable and
// replayable.
const eventsRelayQueue = "events.relay"

const eventDeliveryMaxAttempts = 5

// maxClaimBatch bounds one Claim to the jobs platform's per-request limit, so a
// caller asking for more does not trip generated-command validation.
const maxClaimBatch = 100

// PostgresEventTransport is the reference events.Transport: it maps the
// CloudEvents envelope onto the durable jobs platform with no schema change.
// Publishing writes the event-of-record and one inbox delivery per matching
// subscription; claiming, acking, nacking, and heartbeating are the jobs lease
// lifecycle; replay re-fans-out from the event-of-record rows.
type PostgresEventTransport struct {
	store         *PostgresJobStore
	pool          *pgxpool.Pool
	subscriptions []events.Subscription
	workerID      string
	leaseDuration time.Duration
}

func NewPostgresEventTransport(
	store *PostgresJobStore,
	pool *pgxpool.Pool,
	subscriptions []events.Subscription,
	workerID string,
	leaseDuration time.Duration,
) *PostgresEventTransport {
	return &PostgresEventTransport{
		store:         store,
		pool:          pool,
		subscriptions: subscriptions,
		workerID:      workerID,
		leaseDuration: leaseDuration,
	}
}

var _ events.Transport = (*PostgresEventTransport)(nil)

func (p *PostgresEventTransport) Publish(ctx context.Context, tx events.TxHandle, e *eventsv1.EventEnvelope) error {
	if e.GetId() == "" || e.GetType() == "" || e.GetSource() == "" {
		return events.ErrInvalidEnvelope
	}
	requests := []*jobsv1.EnqueueJobRequest{p.eventOfRecord(e)}
	for _, subscription := range p.subscriptions {
		if !events.Matches(subscription.TypePattern, e.GetType()) {
			continue
		}
		requests = append(requests, p.delivery(e, subscription, e.GetId()+":"+subscription.ID))
	}
	return p.enqueueAll(ctx, tx, requests)
}

// enqueueAll commits the event-of-record and every delivery as one unit: within
// the caller's transaction when one is supplied, otherwise in a single
// transaction of its own. A mid-fan-out failure therefore leaves no partially
// published event behind.
func (p *PostgresEventTransport) enqueueAll(ctx context.Context, tx events.TxHandle, requests []*jobsv1.EnqueueJobRequest) error {
	if pgtx, ok := tx.(pgx.Tx); ok {
		for _, request := range requests {
			if err := enqueueOne(ctx, pgtx, request); err != nil {
				return mapEnqueueError(err)
			}
		}
		return nil
	}
	err := pgx.BeginFunc(ctx, p.pool, func(pgtx pgx.Tx) error {
		for _, request := range requests {
			if err := enqueueOne(ctx, pgtx, request); err != nil {
				return err
			}
		}
		return nil
	})
	return mapEnqueueError(err)
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

func (p *PostgresEventTransport) eventOfRecord(e *eventsv1.EventEnvelope) *jobsv1.EnqueueJobRequest {
	return &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:          eventScope(e.GetTenantId()),
		Queue:          eventsRelayQueue,
		Topic:          e.GetType(),
		Source:         e.GetSource(),
		IdempotencyKey: "event:" + e.GetId(),
		Ordering:       eventOrdering(e.GetPartitionKey()),
		SchemaVersion:  eventSchemaVersion(e),
		Payload:        e.GetData(),
		ContentType:    eventContentType(e),
		Attributes:     eventAttributes(e),
		MaxAttempts:    eventDeliveryMaxAttempts,
	}}
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

func (p *PostgresEventTransport) Replay(ctx context.Context, sel events.ReplaySelector) (int, error) {
	rows, err := p.pool.Query(ctx, replayEventsSQL,
		eventsRelayQueue,
		nullableString(sel.Type),
		nullableString(sel.TenantID),
		nullableTime(sel.Since),
	)
	if err != nil {
		return 0, fmt.Errorf("events: query replay source: %w", err)
	}
	defer rows.Close()

	var replay []*eventsv1.EventEnvelope
	for rows.Next() {
		var payload, attributesJSON []byte
		if err := rows.Scan(&payload, &attributesJSON); err != nil {
			return 0, fmt.Errorf("events: scan replay source: %w", err)
		}
		attributes := map[string]string{}
		if err := json.Unmarshal(attributesJSON, &attributes); err != nil {
			return 0, fmt.Errorf("events: decode replay attributes: %w", err)
		}
		replay = append(replay, envelopeFromAttributes(attributes, payload))
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("events: read replay source: %w", err)
	}

	var redeliveries []*jobsv1.EnqueueJobRequest
	for _, e := range replay {
		nonce := ":replay:" + uuid.NewString()
		for _, subscription := range p.subscriptions {
			if !events.Matches(subscription.TypePattern, e.GetType()) {
				continue
			}
			redeliveries = append(redeliveries, p.delivery(e, subscription, e.GetId()+":"+subscription.ID+nonce))
		}
	}
	if err := p.enqueueAll(ctx, nil, redeliveries); err != nil {
		return 0, err
	}
	return len(replay), nil
}

const replayEventsSQL = `
	SELECT payload, attributes::text
	FROM job_messages
	WHERE queue = $1
	  AND direction = 'outbox'
	  AND ($2::text IS NULL OR topic = $2)
	  AND ($3::text IS NULL OR (scope_kind = 'tenant' AND organization_id::text = $3))
	  AND ($4::timestamptz IS NULL OR created_at >= $4)
	ORDER BY ordering_key NULLS FIRST, created_at, id`

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

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
