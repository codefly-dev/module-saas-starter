package business

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/metadata"
)

// AuditEntry is the domain representation of an audit event. EventType is the
// registered discriminator (see audit_registry.go); Payload is the typed,
// per-type payload validated against that registration.
type AuditEntry struct {
	ID             string
	ActorID        string
	ActorType      string // "user", "api_key", "system", "agent"
	EventType      EventType
	SchemaVersion  int
	Resource       string
	ResourceID     string
	OrgID          string
	Payload        map[string]any
	IPAddress      string
	ImpersonatedBy string // admin user ID if this action was performed during impersonation
	IsImpersonated bool
	CreatedAt      time.Time
}

// AuditEmitter writes audit events. Production uses DurableAuditEmitter so the
// audit row and every matching webhook outbox row commit atomically.
type AuditEmitter interface {
	Emit(ctx context.Context, entry AuditEntry)
}

// DurableAuditEmitter has no process-local queue. A process crash before commit
// leaves neither the domain event nor partial fan-out; after commit, the leased
// delivery worker can resume on any replica.
type DurableAuditEmitter struct {
	store    Store
	producer jobs.Producer
}

func NewDurableAuditEmitter(store Store, producer jobs.Producer) (*DurableAuditEmitter, error) {
	if store == nil {
		return nil, errors.New("audit: store is required")
	}
	if producer == nil {
		return nil, errors.New("audit: transactional job producer is required")
	}
	return &DurableAuditEmitter{store: store, producer: producer}, nil
}

// normalize backfills id / timestamp / schema version and logs an advisory
// validation warning. A security event is never dropped because its payload
// drifted from the registered schema.
func (e *DurableAuditEmitter) normalize(ctx context.Context, entry *AuditEntry) {
	if entry.ID == "" {
		entry.ID = NewIDString()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	if def, ok := LookupAuditEvent(entry.EventType); ok && entry.SchemaVersion == 0 {
		entry.SchemaVersion = def.Version
	}
	if err := ValidatePayload(entry.EventType, entry.Payload); err != nil {
		wool.Get(ctx).In("DurableAuditEmitter.Emit").Warn(
			"audit payload failed registry validation",
			wool.Field("event_type", string(entry.EventType)),
			wool.ErrField(err),
		)
	}
}

// write inserts the audit row and fans out the webhook outbox using whatever
// transaction is already on ctx (getQueryExecutor / EnqueueJob both pick it up).
func (e *DurableAuditEmitter) write(ctx context.Context, entry AuditEntry) error {
	if err := e.store.InsertAuditEvent(ctx, entry); err != nil {
		return err
	}
	if entry.OrgID == "" {
		return nil
	}
	subscriptions, err := e.store.GetActiveWebhookSubscriptions(ctx, string(entry.EventType))
	if err != nil {
		return err
	}
	for _, subscription := range subscriptions {
		delivery, payload, err := newWebhookDelivery(entry, subscription.ID)
		if err != nil {
			return err
		}
		if err := createOutboundWebhookDelivery(
			ctx, e.store, e.producer, entry.OrgID, delivery, payload,
		); err != nil {
			return err
		}
	}
	return nil
}

func (e *DurableAuditEmitter) Emit(ctx context.Context, entry AuditEntry) {
	e.normalize(ctx, &entry)
	var err error
	if entry.OrgID == "" {
		err = e.store.WithControlPlane(ctx, func(ctx context.Context) error { return e.write(ctx, entry) })
	} else {
		err = e.store.WithOrgTx(ctx, entry.OrgID, func(ctx context.Context) error { return e.write(ctx, entry) })
	}
	if err != nil {
		wool.Get(ctx).In("DurableAuditEmitter.Emit").Error(
			"failed to commit audit event and webhook outbox",
			wool.Field("event_id", entry.ID),
			wool.Field("event_type", string(entry.EventType)),
			wool.Field("org_id", entry.OrgID),
			wool.ErrField(err),
		)
	}
}

// EmitTx writes the audit event and its webhook outbox using the caller's
// ambient transaction, so the audit trail commits atomically with the business
// mutation that triggered it. Unlike Emit (fire-and-forget, own tx), it returns
// the error so a failed audit write aborts the caller's tx — the compliance
// record and the action it records are all-or-nothing. It MUST be called inside
// an active tx (a Within/WithOrgTx block); there is no tx of its own.
func (e *DurableAuditEmitter) EmitTx(ctx context.Context, entry AuditEntry) error {
	e.normalize(ctx, &entry)
	return e.write(ctx, entry)
}

func (e *DurableAuditEmitter) Close() {}

// AuditQuery is the structured filter for the audit search path: per user, per
// tenant (org), per event type, per category, per resource, per time range,
// plus an optional JSONB payload-containment predicate. All fields are
// optional; the zero value matches every row visible under RLS.
type AuditQuery struct {
	OrgID           string
	ActorID         string
	EventType       string
	Category        string
	Resource        string
	ResourceID      string
	PayloadContains map[string]any
	From            *time.Time
	To              *time.Time
	PageSize        int32
	PageToken       string
}

// AuditAggregateBucket is one row of an aggregation result: a group key (an
// event type, category, actor id, or a time-bucket boundary in RFC3339) and
// the number of events in that group.
type AuditAggregateBucket struct {
	Key   string
	Count int64
}

// QueryAuditLog delegates to the store, scoping the read to the
// requested org under WithOrgTx so RLS lets the rows through. When
// OrgID is empty the caller is platform-admin (handler authz
// already enforced this in adapters/rpcs.go AuditServer.QueryAuditLog)
// and we use WithControlPlane to span tenants.
func (s *Service) QueryAuditLog(ctx context.Context, q AuditQuery) ([]AuditEntry, string, int32, error) {
	var entries []AuditEntry
	var nextToken string
	var total int32
	wrap := func(ctx context.Context) error {
		ev, nt, tot, err := s.store.QueryAuditLog(ctx, q)
		entries, nextToken, total = ev, nt, tot
		return err
	}
	var err error
	if q.OrgID == "" {
		err = s.store.WithControlPlane(ctx, wrap)
	} else {
		err = s.store.WithOrgTx(ctx, q.OrgID, wrap)
	}
	return entries, nextToken, total, err
}

// AggregateAuditLog computes grouped counts over audit events for analytics:
// group by event type, category, actor, or a time bucket (day/week/month),
// optionally filtered by the same predicates as QueryAuditLog.
func (s *Service) AggregateAuditLog(ctx context.Context, q AuditQuery, groupBy, bucket string) ([]AuditAggregateBucket, error) {
	var out []AuditAggregateBucket
	wrap := func(ctx context.Context) error {
		var err error
		out, err = s.store.AggregateAuditLog(ctx, q, groupBy, bucket)
		return err
	}
	var err error
	if q.OrgID == "" {
		err = s.store.WithControlPlane(ctx, wrap)
	} else {
		err = s.store.WithOrgTx(ctx, q.OrgID, wrap)
	}
	return out, err
}

// buildAuditEntry assembles an AuditEntry, detecting impersonation context from
// gRPC metadata headers injected by the auth sidecar (x-is-impersonated,
// x-impersonated-by).
func (s *Service) buildAuditEntry(ctx context.Context, actorID, actorType string, eventType EventType, resource, resourceID, orgID string, payload ...map[string]any) AuditEntry {
	entry := AuditEntry{
		ActorID:    actorID,
		ActorType:  actorType,
		EventType:  eventType,
		Resource:   resource,
		ResourceID: resourceID,
		OrgID:      orgID,
	}
	if len(payload) > 0 {
		entry.Payload = payload[0]
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("x-is-impersonated"); len(vals) > 0 && vals[0] == "true" {
			entry.IsImpersonated = true
			if by := md.Get("x-impersonated-by"); len(by) > 0 {
				entry.ImpersonatedBy = by[0]
			}
		}
	}
	return entry
}

// emit is the fire-and-forget audit path: the emitter owns its own transaction,
// so the event is written after (and independently of) the caller's mutation.
func (s *Service) emit(ctx context.Context, actorID, actorType string, eventType EventType, resource, resourceID, orgID string, payload ...map[string]any) {
	if s.audit == nil {
		return
	}
	s.audit.Emit(ctx, s.buildAuditEntry(ctx, actorID, actorType, eventType, resource, resourceID, orgID, payload...))
}

// emitTx records an audit event using the caller's ambient transaction, so the
// audit trail commits atomically with the business mutation. It MUST be called
// inside a Within/WithOrgTx block, and its error MUST be propagated: a failed
// audit write then aborts the mutation (fail-closed — no state change without
// its compliance record). Emitters that don't support transactional writes
// (test fakes) fall back to a best-effort non-transactional emit.
func (s *Service) emitTx(ctx context.Context, actorID, actorType string, eventType EventType, resource, resourceID, orgID string, payload ...map[string]any) error {
	if s.audit == nil {
		return nil
	}
	entry := s.buildAuditEntry(ctx, actorID, actorType, eventType, resource, resourceID, orgID, payload...)
	if txEmitter, ok := s.audit.(interface {
		EmitTx(context.Context, AuditEntry) error
	}); ok {
		return txEmitter.EmitTx(ctx, entry)
	}
	s.audit.Emit(ctx, entry)
	return nil
}
