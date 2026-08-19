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
	// OnBehalfOfPrincipalID is the owner an agent/service acted for, and
	// ActorChainHopID the durable actor_chain_journal hop that authorized it
	// (RFC-0003). Both empty for direct, first-party actions.
	OnBehalfOfPrincipalID string
	ActorChainHopID       string
	CreatedAt             time.Time
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

func (e *DurableAuditEmitter) Emit(ctx context.Context, entry AuditEntry) {
	if entry.ID == "" {
		entry.ID = NewIDString()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	if def, ok := LookupAuditEvent(entry.EventType); ok && entry.SchemaVersion == 0 {
		entry.SchemaVersion = def.Version
	}
	// Validation is advisory: a security event is never dropped because its
	// payload drifted from the registered schema. Log so drift surfaces.
	if err := ValidatePayload(entry.EventType, entry.Payload); err != nil {
		wool.Get(ctx).In("DurableAuditEmitter.Emit").Warn(
			"audit payload failed registry validation",
			wool.Field("event_type", string(entry.EventType)),
			wool.ErrField(err),
		)
	}
	write := func(ctx context.Context) error {
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
	var err error
	if entry.OrgID == "" {
		err = e.store.WithControlPlane(ctx, write)
	} else {
		err = e.store.WithOrgTx(ctx, entry.OrgID, write)
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

// emit is a convenience method on Service for audit emission.
// Automatically detects impersonation context from gRPC metadata headers
// injected by the auth sidecar (x-is-impersonated, x-impersonated-by).
func (s *Service) emit(ctx context.Context, actorID, actorType string, eventType EventType, resource, resourceID, orgID string, payload ...map[string]any) {
	if s.audit == nil {
		return
	}

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

	// Extract impersonation context from gRPC metadata (set by auth sidecar)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("x-is-impersonated"); len(vals) > 0 && vals[0] == "true" {
			entry.IsImpersonated = true
			if by := md.Get("x-impersonated-by"); len(by) > 0 {
				entry.ImpersonatedBy = by[0]
			}
		}
	}

	s.audit.Emit(ctx, entry)
}
