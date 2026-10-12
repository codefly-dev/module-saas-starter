package business

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/events"
	"accounts/pkg/jobs"
)

// AuditBackend names where audit entries are kept. It is chosen once, at
// startup, from configuration, and never per call.
type AuditBackend string

const (
	// AuditBackendPostgres keeps every entry in audit_events. The default: the
	// entry is written in the caller's transaction, and Service.VerifyAuditWiring
	// holds exactly as before.
	AuditBackendPostgres AuditBackend = "postgres"
)

// AuditServiceConfig is what AuditService is built from, once, at startup.
type AuditServiceConfig struct {
	Backend AuditBackend
	Store   Store
	// Producer is the transactional job producer the external tee enqueues on.
	Producer jobs.Producer
	// Transport publishes the org-scoped webhook event beside each audit row.
	Transport events.Transport
	// ExternalTee enqueues an audit-export job beside each org-scoped audit row
	// (AUDIT_SINK=both).
	ExternalTee bool
}

// AuditService is the one place audit is written and read. It owns the choice
// of AuditStorage, which it makes once at construction. Under Postgres, Emit and
// EmitTx append to audit_events on the caller's transaction, and every read goes
// to audit_events.
//
// It is the TxAuditEmitter the Service writes through, so a module's audit
// (ModuleEmitAuditEvent) and every host mutation take the same path, and it is
// what the Service's activity list, aggregation and export read through.
type AuditService struct {
	backend AuditBackend
	store   Store
	// storage serves every read.
	storage AuditStorage
	// postgres is the Postgres storage, non-nil under the Postgres backend. It is
	// what lets a resource-gated read check and read in one transaction.
	postgres *PostgresAuditStorage
	// emitter is nil on a read-only AuditService (newAuditReads).
	emitter *DurableAuditEmitter
}

var _ TxAuditEmitter = (*AuditService)(nil)

// NewAuditService builds the AuditService for cfg.Backend. A backend it cannot
// build is an error here, at startup, not a failure on the first audited write.
func NewAuditService(cfg AuditServiceConfig) (*AuditService, error) {
	if cfg.Store == nil {
		return nil, errors.New("audit: store is required")
	}
	postgres, err := NewPostgresAuditStorage(cfg.Store)
	if err != nil {
		return nil, err
	}
	service := &AuditService{backend: cfg.Backend, store: cfg.Store}
	var opts []DurableAuditEmitterOption
	if cfg.ExternalTee {
		opts = append(opts, WithExternalTee())
	}
	if cfg.Transport != nil {
		opts = append(opts, WithDomainEventTransport(cfg.Transport))
	}
	switch cfg.Backend {
	case AuditBackendPostgres, "":
		service.backend, service.storage, service.postgres = AuditBackendPostgres, postgres, postgres
		opts = append(opts, WithAuditStorage(postgres))
	default:
		return nil, fmt.Errorf("audit: unknown backend %q", cfg.Backend)
	}
	service.emitter, err = NewDurableAuditEmitter(cfg.Store, cfg.Producer, opts...)
	if err != nil {
		return nil, err
	}
	return service, nil
}

// newAuditReads is the read side of the Postgres backend, which a Service uses
// until it is given an AuditService, so every Service reads audit_events exactly
// as it always has. It writes nothing.
func newAuditReads(store Store) (*AuditService, error) {
	postgres, err := NewPostgresAuditStorage(store)
	if err != nil {
		return nil, err
	}
	return &AuditService{backend: AuditBackendPostgres, store: store, storage: postgres, postgres: postgres}, nil
}

// Backend is the backend chosen at startup.
func (a *AuditService) Backend() AuditBackend { return a.backend }

// Storage is the AuditStorage the backend reads from.
func (a *AuditService) Storage() AuditStorage { return a.storage }

// Emit writes an entry on a transaction of its own (AuditEmitter).
func (a *AuditService) Emit(ctx context.Context, entry AuditEntry) {
	if a.emitter == nil {
		return
	}
	a.emitter.Emit(ctx, entry)
}

// EmitTx writes an entry on the caller's ambient transaction (TxAuditEmitter).
func (a *AuditService) EmitTx(ctx context.Context, entry AuditEntry) error {
	if a.emitter == nil {
		return errors.New("audit: this AuditService only reads")
	}
	return a.emitter.EmitTx(ctx, entry)
}

// Close releases nothing today; it keeps the emitter's contract.
func (a *AuditService) Close() {}

// List reads one page of the activity list in the scope q names: its
// organization, or the platform's when it names none (a read the transport
// admits only for a platform admin).
func (a *AuditService) List(ctx context.Context, q AuditQuery) ([]AuditEntry, string, error) {
	read, err := a.read(ctx, q, nil)
	if err != nil {
		return nil, "", err
	}
	return a.storage.List(ctx, read)
}

// Aggregate groups the entries q selects per spec, in the same scope as List.
func (a *AuditService) Aggregate(ctx context.Context, q AuditQuery, spec AuditAggregationSpec) ([]AuditAggregateBucket, error) {
	read, err := a.read(ctx, q, &spec)
	if err != nil {
		return nil, err
	}
	return a.storage.Aggregate(ctx, read, spec)
}

// AggregateAuthorized aggregates after authorize has passed, inside the one
// transaction the read's scope names: the check and the read share a snapshot.
func (a *AuditService) AggregateAuthorized(ctx context.Context, q AuditQuery, spec AuditAggregationSpec, authorize func(context.Context) error) ([]AuditAggregateBucket, error) {
	read, err := a.read(ctx, q, &spec)
	if err != nil {
		return nil, err
	}
	var out []AuditAggregateBucket
	err = a.postgres.within(ctx, read.Scope, func(ctx context.Context) error {
		if err := authorize(ctx); err != nil {
			return err
		}
		var err error
		out, err = a.postgres.AggregateOnTx(ctx, read, spec)
		return err
	})
	return out, err
}

// read is the read of q in its scope.
func (a *AuditService) read(_ context.Context, q AuditQuery, _ *AuditAggregationSpec) (AuditRead, error) {
	return AuditRead{Scope: auditReadScopeFor(q), Query: q}, nil
}
