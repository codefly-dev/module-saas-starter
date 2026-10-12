package business

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// recordingStorage is an AuditStorage that remembers what it was asked, so the
// AuditService is tested for which storage it reaches and in which scope.
type recordingStorage struct {
	appended   []AuditEntry
	reads      []AuditRead
	aggregates []AuditRead
	page       []AuditEntry
	appendErr  error
}

func (r *recordingStorage) Append(_ context.Context, entry AuditEntry) error {
	r.appended = append(r.appended, entry)
	return r.appendErr
}

func (r *recordingStorage) List(_ context.Context, read AuditRead) ([]AuditEntry, string, error) {
	r.reads = append(r.reads, read)
	return r.page, "", nil
}

func (r *recordingStorage) Aggregate(_ context.Context, read AuditRead, _ AuditAggregationSpec) ([]AuditAggregateBucket, error) {
	r.aggregates = append(r.aggregates, read)
	return []AuditAggregateBucket{{Key: "k", Count: 1}}, nil
}

// auditServiceStore is the Store an AuditService test runs on: transactions run
// inline and are named, and nothing needs a database.
type auditServiceStore struct {
	scopeStore
	reserved map[string]bool
	types    []AuditEventTypeRow
}

func (s *auditServiceStore) ListAuditEventTypes(context.Context) ([]AuditEventTypeRow, error) {
	return s.types, nil
}

func (s *auditServiceStore) GetDeclaredAuditEventType(context.Context, EventType) (*DeclaredAuditEventType, error) {
	return nil, nil
}

func (s *auditServiceStore) ReserveAuditIdempotency(_ context.Context, orgID, eventType, key string) (bool, error) {
	id := orgID + "|" + eventType + "|" + key
	if s.reserved[id] {
		return false, nil
	}
	if s.reserved == nil {
		s.reserved = map[string]bool{}
	}
	s.reserved[id] = true
	return true, nil
}

const auditServiceOrg = "11111111-1111-1111-1111-111111111111"

func TestAuditServiceUnderPostgresAppendsThroughItsStorage(t *testing.T) {
	store := &auditServiceStore{}
	transport := &recordingTransport{}
	service, err := NewAuditService(AuditServiceConfig{
		Backend: AuditBackendPostgres, Store: store, Producer: silentProducer{}, Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.Backend() != AuditBackendPostgres {
		t.Fatalf("backend = %q", service.Backend())
	}
	entry := AuditEntry{EventType: EventUserCreated, OrgID: auditServiceOrg, ActorType: ActorTypeSystem, Resource: "user"}
	if err := service.EmitTx(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	if len(store.inserted) != 1 || store.inserted[0].EventType != EventUserCreated {
		t.Fatalf("audit_events rows = %v, want the one entry", store.inserted)
	}
	if len(transport.published) != 1 {
		t.Fatalf("webhook publications = %d, want 1 for an org-scoped external type", len(transport.published))
	}
	// EmitTx joins the caller's transaction: it opens none of its own.
	if len(store.scopes) != 0 {
		t.Fatalf("EmitTx opened a transaction: %v", store.scopes)
	}

	// Emit has no caller transaction, so it opens the one its scope names: the
	// organization's, or the control plane's for an entry with no organization.
	service.Emit(t.Context(), entry)
	service.Emit(t.Context(), AuditEntry{EventType: EventAuthLogin, ActorType: ActorTypeSystem, Resource: "session"})
	if got := store.scopes; len(got) != 2 || got[0] != "org:"+auditServiceOrg || got[1] != "control-plane" {
		t.Fatalf("Emit transactions = %v", got)
	}
}

func TestAuditServiceEmitterAppendsToTheStorageItIsGiven(t *testing.T) {
	store := &auditServiceStore{}
	storage := &recordingStorage{}
	emitter, err := NewDurableAuditEmitter(store, silentProducer{}, WithAuditStorage(storage))
	if err != nil {
		t.Fatal(err)
	}
	if err := emitter.EmitTx(t.Context(), AuditEntry{EventType: EventUserCreated, OrgID: auditServiceOrg, ActorType: ActorTypeSystem}); err != nil {
		t.Fatal(err)
	}
	if len(storage.appended) != 1 || len(store.inserted) != 0 {
		t.Fatalf("appended to storage %d, to audit_events %d; want 1 and 0", len(storage.appended), len(store.inserted))
	}
	if storage.appended[0].ID == "" || storage.appended[0].CreatedAt.IsZero() {
		t.Fatalf("the emitter backfills id and time before appending: %+v", storage.appended[0])
	}
	// A failed append is the caller's failure, so its transaction rolls back.
	storage.appendErr = errors.New("boom")
	if err := emitter.EmitTx(t.Context(), AuditEntry{EventType: EventUserCreated, OrgID: auditServiceOrg}); err == nil {
		t.Fatal("a failed append must fail the emit")
	}
}

func TestAuditServiceIdempotencyStillGuardsTheAppend(t *testing.T) {
	store := &auditServiceStore{}
	storage := &recordingStorage{}
	emitter, err := NewDurableAuditEmitter(store, silentProducer{}, WithAuditStorage(storage))
	if err != nil {
		t.Fatal(err)
	}
	entry := AuditEntry{EventType: EventUserCreated, OrgID: auditServiceOrg, IdempotencyKey: "once"}
	for range 2 {
		if err := emitter.EmitTx(t.Context(), entry); err != nil {
			t.Fatal(err)
		}
	}
	if len(storage.appended) != 1 {
		t.Fatalf("appended %d, want 1: a retried emit collapses to one entry", len(storage.appended))
	}
}

func TestNewAuditServiceRefusesWhatItCannotBuild(t *testing.T) {
	store := &auditServiceStore{}
	for name, cfg := range map[string]AuditServiceConfig{
		"no store":        {Backend: AuditBackendPostgres, Producer: silentProducer{}},
		"unknown backend": {Backend: "carrier-pigeon", Store: store, Producer: silentProducer{}},
		"no producer":     {Backend: AuditBackendPostgres, Store: store},
	} {
		if _, err := NewAuditService(cfg); err == nil {
			t.Errorf("%s: NewAuditService succeeded", name)
		}
	}
}

func TestAuditServiceReadsInTheScopeOfTheQuery(t *testing.T) {
	store := &auditServiceStore{}
	service, err := NewAuditService(AuditServiceConfig{Backend: AuditBackendPostgres, Store: store, Producer: silentProducer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.List(t.Context(), AuditQuery{OrgID: auditServiceOrg}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Aggregate(t.Context(), AuditQuery{}, AuditAggregationSpec{}); err != nil {
		t.Fatal(err)
	}
	if got := store.scopes; len(got) != 2 || got[0] != "org:"+auditServiceOrg || got[1] != "control-plane" {
		t.Fatalf("read transactions = %v", got)
	}
}

func TestAuditServiceChecksAndReadsInOneTransaction(t *testing.T) {
	store := &auditServiceStore{}
	service, err := NewAuditService(AuditServiceConfig{Backend: AuditBackendPostgres, Store: store, Producer: silentProducer{}})
	if err != nil {
		t.Fatal(err)
	}
	q := AuditQuery{OrgID: auditServiceOrg, Resource: "datasource", ResourceID: "s-1", EventType: "t"}

	checked := 0
	out, err := service.AggregateAuthorized(t.Context(), q, AuditAggregationSpec{}, func(context.Context) error {
		checked++
		return nil
	})
	if err != nil || len(out) != 1 || checked != 1 {
		t.Fatalf("AggregateAuthorized = %v %v, checked %d", out, err, checked)
	}
	if len(store.scopes) != 1 {
		t.Fatalf("the check and the read opened %d transactions, want 1: %v", len(store.scopes), store.scopes)
	}

	denied := status.Error(codes.PermissionDenied, "no")
	store.aggregated = nil
	if _, err := service.AggregateAuthorized(t.Context(), q, AuditAggregationSpec{}, func(context.Context) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("a refused check = %v, want the refusal", err)
	}
	if len(store.aggregated) != 0 {
		t.Fatal("a refused read reached the store")
	}
}

// The Service's reads go through the AuditService it was given, not through the
// Store: the write and the read cannot name two different stores.
func TestServiceReadsThroughTheAuditServiceItIsGiven(t *testing.T) {
	store := &auditServiceStore{}
	storage := &recordingStorage{page: []AuditEntry{{ID: "a"}, {ID: "b"}}}
	service := &AuditService{backend: AuditBackendPostgres, store: store, storage: storage}
	svc, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAuditService(service)

	entries, _, total, err := svc.QueryAuditLog(t.Context(), AuditQuery{OrgID: auditServiceOrg})
	if err != nil || len(entries) != 2 || total != 2 {
		t.Fatalf("QueryAuditLog = %v %d %v", entries, total, err)
	}
	if len(store.queried) != 0 {
		t.Fatal("the Service read audit_events directly")
	}
	if len(storage.reads) != 1 || storage.reads[0].Scope.OrgID() != auditServiceOrg {
		t.Fatalf("storage reads = %+v", storage.reads)
	}
	if _, err := svc.AggregateAuditLog(t.Context(), AuditQuery{}, AuditAggregationSpec{}); err != nil {
		t.Fatal(err)
	}
	if len(storage.aggregates) != 1 || !storage.aggregates[0].Scope.Platform() {
		t.Fatalf("storage aggregates = %+v", storage.aggregates)
	}
	if _, _, _, err := svc.QueryAuditLog(t.Context(), AuditQuery{OrgID: auditServiceOrg, CollectionID: "c"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a collection filter without a reader = %v, want InvalidArgument", err)
	}
}

func TestServiceWithoutAnAuditServiceReadsAuditEvents(t *testing.T) {
	store := &auditServiceStore{}
	svc, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	entries, next, total, err := svc.QueryAuditLog(t.Context(), AuditQuery{OrgID: auditServiceOrg})
	if err != nil || len(entries) != 1 || next != "next" || total != 1 {
		t.Fatalf("QueryAuditLog = %v %q %d %v", entries, next, total, err)
	}
	if len(store.queried) != 1 || len(store.scopes) != 1 {
		t.Fatalf("store reads %d in %v", len(store.queried), store.scopes)
	}
}
