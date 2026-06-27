package business

import (
	"context"
	"time"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/metadata"
)

// AuditEntry is the domain representation of an audit event.
type AuditEntry struct {
	ID             string
	ActorID        string
	ActorType      string // "user", "api_key", "system"
	Action         string // "user.registered", "api_key.created", etc.
	Resource       string
	ResourceID     string
	OrgID          string
	Metadata       map[string]string
	IPAddress      string
	ImpersonatedBy string // admin user ID if this action was performed during impersonation
	IsImpersonated bool
	CreatedAt      time.Time
}

// AuditEmitter writes audit events. Implementations must be non-blocking.
type AuditEmitter interface {
	Emit(ctx context.Context, entry AuditEntry)
}

// AsyncAuditEmitter buffers events and writes them asynchronously.
type AsyncAuditEmitter struct {
	store Store
	ch    chan auditWork
}

type auditWork struct {
	entry AuditEntry
}

func NewAsyncAuditEmitter(store Store, bufferSize int) *AsyncAuditEmitter {
	e := &AsyncAuditEmitter{
		store: store,
		ch:    make(chan auditWork, bufferSize),
	}
	go e.drain()
	return e
}

func (e *AsyncAuditEmitter) Emit(ctx context.Context, entry AuditEntry) {
	select {
	case e.ch <- auditWork{entry: entry}:
	default:
		// Buffer full — drop rather than block business logic. Loud
		// log so an under-sized buffer in prod doesn't silently lose
		// audit rows. If this fires, either bump bufferSize or
		// investigate why drain() is falling behind.
		wool.Get(ctx).In("AsyncAuditEmitter.Emit").Warn("audit buffer full; event dropped",
			wool.Field("action", entry.Action),
			wool.Field("actor_id", entry.ActorID),
			wool.Field("org_id", entry.OrgID),
		)
	}
}

func (e *AsyncAuditEmitter) drain() {
	for w := range e.ch {
		ctx := context.Background()
		// audit_events is RLS-protected (Phase 2D). Tenant-scoped
		// events go through WithOrgTx so the WITH CHECK passes; system
		// events (no resolved org — gdpr, billing webhooks, scheduled
		// cleanup) carry an empty OrgID and write under WithBypass,
		// which is also the only way NULL-org rows are visible to
		// later readers (cross-tenant by nature).
		if w.entry.OrgID != "" {
			_ = e.store.WithOrgTx(ctx, w.entry.OrgID, func(ctx context.Context) error {
				return e.store.InsertAuditEvent(ctx, w.entry)
			})
		} else {
			_ = e.store.WithBypass(ctx, func(ctx context.Context) error {
				return e.store.InsertAuditEvent(ctx, w.entry)
			})
		}
	}
}

func (e *AsyncAuditEmitter) Close() {
	close(e.ch)
}

// QueryAuditLog delegates to the store, scoping the read to the
// requested org under WithOrgTx so RLS lets the rows through. When
// orgID is empty the caller is platform-admin (handler authz
// already enforced this in adapters/rpcs.go AuditServer.QueryAuditLog)
// and we use WithBypass to span tenants.
func (s *Service) QueryAuditLog(ctx context.Context, orgID, actorID, action, resource, resourceID string,
	from, to *time.Time, pageSize int32, pageToken string) ([]AuditEntry, string, int32, error) {
	var entries []AuditEntry
	var nextToken string
	var total int32
	wrap := func(ctx context.Context) error {
		ev, nt, tot, err := s.store.QueryAuditLog(ctx, orgID, actorID, action, resource, resourceID, from, to, pageSize, pageToken)
		entries, nextToken, total = ev, nt, tot
		return err
	}
	var err error
	if orgID == "" {
		err = s.store.WithBypass(ctx, wrap)
	} else {
		err = s.store.WithOrgTx(ctx, orgID, wrap)
	}
	return entries, nextToken, total, err
}

// emit is a convenience method on Service for audit emission.
// Automatically detects impersonation context from gRPC metadata headers
// injected by the auth sidecar (x-is-impersonated, x-impersonated-by).
func (s *Service) emit(ctx context.Context, actorID, actorType, action, resource, resourceID, orgID string) {
	if s.audit == nil {
		return
	}

	entry := AuditEntry{
		ActorID:    actorID,
		ActorType:  actorType,
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		OrgID:      orgID,
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
