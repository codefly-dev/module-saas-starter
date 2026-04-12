package business

import (
	"context"
	"time"

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

func (e *AsyncAuditEmitter) Emit(_ context.Context, entry AuditEntry) {
	select {
	case e.ch <- auditWork{entry: entry}:
	default:
		// Buffer full — drop rather than block business logic
	}
}

func (e *AsyncAuditEmitter) drain() {
	for w := range e.ch {
		_ = e.store.InsertAuditEvent(context.Background(), w.entry)
	}
}

func (e *AsyncAuditEmitter) Close() {
	close(e.ch)
}

// QueryAuditLog delegates to the store.
func (s *Service) QueryAuditLog(ctx context.Context, orgID, actorID, action, resource, resourceID string,
	from, to *time.Time, pageSize int32, pageToken string) ([]AuditEntry, string, int32, error) {
	return s.store.QueryAuditLog(ctx, orgID, actorID, action, resource, resourceID, from, to, pageSize, pageToken)
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
