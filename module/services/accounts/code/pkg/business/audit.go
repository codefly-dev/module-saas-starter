package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/eventcatalog"
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
	CreatedAt      time.Time
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
	return emitter, nil
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

// write inserts the audit row and publishes its domain event using whatever
// transaction is already on ctx (getQueryExecutor / Publish both pick it up).
func (e *DurableAuditEmitter) write(ctx context.Context, entry AuditEntry) error {
	if entry.IdempotencyKey != "" {
		reserved, err := e.store.ReserveAuditIdempotency(ctx, entry.OrgID, string(entry.EventType), entry.IdempotencyKey)
		if err != nil {
			return err
		}
		if !reserved {
			// A prior emit of this (org, event_type, idempotency_key) already wrote
			// the event in a committed transaction. Skip the insert and the webhook
			// fan-out and report success: the intended effect (exactly one audit row
			// and one set of deliveries) is already in place. Reserving inside the
			// caller's tx keeps the guard row and the audit row atomic, so a rolled
			// back audit write also frees the key for a genuine retry.
			return nil
		}
	}
	if err := e.store.InsertAuditEvent(ctx, entry); err != nil {
		return err
	}
	if entry.OrgID == "" {
		return nil
	}
	// The tenant predicate is the event's own org, passed explicitly: the audit
	// write runs inside its mutation's transaction, and that transaction is the
	// control plane for platform-admin and other privileged writes, where RLS
	// would not scope this read at all.
	if err := e.publishDomainEvent(ctx, entry); err != nil {
		return err
	}
	if e.teeExternal {
		if err := enqueueAuditExport(ctx, e.producer, entry); err != nil {
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
	Namespace       string
	Resource        string
	ResourceID      string
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

// QueryAuditLog delegates to the store, scoping the read to the
// requested org under WithOrgTx so RLS lets the rows through. When
// OrgID is empty the caller is platform-admin (handler authz
// already enforced this in adapters/rpcs.go AuditServer.QueryAuditLog)
// and we use WithControlPlane to span tenants.
func (s *Service) QueryAuditLog(ctx context.Context, q AuditQuery) ([]AuditEntry, string, int32, error) {
	if q.CollectionID != "" {
		return nil, "", 0, status.Error(codes.InvalidArgument, "collection analytics requires reader authorization")
	}
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

// AggregateAuditLogForReader additionally gates exact-resource analytics on the
// reader's current resource grant. The check and query share the tenant tx.
// Organization-wide audit reads retain their existing audit authority contract.
func (s *Service) AggregateAuditLogForReader(ctx context.Context, reader string, q AuditQuery, spec AuditAggregationSpec) ([]AuditAggregateBucket, error) {
	if q.From != nil && q.To != nil && q.From.After(*q.To) {
		return nil, status.Error(codes.InvalidArgument, "audit window is reversed")
	}
	if err := spec.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if q.ResourceID == "" && q.CollectionID == "" {
		return s.AggregateAuditLog(ctx, q, spec)
	}
	if reader == "" || q.OrgID == "" || (q.ResourceID != "" && q.Resource == "") || q.EventType == "" {
		return nil, status.Error(codes.InvalidArgument, "resource analytics requires reader, organization, resource and event type")
	}
	if q.CollectionID != "" {
		definition, registered := LookupAuditEvent(EventType(q.EventType))
		hasBoundary := false
		for _, field := range definition.Fields {
			if field.Name == "boundary" {
				hasBoundary = true
			}
		}
		if !registered || !strings.HasPrefix(q.EventType, "saas.document.") || !hasBoundary {
			return nil, status.Error(codes.InvalidArgument, "collection analytics requires a registered document boundary event")
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
	var out []AuditAggregateBucket
	err := s.store.WithOrgTx(ctx, q.OrgID, func(ctx context.Context) error {
		allowed := true
		var err error
		if q.CollectionID != "" {
			allowed, err = s.canReadAuditCollection(ctx, reader, q.OrgID, q.CollectionID)
		}
		if err == nil && allowed && q.ResourceID != "" {
			allowed, err = s.canReadAuditResource(ctx, reader, q)
		}
		if err != nil {
			return err
		}
		if !allowed {
			return status.Error(codes.PermissionDenied, "resource read access required")
		}
		compiled := q
		compiled.CollectionID = "" // Authorized and compiled into the boundary predicate above.
		out, err = s.store.AggregateAuditLog(ctx, compiled, spec)
		return err
	})
	return out, err
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
// filtered by the same predicates as QueryAuditLog.
func (s *Service) AggregateAuditLog(ctx context.Context, q AuditQuery, spec AuditAggregationSpec) ([]AuditAggregateBucket, error) {
	if q.CollectionID != "" {
		return nil, status.Error(codes.InvalidArgument, "collection analytics requires reader authorization")
	}
	var out []AuditAggregateBucket
	wrap := func(ctx context.Context) error {
		var err error
		out, err = s.store.AggregateAuditLog(ctx, q, spec)
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

// buildAuditEntry assembles an AuditEntry. actorID is the effective subject the
// action ran as; when that subject is not the person behind the request, the
// entry additionally records the real actor, so an impersonated action is
// attributable to both. Both ids come from the typed request identity the
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
	if identity, ok := auth.VerifiedRequestIdentity(ctx); ok && identity.Impersonated() {
		entry.IsImpersonated = true
		entry.ImpersonatedBy = identity.RealActorID()
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
// Only a type the catalog declares external is published: eligibility to leave
// the platform is granted by declaration. A platform-scope record never reaches
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
func (e *DurableAuditEmitter) publishDomainEvent(ctx context.Context, entry AuditEntry) error {
	if e.transport == nil || !eventcatalog.IsExternalPublished(string(entry.EventType)) {
		return nil
	}
	data, err := AuditEventWebhookData(entry)
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
