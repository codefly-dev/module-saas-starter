package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/events"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/jobs"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	// ClientID names the registered client the call was made through. Empty
	// means the host's own web session, so "who did this" and "what did they do
	// it through" stay separate questions.
	ClientID  string
	CreatedAt time.Time
	// IdempotencyKey, when set, deduplicates retried emits: the emitter reserves
	// (OrgID, EventType, IdempotencyKey) in a guard table inside the same
	// transaction as the audit write, and a duplicate emit is a no-op success (no
	// second row, no second webhook fan-out). Empty disables dedup. See
	// audit_event_idempotency (migration 118).
	IdempotencyKey string
}

// Audit actor types. They are the values audit_events.actor_type admits, and
// name what kind of credential the mutation was made with.
const (
	ActorTypeUser   = "user"
	ActorTypeAPIKey = "api_key"
	ActorTypeSystem = "system"
	ActorTypeAgent  = "agent"
)

// AuditActor is the verified initiator of a privileged mutation. A transport
// adapter resolves it from the authenticated request context after it has
// authorized the call, never from fields the caller supplies in the request
// body. ActorTypeSystem belongs to genuinely automated work: a caller with no
// human behind it.
type AuditActor struct {
	ID   string
	Type string
	// DelegationChain names every party that acted on the actor's behalf
	// (RFC 8693 `act`), immediate delegate first, when the request arrived
	// through a delegation chain. It answers a different question than
	// impersonation: who is acting *for* this actor, not which subject the
	// actor is acting *as*.
	//
	// It is the whole chain, not its head: a multi-hop call is recorded by the
	// only party that ever sees the chain, so an intermediary dropped here can
	// never be recovered from the trail afterwards.
	DelegationChain []string
}

func (a AuditActor) validate() error {
	if a.ID == "" {
		return errors.New("audit actor id is required")
	}
	// audit_events.actor_id is a UUID column, and the insert path maps a
	// non-UUID id to NULL (see nilIfNotUUID). Rejecting it here is what makes
	// this guard fail closed: without the check, an id this validator accepts
	// still commits as an unattributed row — the exact outcome it exists to
	// prevent — and does it silently.
	if _, err := uuid.Parse(a.ID); err != nil {
		return fmt.Errorf("audit actor id %q is not a uuid, and would be stored as no actor at all: %w", a.ID, err)
	}
	switch a.Type {
	case ActorTypeUser, ActorTypeAPIKey, ActorTypeSystem, ActorTypeAgent:
		return nil
	default:
		return fmt.Errorf("audit actor type %q is not one of %q, %q, %q, %q",
			a.Type, ActorTypeUser, ActorTypeAPIKey, ActorTypeSystem, ActorTypeAgent)
	}
}

// provenance is the payload the actor contributes to every event it initiates.
// A direct call contributes nothing, so the stored payload stays empty rather
// than carrying an empty delegation.
func (a AuditActor) provenance() map[string]any {
	if len(a.DelegationChain) == 0 {
		return nil
	}
	return map[string]any{"delegated_by": a.DelegationChain}
}

// AuditEmitter writes audit events on a transaction it opens itself. It is the
// path for observations a domain transaction does not own — authentication
// outcomes, denials, reads, outcomes produced by an external provider — which
// must survive a rolled-back domain write.
type AuditEmitter interface {
	Emit(ctx context.Context, entry AuditEntry)
}

// TxAuditEmitter additionally writes on the caller's ambient transaction and
// returns the error, so a security mutation and its record commit or roll back
// together. Every event registered DurabilityTransactional travels this path;
// production construction rejects an emitter that does not implement it (see
// Service.VerifyAuditWiring), and test doubles must implement it rather than
// letting a security write fall back to best effort.
type TxAuditEmitter interface {
	AuditEmitter
	EmitTx(ctx context.Context, entry AuditEntry) error
}

// DurableAuditEmitter has no process-local queue. A process crash before commit
// leaves neither the domain event nor partial fan-out; after commit, the leased
// delivery worker can resume on any replica.
type DurableAuditEmitter struct {
	store       Store
	producer    jobs.Producer
	transport   events.Transport
	teeExternal bool
	queued      bool
}

// DurableAuditEmitterOption tunes the emitter at construction.
type DurableAuditEmitterOption func(*DurableAuditEmitter)

// WithExternalTee enqueues an audit-export job in the same transaction as each
// org-scoped audit row, feeding the external sink asynchronously from the
// durable outbox. Postgres stays the atomic source of truth; the tee never runs
// on the synchronous mutation path (see AuditExportQueue in audit_jobs.go).
func WithExternalTee() DurableAuditEmitterOption {
	return func(e *DurableAuditEmitter) { e.teeExternal = true }
}

// WithQueuedRecords writes each event's record into the transactional queue
// (audit_event_queue) instead of audit_events. It is the emitter under a swap
// value (ADR 0009): the queue row commits on the same transaction the
// audit_events row would have, beside the same domain event, and AuditRelay
// delivers it to the store of record afterwards. audit_events receives nothing.
func WithQueuedRecords() DurableAuditEmitterOption {
	return func(e *DurableAuditEmitter) { e.queued = true }
}

// WithDomainEventTransport publishes an external-visibility domain event beside
// each org-scoped audit row, in the same transaction. That event is what an
// outbound webhook subscription is fanned out from; without a transport wired the
// emitter still records audit, and nothing is delivered.
func WithDomainEventTransport(transport events.Transport) DurableAuditEmitterOption {
	return func(e *DurableAuditEmitter) { e.transport = transport }
}

func NewDurableAuditEmitter(store Store, producer jobs.Producer, opts ...DurableAuditEmitterOption) (*DurableAuditEmitter, error) {
	if store == nil {
		return nil, errors.New("audit: store is required")
	}
	if producer == nil {
		return nil, errors.New("audit: transactional job producer is required")
	}
	emitter := &DurableAuditEmitter{store: store, producer: producer}
	for _, opt := range opts {
		opt(emitter)
	}
	if emitter.teeExternal && emitter.queued {
		// The tee copies audit_events rows; under a swap there are none to copy.
		return nil, errors.New("audit: the external tee and the queued store of record are exclusive")
	}
	return emitter, nil
}

// normalize backfills id / timestamp. The schema version and the advisory
// payload check need the registry, which for a solution-declared type is a
// read, so write does them on the transaction it already holds.
func (e *DurableAuditEmitter) normalize(entry *AuditEntry) {
	if entry.ID == "" {
		entry.ID = NewIDString()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
}

// resolve reads what the registry says about the entry's type, backfills its
// schema version and logs an advisory validation warning. A security event is
// never dropped because its payload drifted from the registered schema.
func (e *DurableAuditEmitter) resolve(ctx context.Context, entry *AuditEntry) (ResolvedAuditEvent, error) {
	resolved, err := NewAuditEventResolver(e.store).Resolve(ctx, entry.EventType)
	if err != nil {
		return ResolvedAuditEvent{}, err
	}
	if resolved.Registered && entry.SchemaVersion == 0 {
		entry.SchemaVersion = resolved.Definition.Version
	}
	if err := resolved.Validate(entry.EventType, entry.Payload); err != nil {
		wool.Get(ctx).In("DurableAuditEmitter.Emit").Warn(
			"audit payload failed registry validation",
			wool.Field("event_type", string(entry.EventType)),
			wool.ErrField(err),
		)
	}
	return resolved, nil
}

// write inserts the audit row and publishes its domain event using whatever
// transaction is already on ctx (getQueryExecutor / Publish both pick it up).
func (e *DurableAuditEmitter) write(ctx context.Context, entry AuditEntry) error {
	resolved, err := e.resolve(ctx, &entry)
	if err != nil {
		return err
	}
	written, err := e.record(ctx, entry)
	if err != nil || !written {
		return err
	}
	if entry.OrgID == "" {
		return nil
	}
	// The tenant predicate is the event's own org, passed explicitly: the audit
	// write runs inside its mutation's transaction, and that transaction is the
	// control plane for platform-admin and other privileged writes, where RLS
	// would not scope this read at all.
	if err := e.publishDomainEvent(ctx, entry, resolved); err != nil {
		return err
	}
	if e.teeExternal {
		if err := enqueueAuditExport(ctx, e.producer, entry, resolved); err != nil {
			return err
		}
	}
	return nil
}

// record writes the event's record — the audit_events row, or under a swap
// value the queue row — on the ambient transaction, after reserving its
// idempotency key. written is false for a duplicate emit, which writes nothing.
func (e *DurableAuditEmitter) record(ctx context.Context, entry AuditEntry) (bool, error) {
	if entry.IdempotencyKey != "" {
		reserved, err := e.store.ReserveAuditIdempotency(ctx, entry.OrgID, string(entry.EventType), entry.IdempotencyKey)
		if err != nil {
			return false, err
		}
		if !reserved {
			// A prior emit of this (org, event_type, idempotency_key) already wrote
			// the event in a committed transaction. Skip the insert and the webhook
			// fan-out and report success: the intended effect (exactly one audit row
			// and one set of deliveries) is already in place. Reserving inside the
			// caller's tx keeps the guard row and the audit row atomic, so a rolled
			// back audit write also frees the key for a genuine retry.
			return false, nil
		}
	}
	if e.queued {
		return true, e.store.EnqueueAuditEvent(ctx, entry)
	}
	return true, e.store.InsertAuditEvent(ctx, entry)
}

func (e *DurableAuditEmitter) Emit(ctx context.Context, entry AuditEntry) {
	e.normalize(&entry)
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
	e.normalize(&entry)
	return e.write(ctx, entry)
}

// RecordTx writes only the event's record on the caller's transaction — no
// domain event, no tee job (AuditRecorder). Like EmitTx it MUST run inside an
// active transaction and its error MUST be propagated.
func (e *DurableAuditEmitter) RecordTx(ctx context.Context, entry AuditEntry) error {
	e.normalize(&entry)
	if _, err := e.resolve(ctx, &entry); err != nil {
		return err
	}
	_, err := e.record(ctx, entry)
	return err
}

func (e *DurableAuditEmitter) Close() {}

// AuditQuery is the structured filter for the audit search path: per user, per
// tenant (org), per event type, per category, per resource, per time range,
// plus an optional JSONB payload-containment predicate. All fields are
// optional; the zero value matches every row visible under RLS.
type AuditQuery struct {
	OrgID     string
	ActorID   string
	EventType string
	// EventTypes is the set form of EventType: a record matches when its type is
	// any one of them. Set alongside EventType both apply, so a one-element set
	// and the scalar answer alike.
	//
	// A summary over a family of event types had nothing to filter on before
	// this. An empty slice is no predicate at all — it is "the caller named no
	// set", not "match nothing" — because the only producer of an empty set is a
	// caller that did not send the field.
	EventTypes      []string
	Category        string
	Namespace       string
	Resource        string
	ResourceID      string
	ClientID        string
	CollectionID    string
	PayloadContains map[string]any
	From            *time.Time
	To              *time.Time
	PageSize        int32
	PageToken       string
}

// AuditMetric is one aggregation computed per group. Op ∈ {count,
// count_distinct, sum, avg, min, max, percentile}. Field names a payload key as
// "payload:<key>" (or, for count_distinct, a bare column: actor_id, event_type,
// category, resource, resource_id). Percentile is used only for op percentile.
type AuditMetric struct {
	Op         string
	Field      string
	Percentile float64
	Alias      string
}

// AuditDerivedMetric is a per-group ratio of two metrics referenced by alias.
type AuditDerivedMetric struct {
	Alias       string
	Numerator   string
	Denominator string
}

// AuditAggregationSpec describes an aggregation: the group dimensions, the time
// grain for a "time" dimension, the metrics to compute, and any derived ratios.
// GroupBy entries ∈ {event_type, category, actor, time, payload:<key>}. When
// GroupBy is empty the aggregation groups by event_type; when Metrics is empty a
// single COUNT(*) is returned.
type AuditAggregationSpec struct {
	GroupBy []string
	Bucket  string
	Metrics []AuditMetric
	Derived []AuditDerivedMetric
}

// ResolvedAlias returns the response-map key for a metric: its explicit alias,
// else "count" for a count, else "<op>_<key>".
func (m AuditMetric) ResolvedAlias() string {
	if m.Alias != "" {
		return m.Alias
	}
	if m.Op == "" || m.Op == "count" {
		return "count"
	}
	return m.Op + "_" + strings.TrimPrefix(m.Field, "payload:")
}

var (
	auditGroupDimensions = map[string]bool{"event_type": true, "category": true, "actor": true, "time": true}
	auditMetricOps       = map[string]bool{"count": true, "count_distinct": true, "sum": true, "avg": true, "min": true, "max": true, "percentile": true}
	auditDistinctColumns = map[string]bool{"actor_id": true, "event_type": true, "category": true, "resource": true, "resource_id": true}
	auditTimeBuckets     = map[string]bool{"day": true, "week": true, "month": true}
	auditNumericOps      = map[string]bool{"sum": true, "avg": true, "min": true, "max": true, "percentile": true}
)

// payloadKey reports whether field addresses a payload key ("payload:<key>")
// and returns the key.
func payloadKey(field string) (string, bool) {
	key, ok := strings.CutPrefix(field, "payload:")
	return key, ok && key != ""
}

// Validate checks the spec against the allowed dimensions, ops, and columns so
// the SQL builder can trust its input. It reports the first problem found.
func (s AuditAggregationSpec) Validate() error {
	if s.Bucket != "" && !auditTimeBuckets[s.Bucket] {
		return fmt.Errorf("audit: invalid time bucket %q (want day|week|month)", s.Bucket)
	}
	for _, d := range s.GroupBy {
		if _, ok := payloadKey(d); ok {
			continue
		}
		if !auditGroupDimensions[d] {
			return fmt.Errorf("audit: invalid group dimension %q", d)
		}
	}
	aliases := map[string]bool{}
	for _, m := range s.Metrics {
		if !auditMetricOps[m.Op] {
			return fmt.Errorf("audit: invalid metric op %q", m.Op)
		}
		if auditNumericOps[m.Op] {
			if _, ok := payloadKey(m.Field); !ok {
				return fmt.Errorf("audit: metric op %q requires a payload:<key> field, got %q", m.Op, m.Field)
			}
		}
		if m.Op == "count_distinct" {
			if _, ok := payloadKey(m.Field); !ok && !auditDistinctColumns[m.Field] {
				return fmt.Errorf("audit: count_distinct field %q is neither a payload:<key> nor a known column", m.Field)
			}
		}
		if m.Op == "percentile" && (m.Percentile <= 0 || m.Percentile > 1) {
			return fmt.Errorf("audit: percentile must be in (0,1], got %v", m.Percentile)
		}
		// Aliases key the response map, so a collision would silently drop one
		// metric's value; reject it instead of returning a lossy result.
		alias := m.ResolvedAlias()
		if aliases[alias] {
			return fmt.Errorf("audit: duplicate metric alias %q (set a distinct alias)", alias)
		}
		aliases[alias] = true
	}
	for _, d := range s.Derived {
		if d.Alias == "" || d.Numerator == "" || d.Denominator == "" {
			return fmt.Errorf("audit: derived metric needs alias, numerator, and denominator")
		}
		if aliases[d.Alias] {
			return fmt.Errorf("audit: duplicate metric alias %q (set a distinct alias)", d.Alias)
		}
		if !aliases[d.Numerator] {
			return fmt.Errorf("audit: derived metric %q references unknown numerator %q", d.Alias, d.Numerator)
		}
		if !aliases[d.Denominator] {
			return fmt.Errorf("audit: derived metric %q references unknown denominator %q", d.Alias, d.Denominator)
		}
		aliases[d.Alias] = true
	}
	return nil
}

// AuditAggregateBucket is one row of an aggregation result: the group key(s) and
// the computed metrics. Key/Count mirror Keys[0] and the group's COUNT(*) for
// back-compat with the count-only aggregation.
type AuditAggregateBucket struct {
	Samples map[string]int64
	Key     string
	Count   int64
	Keys    []string
	Metrics map[string]float64
}

// QueryAuditLog reads one page of the activity list from the audit store of
// record. Under postgres and both that is audit_events, read under WithOrgTx
// for the requested org so RLS lets its rows through — or, when OrgID is
// empty, under WithControlPlane to span tenants: the caller is then a platform
// admin (handler authz already enforced this in adapters/rpcs.go
// AuditServer.QueryAuditLog). Under a swap value it is the store the swap
// selected, read under the same scope, named explicitly (AuditReadScope).
func (s *Service) QueryAuditLog(ctx context.Context, q AuditQuery) ([]AuditEntry, string, int32, error) {
	if q.CollectionID != "" {
		return nil, "", 0, status.Error(codes.InvalidArgument, "collection analytics requires reader authorization")
	}
	read, err := s.auditRead(ctx, q, nil)
	if err != nil {
		return nil, "", 0, err
	}
	entries, nextToken, err := s.auditReads().ListAuditEvents(ctx, read)
	return entries, nextToken, int32(len(entries)), err
}

// auditRead is the read of q in its scope (auditReadScopeFor). Under a swap
// value it also carries the event-type facts the query or the aggregation
// needs, read from audit_event_types under that same scope — the rows the
// Postgres reads would have joined in SQL.
func (s *Service) auditRead(ctx context.Context, q AuditQuery, spec *AuditAggregationSpec) (AuditRead, error) {
	read := AuditRead{Scope: auditReadScopeFor(q), Query: q}
	if s.auditStore == nil || !auditReadNeedsTypes(q, spec) {
		return read, nil
	}
	err := postgresAuditReader{store: s.store}.within(ctx, read.Scope, func(ctx context.Context) error {
		rows, err := s.store.ListAuditEventTypes(ctx)
		if err != nil {
			return err
		}
		read.Types = NewAuditEventTypeIndex(rows)
		return nil
	})
	if err != nil {
		return AuditRead{}, fmt.Errorf("audit read: event types: %w", err)
	}
	return read, nil
}

// AggregateAuditLogForReader additionally gates exact-resource analytics on the
// reader's current resource grant. Under postgres and both the check and query
// share the tenant tx. Under a swap value the check runs in that tenant tx and
// the aggregate is then read from the store of record — two steps, no shared
// snapshot. Organization-wide audit reads retain their existing audit
// authority contract.
func (s *Service) AggregateAuditLogForReader(ctx context.Context, reader string, q AuditQuery, spec AuditAggregationSpec) ([]AuditAggregateBucket, error) {
	if q.From != nil && q.To != nil && q.From.After(*q.To) {
		return nil, status.Error(codes.InvalidArgument, "audit window is reversed")
	}
	if err := spec.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	// collection_id compiles to exactly a payload `boundary` predicate below, so
	// naming that predicate directly is the same collection read by another
	// spelling. Promote it into the gated path: otherwise the grant check below
	// is defeated by moving the filter from collection_id into payload_contains.
	if q.CollectionID == "" && isCollectionBoundaryEvent(q.EventType) {
		if boundary, ok := q.PayloadContains["boundary"].(string); ok && boundary != "" {
			q.CollectionID = boundary
		}
	}
	if q.ResourceID == "" && q.CollectionID == "" {
		return s.AggregateAuditLog(ctx, q, spec)
	}
	if reader == "" || q.OrgID == "" || (q.ResourceID != "" && q.Resource == "") || q.EventType == "" {
		return nil, status.Error(codes.InvalidArgument, "resource analytics requires reader, organization, resource and event type")
	}
	if q.CollectionID != "" {
		if !isCollectionBoundaryEvent(q.EventType) {
			return nil, status.Error(codes.InvalidArgument, "collection analytics requires an event that records a collection boundary")
		}
		if boundary, exists := q.PayloadContains["boundary"]; exists && boundary != q.CollectionID {
			return nil, status.Error(codes.InvalidArgument, "conflicting collection boundary filter")
		}
		payload := make(map[string]any, len(q.PayloadContains)+1)
		for key, value := range q.PayloadContains {
			payload[key] = value
		}
		payload["boundary"] = q.CollectionID
		q.PayloadContains = payload
	}
	compiled := q
	compiled.CollectionID = "" // Authorized and compiled into the boundary predicate above.
	if s.auditStore != nil {
		if err := s.store.WithOrgTx(ctx, q.OrgID, func(ctx context.Context) error {
			return s.authorizeAuditResourceRead(ctx, reader, q)
		}); err != nil {
			return nil, err
		}
		read, err := s.auditRead(ctx, compiled, &spec)
		if err != nil {
			return nil, err
		}
		return auditAggregateResult(s.auditStore.AggregateAuditEvents(ctx, read, spec))
	}
	var out []AuditAggregateBucket
	err := s.store.WithOrgTx(ctx, q.OrgID, func(ctx context.Context) error {
		if err := s.authorizeAuditResourceRead(ctx, reader, q); err != nil {
			return err
		}
		var err error
		out, err = s.store.AggregateAuditLog(ctx, compiled, spec)
		return err
	})
	return out, err
}

// authorizeAuditResourceRead proves the reader may read the resource or
// collection q names, inside the caller's tenant transaction. Each scope proves
// itself; there is no permissive default to fall through to. `proven` records
// that at least one check actually ran, so a future caller reaching here with
// neither scope set is denied rather than served on the strength of an
// untested assumption.
func (s *Service) authorizeAuditResourceRead(ctx context.Context, reader string, q AuditQuery) error {
	denied := status.Error(codes.PermissionDenied, "resource read access required")
	proven := false
	if q.CollectionID != "" {
		allowed, err := s.canReadAuditCollection(ctx, reader, q.OrgID, q.CollectionID)
		if err != nil {
			return err
		}
		if !allowed {
			return denied
		}
		proven = true
	}
	if q.ResourceID != "" {
		allowed, err := s.canReadAuditResource(ctx, reader, q)
		if err != nil {
			return err
		}
		if !allowed {
			return denied
		}
		proven = true
	}
	if !proven {
		return denied
	}
	return nil
}

// isCollectionBoundaryEvent reports whether eventType is a registered event
// that records a collection node under the payload key `boundary` — the shape
// for which a payload `boundary` value names a collection node. Both the
// collection filter and the payload-spelling promotion above test the same
// predicate, so the two spellings of one filter can never diverge on which
// events they authorize.
//
// The declared field is the whole predicate. It deliberately does not also
// test the event's name: a collection node is named by whichever event records
// one, not by the aggregate it happens to be filed under, and
// saas.datasource.source.removed records the boundary node its source fed. A
// name test admitted only saas.document.* — so an event outside that prefix
// carrying the same value was served from the ungated path while the
// collection_id spelling of the same question was refused, which is the
// grant check being worth nothing by another route. Nothing needs to be
// allowlisted here for a new event: declaring the field is what enrolls it.
func isCollectionBoundaryEvent(eventType string) bool {
	definition, registered := LookupAuditEvent(EventType(eventType))
	if !registered {
		return false
	}
	for _, field := range definition.Fields {
		if field.Name == "boundary" {
			return true
		}
	}
	return false
}

// Datasources are connected to structural collection nodes, not placed records.
// Resolve the stored boundary and reuse the documents/read scope predicates
// that govern collection retrieval; no request-supplied boundary is trusted.
func (s *Service) canReadAuditResource(ctx context.Context, reader string, q AuditQuery) (bool, error) {
	if q.Resource != "datasource" {
		allowed, _, err := s.store.CheckAccess(ctx, reader, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, q.Resource, q.ResourceID, "read")
		return allowed, err
	}
	source, err := s.store.GetDatasourceSource(ctx, q.OrgID, q.ResourceID)
	if err != nil {
		return false, err
	}
	if source == nil || source.OrgID != q.OrgID || source.BoundaryNodeID == "" {
		return false, nil
	}
	return s.canReadAuditCollection(ctx, reader, q.OrgID, source.BoundaryNodeID)
}

func (s *Service) canReadAuditCollection(ctx context.Context, reader, orgID, boundaryID string) (bool, error) {
	return s.store.CanReadScopeNode(ctx, orgID, reader, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "documents", "read", boundaryID)
}

// AggregateAuditLog computes grouped metrics over audit events for analytics.
// The spec selects the group dimensions (event type, category, actor, time
// bucket, or a payload field), the aggregations (count, distinct-count, sum,
// avg, min, max, percentile over payload fields), and any derived ratios,
// filtered by the same predicates as QueryAuditLog, and read in the same scope.
func (s *Service) AggregateAuditLog(ctx context.Context, q AuditQuery, spec AuditAggregationSpec) ([]AuditAggregateBucket, error) {
	if q.CollectionID != "" {
		return nil, status.Error(codes.InvalidArgument, "collection analytics requires reader authorization")
	}
	read, err := s.auditRead(ctx, q, &spec)
	if err != nil {
		return nil, err
	}
	return auditAggregateResult(s.auditReads().AggregateAuditEvents(ctx, read, spec))
}

// buildAuditEntry assembles an AuditEntry. actorID is the effective subject the
// action ran as; when that subject is not the person behind the request, the
// entry additionally records the real actor, so an impersonated action is
// attributable to both, and the registered client the call came through when
// there was one. All of them come from the typed request identity the
// authentication interceptors project — never from a separate metadata
// convention, which is how the two representations drifted apart before.
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
	if identity, ok := auth.VerifiedRequestIdentity(ctx); ok {
		if identity.Impersonated() {
			entry.IsImpersonated = true
			entry.ImpersonatedBy = identity.RealActorID()
		}
		entry.ClientID = identity.ClientID
	}
	return entry
}

// emit is the fire-and-forget audit path: the emitter owns its own transaction,
// so the event is written after (and independently of) the caller's mutation.
// It is reserved for DurabilityObservational events — an observation must not
// vanish because the domain transaction it was observed under rolled back.
// TestAuditDurability_EmitSitesMatchTheirClassification enforces that.
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
// its compliance record).
func (s *Service) emitTx(ctx context.Context, actorID, actorType string, eventType EventType, resource, resourceID, orgID string, payload ...map[string]any) error {
	if s.audit == nil {
		return nil
	}
	return s.emitEntryTx(ctx, s.buildAuditEntry(ctx, actorID, actorType, eventType, resource, resourceID, orgID, payload...))
}

// emitEntryTx writes a pre-built AuditEntry on the caller's ambient transaction,
// the same fail-closed contract as emitTx. It is the seam for callers that set
// fields buildAuditEntry does not take positionally (e.g. IdempotencyKey).
//
// An emitter that cannot write on the caller's transaction is an error, not a
// reason to downgrade to a best-effort write: silently emitting outside the
// transaction is exactly the mutation/record split this path exists to close.
// Production never reaches that branch — VerifyAuditWiring rejects such an
// emitter at boot — so it fires only for a test double that skipped the
// contract.
func (s *Service) emitEntryTx(ctx context.Context, entry AuditEntry) error {
	if s.auditTx != nil {
		return s.auditTx.EmitTx(ctx, entry)
	}
	if s.audit == nil {
		return nil
	}
	return fmt.Errorf("audit: emitter %T cannot write %q on the caller's transaction (implement TxAuditEmitter)", s.audit, entry.EventType)
}

// publishDomainEvent writes the external domain event that carries this audit
// record to its subscribers. The relay resolves matching webhook subscriptions
// after commit and dispatches them; the audit spine itself is never a delivery
// channel, so the event is a sibling of the record, not a re-read of it.
//
// The envelope id is the audit record's id, which is the X-Webhook-Event-ID an
// endpoint deduplicates on, so a delivery is identified the same way it was
// before webhooks moved onto subscriptions.
//
// Only a type declared external is published: eligibility to leave the platform
// is granted by declaration. The answer comes from the resolver the write
// already made, which spans both halves of the registry — the composed catalog
// for a code-owned type, the type's own row for one a solution or a composed
// module declared — so a declared type is published exactly when its producer
// and the operator both said it may be. A platform-scope record never reaches
// here — the caller returns early when the entry has no organization — so an
// event is always tenant-scoped.
//
// No partition key is set, and that is deliberate rather than an omission. A
// partition key is a promise of FIFO within it, and publish_domain_event buys
// that promise with a per-partition advisory lock held until the producer's
// transaction commits. Keying it on the organization would serialize every
// audited mutation in that organization against every other one — for an
// ordering nothing consumes: an outbound webhook is dispatched in
// subscription-id order, and the platform namespace is not subscribable by a
// module, so no ordered subscriber can exist for these types.
func (e *DurableAuditEmitter) publishDomainEvent(ctx context.Context, entry AuditEntry, resolved ResolvedAuditEvent) error {
	if e.transport == nil || !resolved.ExternallyDeliverable() {
		return nil
	}
	data, err := AuditEventWebhookData(entry, resolved)
	if err != nil {
		return err
	}
	return e.transport.Publish(ctx, moduleTx(ctx), &events.EventEnvelope{
		Id:               entry.ID,
		Type:             string(entry.EventType),
		Source:           domainEventSource,
		Subject:          entry.ResourceID,
		Specversion:      "1.0",
		Datacontenttype:  "application/json",
		Time:             timestamppb.New(entry.CreatedAt.UTC()),
		Data:             data,
		TenantId:         entry.OrgID,
		ActorPrincipalId: entry.ActorID,
		// The registered contract version of this event. It is the CloudEvents
		// attribute EVENTS.md defines as the minor within the dataschema major, so
		// a subscriber reading the envelope — rather than digging into data — is
		// what it has to tell a revised payload by. Leaving it unset defaults the
		// stored event and its delivery job to 1, which would say "v1" for the
		// saas.webhook.* types revised to v2 while the payload said otherwise.
		SchemaVersion: uint32(entry.SchemaVersion),
	})
}
