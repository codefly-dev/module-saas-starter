package business_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/events"
	eventsv1 "accounts/pkg/gen/saas/events/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

type journalRecorder struct {
	events.Transport
	published []*eventsv1.EventEnvelope
}

func (j *journalRecorder) Publish(_ context.Context, _ events.TxHandle, e *eventsv1.EventEnvelope) error {
	j.published = append(j.published, e)
	return nil
}

type idleProducer struct{}

func (idleProducer) EnqueueJob(context.Context, *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error) {
	return &jobsv1.EnqueueJobResponse{}, nil
}

type noWarehouse struct{}

func (noWarehouse) Append(context.Context, business.AuditEntry) error { return nil }
func (noWarehouse) List(context.Context, business.AuditRead) ([]business.AuditEntry, string, error) {
	return nil, "", nil
}
func (noWarehouse) Aggregate(context.Context, business.AuditRead, business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	return nil, nil
}

// Audit a module emits through ModuleEmitAuditEvent takes the same path as the
// host's own: under the warehouse backend it is journaled on the caller's
// transaction, whether it is tenant-scoped or system-scoped.
func TestModuleEmitAuditEventIsJournaledUnderTheWarehouseBackend(t *testing.T) {
	store := fakeTxStore{}
	svc := newModuleServiceWithStore(t, store, &fakeJobBackend{}, true)
	transport := &journalRecorder{}
	audit, err := business.NewAuditService(business.AuditServiceConfig{
		Backend: business.AuditBackendWarehouse, Store: store, Producer: idleProducer{},
		Transport: transport, Warehouse: noWarehouse{},
	})
	if err != nil {
		t.Fatalf("NewAuditService: %v", err)
	}
	svc.SetAuditService(audit)
	if err := svc.VerifyAuditWiring(); err != nil {
		t.Fatalf("the warehouse backend writes on the caller's transaction too: %v", err)
	}

	for name, tenant := range map[string]string{"tenant-scoped": moduleTenantA, "system-scoped": ""} {
		transport.published = nil
		if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
			tenant, "saas.document.ingested", modulePrincSvc, "example-solution", "entry-1", "", nil); err != nil {
			t.Fatalf("%s emit: %v", name, err)
		}
		var journal *eventsv1.EventEnvelope
		for _, e := range transport.published {
			if e.GetType() == business.AuditRecordedEventType {
				journal = e
			}
		}
		if journal == nil {
			t.Fatalf("%s: no journal event among %d published", name, len(transport.published))
		}
		if journal.GetTenantId() != tenant {
			t.Fatalf("%s: journal tenant = %q, want %q", name, journal.GetTenantId(), tenant)
		}
		entry, err := business.AuditEntryFromJournal(journal.GetData())
		if err != nil {
			t.Fatal(err)
		}
		if entry.EventType != "saas.document.ingested" || entry.OrgID != tenant || entry.ResourceID != "entry-1" ||
			entry.ActorID != modulePrincSvc || entry.ActorType != business.ActorTypeSystem {
			t.Fatalf("%s: journaled entry = %+v", name, entry)
		}
	}
}
