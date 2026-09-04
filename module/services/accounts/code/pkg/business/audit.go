package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	store       Store
	producer    jobs.Producer
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
	Resource        string
	ResourceID      string
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

// AggregateAuditLog computes grouped metrics over audit events for analytics.
// The spec selects the group dimensions (event type, category, actor, time
// bucket, or a payload field), the aggregations (count, distinct-count, sum,
// avg, min, max, percentile over payload fields), and any derived ratios,
// filtered by the same predicates as QueryAuditLog.
func (s *Service) AggregateAuditLog(ctx context.Context, q AuditQuery, spec AuditAggregationSpec) ([]AuditAggregateBucket, error) {
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
