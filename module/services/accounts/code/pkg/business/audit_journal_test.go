package business

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"accounts/pkg/eventcatalog"
	"accounts/pkg/events"
	eventsv1 "accounts/pkg/gen/saas/events/v1"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// journalTransport records every envelope published, with the transaction
// handle it was published on, in order.
type journalTransport struct {
	events.Transport
	published []*eventsv1.EventEnvelope
}

func (j *journalTransport) Publish(_ context.Context, _ events.TxHandle, e *eventsv1.EventEnvelope) error {
	j.published = append(j.published, e)
	return nil
}

func (j *journalTransport) types() []string {
	var out []string
	for _, e := range j.published {
		out = append(out, e.GetType())
	}
	return out
}

func newWarehouseAuditService(t *testing.T) (*AuditService, *auditServiceStore, *journalTransport, *recordingStorage) {
	t.Helper()
	store := &auditServiceStore{}
	transport := &journalTransport{}
	warehouse := &recordingStorage{}
	service, err := NewAuditService(AuditServiceConfig{
		Backend: AuditBackendWarehouse, Store: store, Producer: silentProducer{}, Transport: transport, Warehouse: warehouse,
	})
	if err != nil {
		t.Fatalf("NewAuditService: %v", err)
	}
	return service, store, transport, warehouse
}

// The journal event carries the whole entry, and the entry comes back out of it
// exactly: the delivery reads nothing else.
func TestAuditJournalEnvelopeCarriesTheWholeEntry(t *testing.T) {
	at := time.Date(2026, 10, 12, 8, 30, 0, 123456000, time.UTC)
	entry := AuditEntry{
		ID: uuid.NewString(), EventType: EventDocumentRead, SchemaVersion: 2,
		ActorID: uuid.NewString(), ActorType: ActorTypeUser, Resource: "datasource", ResourceID: "doc-1",
		OrgID: auditServiceOrg, IPAddress: "203.0.113.7", ClientID: "client-1",
		IsImpersonated: true, ImpersonatedBy: uuid.NewString(), CreatedAt: at,
		Payload: map[string]any{"amount": json.Number("12345678901234567890.50"), "nested": map[string]any{"k": []any{"x", json.Number("1")}}, "label": "<b>&é"},
	}
	envelope, err := NewAuditJournalEnvelope(entry)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.GetType() != AuditRecordedEventType || envelope.GetType() != "saas.audit.recorded.v1" {
		t.Fatalf("type = %q", envelope.GetType())
	}
	if envelope.GetTenantId() != entry.OrgID || envelope.GetSubject() != entry.ID || envelope.GetPartitionKey() != "" {
		t.Fatalf("tenant %q subject %q partition %q", envelope.GetTenantId(), envelope.GetSubject(), envelope.GetPartitionKey())
	}
	if !envelope.GetTime().AsTime().Equal(at) {
		t.Fatalf("time = %v", envelope.GetTime().AsTime())
	}
	// publish_domain_event admits a no-organization entry from a user-scoped
	// transaction only when this is that user (migration 36).
	if envelope.GetActorPrincipalId() != entry.ActorID {
		t.Fatalf("actor principal = %q, want the entry's actor %q", envelope.GetActorPrincipalId(), entry.ActorID)
	}
	if envelope.GetId() == entry.ID || envelope.GetId() != AuditJournalEventID(entry.ID) {
		t.Fatalf("journal id %q must be derived from, and differ from, the entry id %q", envelope.GetId(), entry.ID)
	}
	if _, err := uuid.Parse(envelope.GetId()); err != nil {
		t.Fatalf("the journal id is a uuid: %v", err)
	}
	if AuditJournalEventID(entry.ID) != AuditJournalEventID(entry.ID) || AuditJournalEventID(entry.ID) == AuditJournalEventID(uuid.NewString()) {
		t.Fatal("the journal id is deterministic per entry")
	}

	back, err := AuditEntryFromJournal(envelope.GetData())
	if err != nil {
		t.Fatal(err)
	}
	if back.Payload["amount"] != json.Number("12345678901234567890.50") {
		t.Fatalf("a number lost its text: %#v", back.Payload["amount"])
	}
	back.Payload, entry.Payload = nil, nil
	if back.ID != entry.ID || back.EventType != entry.EventType || back.SchemaVersion != 2 ||
		back.ActorID != entry.ActorID || back.ActorType != entry.ActorType || back.Resource != entry.Resource ||
		back.ResourceID != entry.ResourceID || back.OrgID != entry.OrgID || back.IPAddress != entry.IPAddress ||
		back.ClientID != entry.ClientID || back.IsImpersonated != true || back.ImpersonatedBy != entry.ImpersonatedBy ||
		!back.CreatedAt.Equal(at) {
		t.Fatalf("entry did not round-trip:\n got %+v\nwant %+v", back, entry)
	}
}

func TestAuditJournalEnvelopeOfAnEntryWithNoOrganizationHasNoTenant(t *testing.T) {
	envelope, err := NewAuditJournalEnvelope(AuditEntry{ID: uuid.NewString(), EventType: EventAuthLogin, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.GetTenantId() != "" {
		t.Fatalf("tenant = %q", envelope.GetTenantId())
	}
	var record map[string]any
	if err := json.Unmarshal(envelope.GetData(), &record); err != nil {
		t.Fatal(err)
	}
	if _, present := record["organization_id"]; present {
		t.Fatal("an entry with no organization names none")
	}
	if record["schema_version"] != float64(1) {
		t.Fatalf("an entry with no registered version is version 1, as audit_events records it: %v", record["schema_version"])
	}
	back, err := AuditEntryFromJournal(envelope.GetData())
	if err != nil || back.OrgID != "" {
		t.Fatalf("back = %+v %v", back, err)
	}
}

func TestAuditJournalEnvelopeRespectsTheDataLimit(t *testing.T) {
	entry := func(size int) AuditEntry {
		return AuditEntry{ID: uuid.NewString(), EventType: EventDocumentRead, CreatedAt: time.Now(), Payload: map[string]any{"blob": strings.Repeat("a", size)}}
	}
	if _, err := NewAuditJournalEnvelope(entry(maxAuditJournalData - 1024)); err != nil {
		t.Fatalf("an entry under the limit is published: %v", err)
	}
	_, err := NewAuditJournalEnvelope(entry(maxAuditJournalData))
	if !errors.Is(err, ErrAuditEntryTooLarge) {
		t.Fatalf("an entry over the limit = %v, want ErrAuditEntryTooLarge", err)
	}
	if _, err := NewAuditJournalEnvelope(AuditEntry{}); err == nil {
		t.Fatal("an entry with no id has no journal event")
	}
	if _, err := AuditEntryFromJournal([]byte(`{}`)); err == nil {
		t.Fatal("a journal record that names no entry is refused")
	}
}

// The warehouse backend replaces the audit_events write with the journal event,
// in the caller's transaction, and keeps the webhook publication for an
// organization-scoped external type exactly as it is.
func TestWarehouseBackendPublishesTheJournalInsteadOfAppending(t *testing.T) {
	service, store, transport, warehouse := newWarehouseAuditService(t)
	if err := service.EmitTx(t.Context(), AuditEntry{EventType: EventUserCreated, OrgID: auditServiceOrg, ActorType: ActorTypeSystem, Resource: "user"}); err != nil {
		t.Fatal(err)
	}
	if got := transport.types(); len(got) != 2 || got[0] != AuditRecordedEventType || got[1] != string(EventUserCreated) {
		t.Fatalf("published %v, want the journal event then the webhook event", got)
	}
	if len(store.inserted) != 0 || len(warehouse.appended) != 0 {
		t.Fatalf("an entry was appended: audit_events %d, warehouse %d; the write path publishes only", len(store.inserted), len(warehouse.appended))
	}
	journal, webhook := transport.published[0], transport.published[1]
	if journal.GetTenantId() != auditServiceOrg || webhook.GetTenantId() != auditServiceOrg {
		t.Fatalf("tenants %q %q", journal.GetTenantId(), webhook.GetTenantId())
	}
	if webhook.GetId() != journal.GetSubject() {
		t.Fatalf("the webhook event keeps the entry id (%q); the journal event names that entry (%q)", webhook.GetId(), journal.GetSubject())
	}
	if journal.GetId() == webhook.GetId() {
		t.Fatal("the journal event and the webhook event cannot share an id: an id is unique in the journal")
	}
	if len(store.scopes) != 0 {
		t.Fatalf("EmitTx opened a transaction: %v", store.scopes)
	}
}

// An entry with no organization is journaled too — it has no webhook event, and
// under the warehouse the journal is the only record it gets.
func TestWarehouseBackendJournalsAnEntryWithNoOrganization(t *testing.T) {
	service, store, transport, _ := newWarehouseAuditService(t)
	service.Emit(t.Context(), AuditEntry{EventType: EventAuthLogin, ActorType: ActorTypeSystem, Resource: "session"})
	if got := transport.types(); len(got) != 1 || got[0] != AuditRecordedEventType {
		t.Fatalf("published %v, want the journal event alone", got)
	}
	if transport.published[0].GetTenantId() != "" {
		t.Fatalf("tenant = %q", transport.published[0].GetTenantId())
	}
	if len(store.scopes) != 1 || store.scopes[0] != "control-plane" {
		t.Fatalf("a no-organization entry is published on the control plane's transaction, got %v", store.scopes)
	}
}

// Every audit write goes the same way, so a type that is not external, and one
// no catalog knows, are journaled as well: the journal is the record, the
// webhook event is only a notification.
func TestWarehouseBackendJournalsEveryType(t *testing.T) {
	service, _, transport, _ := newWarehouseAuditService(t)
	for _, eventType := range []EventType{EventUserCreated, EventAuthLogin, "example.item.closed"} {
		if err := service.EmitTx(t.Context(), AuditEntry{EventType: eventType, OrgID: auditServiceOrg}); err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
	}
	journaled := 0
	for _, e := range transport.published {
		if e.GetType() == AuditRecordedEventType {
			journaled++
		}
	}
	if journaled != 3 {
		t.Fatalf("journal events = %d, want one per entry: %v", journaled, transport.types())
	}
}

// A retried emit collapses to one journal event: the idempotency guard runs
// before the journal publication, as it runs before the append.
func TestWarehouseBackendIdempotencyGuardsTheJournal(t *testing.T) {
	service, _, transport, _ := newWarehouseAuditService(t)
	entry := AuditEntry{EventType: EventUserCreated, OrgID: auditServiceOrg, IdempotencyKey: "once"}
	for range 2 {
		if err := service.EmitTx(t.Context(), entry); err != nil {
			t.Fatal(err)
		}
	}
	if got := transport.types(); len(got) != 2 {
		t.Fatalf("published %v, want one journal event and one webhook event for two emits", got)
	}
}

// An entry that cannot be journaled fails the emit, and the webhook event is not
// published either: the caller's transaction rolls back with both.
func TestWarehouseBackendFailsClosedOnAnEntryTooLargeToJournal(t *testing.T) {
	service, _, transport, _ := newWarehouseAuditService(t)
	err := service.EmitTx(t.Context(), AuditEntry{
		EventType: EventUserCreated, OrgID: auditServiceOrg,
		Payload: map[string]any{"blob": strings.Repeat("a", maxAuditJournalData)},
	})
	if !errors.Is(err, ErrAuditEntryTooLarge) {
		t.Fatalf("EmitTx = %v, want ErrAuditEntryTooLarge", err)
	}
	if len(transport.published) != 0 {
		t.Fatalf("published %v after a refused entry", transport.types())
	}
}

func TestNewAuditServiceUnderTheWarehouseRefusesWhatCannotWork(t *testing.T) {
	store := &auditServiceStore{}
	transport := &journalTransport{}
	for name, cfg := range map[string]AuditServiceConfig{
		"no warehouse storage":       {Backend: AuditBackendWarehouse, Store: store, Producer: silentProducer{}, Transport: transport},
		"no transport":               {Backend: AuditBackendWarehouse, Store: store, Producer: silentProducer{}, Warehouse: &recordingStorage{}},
		"the external tee":           {Backend: AuditBackendWarehouse, Store: store, Producer: silentProducer{}, Transport: transport, Warehouse: &recordingStorage{}, ExternalTee: true},
		"a warehouse under postgres": {Backend: AuditBackendPostgres, Store: store, Producer: silentProducer{}, Transport: transport, Warehouse: &recordingStorage{}},
	} {
		if _, err := NewAuditService(cfg); err == nil {
			t.Errorf("%s: NewAuditService succeeded", name)
		}
	}
}

// Under the warehouse the Service's reads go to the warehouse, with the
// event-type facts the warehouse cannot join itself.
func TestWarehouseBackendReadsFromTheWarehouseWithTheTypeIndex(t *testing.T) {
	service, store, _, warehouse := newWarehouseAuditService(t)
	store.types = []AuditEventTypeRow{{Name: string(EventAuthLogin), Category: "auth", Namespace: "saas"}}

	if _, _, err := service.List(t.Context(), AuditQuery{OrgID: auditServiceOrg}); err != nil {
		t.Fatal(err)
	}
	if warehouse.reads[0].Types != nil {
		t.Fatal("a read that filters on neither category nor namespace needs no index")
	}
	if _, _, err := service.List(t.Context(), AuditQuery{OrgID: auditServiceOrg, Category: "auth"}); err != nil {
		t.Fatal(err)
	}
	facts, ok := warehouse.reads[1].Types[string(EventAuthLogin)]
	if !ok || facts.Category != "auth" || facts.Namespace != "saas" {
		t.Fatalf("type index = %v", warehouse.reads[1].Types)
	}
	if _, err := service.Aggregate(t.Context(), AuditQuery{}, AuditAggregationSpec{GroupBy: []string{"category"}}); err != nil {
		t.Fatal(err)
	}
	if !warehouse.aggregates[0].Scope.Platform() || warehouse.aggregates[0].Types == nil {
		t.Fatalf("aggregate read = %+v", warehouse.aggregates[0])
	}
	if got := store.scopes; len(got) != 2 || got[0] != "org:"+auditServiceOrg || got[1] != "control-plane" {
		t.Fatalf("the index is read in the read's own scope: %v", got)
	}
}

// The resource-grant check runs in the organization's Postgres transaction, and
// the aggregate is read from the warehouse only after it passes.
func TestWarehouseBackendChecksBeforeItReads(t *testing.T) {
	service, store, _, warehouse := newWarehouseAuditService(t)
	q := AuditQuery{OrgID: auditServiceOrg, Resource: "datasource", ResourceID: "s-1", EventType: "t"}

	if _, err := service.AggregateAuthorized(t.Context(), q, AuditAggregationSpec{}, func(context.Context) error { return errors.New("denied") }); err == nil {
		t.Fatal("a refused check must refuse the read")
	}
	if len(warehouse.aggregates) != 0 {
		t.Fatal("a refused read reached the warehouse")
	}
	if _, err := service.AggregateAuthorized(t.Context(), q, AuditAggregationSpec{}, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(warehouse.aggregates) != 1 || len(store.scopes) != 2 {
		t.Fatalf("warehouse reads %d, transactions %v", len(warehouse.aggregates), store.scopes)
	}
}

// The journal type is internal: the relay marks it published with no fan-out, a
// replay skips it, and no subscription pattern may match it.
func TestAuditRecordedEventTypeIsInternal(t *testing.T) {
	if !eventcatalog.IsInternalPublished(AuditRecordedEventType) {
		t.Fatalf("%s must be an internal published type", AuditRecordedEventType)
	}
	if eventcatalog.IsExternalPublished(AuditRecordedEventType) {
		t.Fatalf("%s must never be eligible for an outbound webhook", AuditRecordedEventType)
	}
	found := false
	for _, internal := range eventcatalog.InternalPublishedTypes() {
		found = found || internal == AuditRecordedEventType
	}
	if !found {
		t.Fatal("InternalPublishedTypes is what the subscribe gate matches patterns against; the journal type is missing from it")
	}
	if eventcatalog.Namespace(AuditRecordedEventType) != auditEventsNamespace {
		t.Fatalf("namespace = %q, want the platform namespace, which no module may subscribe to", eventcatalog.Namespace(AuditRecordedEventType))
	}
	for _, pattern := range []string{AuditRecordedEventType, "saas.audit.*", "saas.*"} {
		if !events.Matches(pattern, AuditRecordedEventType) {
			t.Errorf("pattern %q should match the journal type, so the subscribe gate refuses it", pattern)
		}
	}
}

// The journal type is a published type and not an audit event: it has no audit
// registry entry, so no audit entry can ever be written under it.
func TestAuditRecordedEventTypeIsNotAnAuditEventType(t *testing.T) {
	if _, registered := LookupAuditEvent(EventType(AuditRecordedEventType)); registered {
		t.Fatalf("%s must not be an audit event type", AuditRecordedEventType)
	}
}

// A storage that cannot compute the percentile metric refuses it, and the
// service answers that as the caller's invalid request, on both read paths.
func TestWarehouseBackendAnswersARefusedPercentileAsInvalidArgument(t *testing.T) {
	service, _, _, warehouse := newWarehouseAuditService(t)
	warehouse.refusePercentile = true
	spec := AuditAggregationSpec{Metrics: []AuditMetric{{Op: "percentile", Field: "payload:duration_ms", Percentile: 0.5}}}

	_, err := service.Aggregate(t.Context(), AuditQuery{OrgID: auditServiceOrg}, spec)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Aggregate = %v, want InvalidArgument", err)
	}
	q := AuditQuery{OrgID: auditServiceOrg, Resource: "datasource", ResourceID: "s-1", EventType: "t"}
	_, err = service.AggregateAuthorized(t.Context(), q, spec, func(context.Context) error { return nil })
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("AggregateAuthorized = %v, want InvalidArgument", err)
	}
	if _, err := service.Aggregate(t.Context(), AuditQuery{OrgID: auditServiceOrg}, AuditAggregationSpec{}); err != nil {
		t.Fatalf("every other metric is served: %v", err)
	}
	if !spec.HasPercentile() || (AuditAggregationSpec{}).HasPercentile() {
		t.Fatal("HasPercentile")
	}
}
