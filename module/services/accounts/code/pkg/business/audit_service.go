package business

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"accounts/pkg/events"
	"accounts/pkg/jobs"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AuditBackend names where audit entries are kept. It is chosen once, at
// startup, from configuration, and never per call.
type AuditBackend string

const (
	// AuditBackendPostgres keeps every entry in audit_events. The default: the
	// entry is written in the caller's transaction, and Service.VerifyAuditWiring
	// holds exactly as before.
	AuditBackendPostgres AuditBackend = "postgres"
	// AuditBackendWarehouse keeps entries in a warehouse. The warehouse cannot
	// join the caller's transaction, so the entry is published to the journal
	// (domain_events) on that transaction instead, and a task appends it to the
	// warehouse afterwards through Storage. It replaces audit_events as the
	// store of record: a swap, not a tee.
	AuditBackendWarehouse AuditBackend = "warehouse"
)

// ParseAuditBackend reads the audit-storage backend. Empty is the default.
func ParseAuditBackend(raw string) (AuditBackend, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(AuditBackendPostgres):
		return AuditBackendPostgres, nil
	case string(AuditBackendWarehouse):
		return AuditBackendWarehouse, nil
	default:
		return "", fmt.Errorf("audit storage backend must be %s or %s", AuditBackendPostgres, AuditBackendWarehouse)
	}
}

// AuditServiceConfig is what AuditService is built from, once, at startup.
type AuditServiceConfig struct {
	Backend AuditBackend
	Store   Store
	// Producer is the transactional job producer the external tee enqueues on.
	Producer jobs.Producer
	// Transport publishes the org-scoped webhook event beside each audit row
	// and, under the warehouse backend, the journal event of every entry. The
	// warehouse backend needs it.
	Transport events.Transport
	// ExternalTee enqueues an audit-export job beside each org-scoped audit row
	// (AUDIT_SINK=both). It copies audit_events rows, so it is refused under the
	// warehouse backend.
	ExternalTee bool
	// Warehouse is the storage of the warehouse backend. It is required exactly
	// when Backend is AuditBackendWarehouse.
	Warehouse AuditStorage
}

// AuditService is the one place audit is written and read. It owns the choice
// of AuditStorage, which it makes once at construction:
//
//   - under Postgres, Emit and EmitTx append to audit_events on the caller's
//     transaction, and every read goes to audit_events;
//   - under the warehouse, Emit and EmitTx publish the entry to the journal on
//     the caller's transaction (including an entry with no organization) and
//     append nothing, and every read goes to the warehouse.
//
// It is the TxAuditEmitter the Service writes through, so a module's audit
// (ModuleEmitAuditEvent) and every host mutation take the same path, and it is
// what the Service's activity list, aggregation and export read through.
type AuditService struct {
	backend AuditBackend
	store   Store
	// storage serves every read, and is what a delivery appends to under the
	// warehouse backend.
	storage AuditStorage
	// postgres is the Postgres storage, non-nil exactly under the Postgres
	// backend. It is what lets a resource-gated read check and read in one
	// transaction.
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
		if cfg.Warehouse != nil {
			return nil, errors.New("audit: a warehouse storage is wired but the audit backend is postgres")
		}
		service.backend, service.storage, service.postgres = AuditBackendPostgres, postgres, postgres
		opts = append(opts, WithAuditStorage(postgres))
	case AuditBackendWarehouse:
		if cfg.Warehouse == nil {
			return nil, errors.New("audit: the warehouse backend needs a warehouse storage")
		}
		if cfg.ExternalTee {
			return nil, errors.New("audit: the external tee copies audit_events rows and cannot run under the warehouse backend")
		}
		service.storage = cfg.Warehouse
		opts = append(opts, WithAuditJournal())
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

// Storage is the AuditStorage the backend reads from, and a delivery appends to
// under the warehouse backend.
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
	out, err := a.storage.Aggregate(ctx, read, spec)
	return out, refusalOfUnsupportedMetric(err)
}

// refusalOfUnsupportedMetric answers a storage's refusal of the percentile
// metric as what it is: the request asked for something this deployment's audit
// storage cannot compute, not a fault in the service.
func refusalOfUnsupportedMetric(err error) error {
	if errors.Is(err, ErrAuditPercentileUnsupported) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return err
}

// AggregateAuthorized aggregates after authorize has passed inside the
// organization's transaction. Under Postgres the check and the read share that
// one transaction, one snapshot. Under the warehouse the check still runs in the
// organization's Postgres transaction and the aggregate is read afterwards from
// the warehouse: two steps, no shared snapshot.
func (a *AuditService) AggregateAuthorized(ctx context.Context, q AuditQuery, spec AuditAggregationSpec, authorize func(context.Context) error) ([]AuditAggregateBucket, error) {
	read, err := a.read(ctx, q, &spec)
	if err != nil {
		return nil, err
	}
	if a.postgres != nil {
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
	if read.Scope.Platform() {
		return nil, status.Error(codes.InvalidArgument, "a resource-gated read names its organization")
	}
	if err := a.store.WithOrgTx(ctx, read.Scope.OrgID(), authorize); err != nil {
		return nil, err
	}
	out, err := a.storage.Aggregate(ctx, read, spec)
	return out, refusalOfUnsupportedMetric(err)
}

// read is the read of q in its scope. A storage outside Postgres also gets the
// event-type facts the query or the aggregation needs, read from
// audit_event_types in that same scope — the rows the Postgres reads join in SQL.
func (a *AuditService) read(ctx context.Context, q AuditQuery, spec *AuditAggregationSpec) (AuditRead, error) {
	read := AuditRead{Scope: auditReadScopeFor(q), Query: q}
	if a.postgres != nil || !auditReadNeedsTypes(q, spec) {
		return read, nil
	}
	err := withinAuditScope(ctx, a.store, read.Scope, func(ctx context.Context) error {
		rows, err := a.store.ListAuditEventTypes(ctx)
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
