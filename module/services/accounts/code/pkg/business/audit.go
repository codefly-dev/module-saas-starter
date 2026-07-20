package business

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/jobs"

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
	write := func(ctx context.Context) error {
		if err := e.store.InsertAuditEvent(ctx, entry); err != nil {
			return err
		}
		if entry.OrgID == "" {
			return nil
		}
		subscriptions, err := e.store.GetActiveWebhookSubscriptions(ctx, entry.Action)
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
			wool.Field("action", entry.Action),
			wool.Field("org_id", entry.OrgID),
			wool.ErrField(err),
		)
	}
}

func (e *DurableAuditEmitter) Close() {}

// QueryAuditLog delegates to the store, scoping the read to the
// requested org under WithOrgTx so RLS lets the rows through. When
// orgID is empty the caller is platform-admin (handler authz
// already enforced this in adapters/rpcs.go AuditServer.QueryAuditLog)
// and we use WithControlPlane to span tenants.
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
		err = s.store.WithControlPlane(ctx, wrap)
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
