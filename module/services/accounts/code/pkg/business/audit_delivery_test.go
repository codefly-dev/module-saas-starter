package business_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// deliveryStorage is the warehouse of a test: it keeps every append it is given, as
// an at-least-once store does, and every read returns each entry once, by id.
type deliveryStorage struct {
	appended []business.AuditEntry
	failOn   string // an entry id whose append fails
}

func (d *deliveryStorage) Append(_ context.Context, entry business.AuditEntry) error {
	if entry.ID == d.failOn {
		return errors.New("warehouse unavailable")
	}
	d.appended = append(d.appended, entry)
	return nil
}

func (d *deliveryStorage) List(context.Context, business.AuditRead) ([]business.AuditEntry, string, error) {
	seen := map[string]business.AuditEntry{}
	for _, entry := range d.appended {
		seen[entry.ID] = entry
	}
	out := make([]business.AuditEntry, 0, len(seen))
	for _, entry := range seen {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, "", nil
}

func (d *deliveryStorage) Aggregate(context.Context, business.AuditRead, business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	return nil, nil
}

// rangeJournalStore answers the cross-tenant journal from rows it holds, with the
// real store's rules: after the position, up to the end, of the types, a page at a
// time, and only below the frontier.
type rangeJournalStore struct {
	fakeTxStore
	rows     []business.JournalRow
	frontier uint64 // the oldest transaction still running
}

func (r *rangeJournalStore) matching(q business.JournalPageQuery) []business.JournalRow {
	var out []business.JournalRow
	for _, row := range r.rows {
		if !q.After.Less(row.Position) || row.Position.Xact >= r.frontier {
			continue
		}
		if q.Through != nil && q.Through.Less(row.Position) {
			continue
		}
		for _, t := range q.Types {
			if t == row.Type {
				out = append(out, row)
				break
			}
		}
	}
	return out
}

func (r *rangeJournalStore) ReadJournalPage(_ context.Context, q business.JournalPageQuery) ([]business.JournalRow, error) {
	rows := r.matching(q)
	if len(rows) > q.Limit {
		rows = rows[:q.Limit]
	}
	return rows, nil
}

func (r *rangeJournalStore) CountJournalRange(_ context.Context, q business.JournalPageQuery, limit int) (int, error) {
	return min(len(r.matching(q)), limit), nil
}

func (r *rangeJournalStore) JournalPositionVisible(_ context.Context, pos business.JournalPosition) (bool, error) {
	return pos.Xact < r.frontier, nil
}

const deliveryTenant = "55555555-5555-5555-5555-555555555555"

// auditJournalRows are n audit records at transaction 10, seq 10, 20, ..., 10n; row i belongs to
// the organization orgs[i%len(orgs)].
func auditJournalRows(t *testing.T, n int, orgs ...string) []business.JournalRow {
	t.Helper()
	rows := make([]business.JournalRow, 0, n)
	for i := 1; i <= n; i++ {
		org := orgs[i%len(orgs)]
		envelope, err := business.NewAuditJournalEnvelope(business.AuditEntry{
			ID: uuid.NewString(), ActorID: moduleUserA, ActorType: business.ActorTypeUser,
			EventType: business.EventMFATOTPSetupStarted, Resource: "mfa_device", ResourceID: fmt.Sprint(i),
			OrgID: org, Payload: map[string]any{"n": i}, CreatedAt: time.Date(2026, 10, 13, 0, 0, i, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, business.JournalRow{
			Position: business.JournalPosition{Xact: 10, Seq: int64(i) * 10}, EventID: envelope.Id, Type: envelope.Type,
			TenantID: org, ContentType: envelope.Datacontenttype, Data: envelope.Data,
		})
	}
	return rows
}

func positionOf(row business.JournalRow) string { return row.Position.String() }

func deliveryService(t *testing.T, store *rangeJournalStore, storage business.AuditStorage) (*business.Service, *journalRecorder) {
	t.Helper()
	svc := newModuleServiceWithStore(t, store, &fakeJobBackend{}, true)
	transport := &journalRecorder{}
	audit, err := business.NewAuditService(business.AuditServiceConfig{
		Backend: business.AuditBackendWarehouse, Store: store, Producer: idleProducer{},
		Transport: transport, Warehouse: storage,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAuditService(audit)
	return svc, transport
}

func admitted(from string) context.Context {
	return receipts.WithEffect(context.Background(), receipts.Effect{
		ID: business.AuditDeliveryEffectID(from), Tenant: deliveryTenant,
		Method: business.DeliverAuditEventsMethod, RequestDigest: []byte("digest"),
	})
}

type commitSpy struct {
	calls  int
	result business.AuditDeliveryResult
	err    error
}

func (c *commitSpy) commit(_ context.Context, r business.AuditDeliveryResult) error {
	c.calls++
	c.result = r
	return c.err
}

func TestDeliverAuditEventsAppendsTheRangeWithTheRowsOrganization(t *testing.T) {
	audit := auditJournalRows(t, 5, moduleTenantA, moduleTenantB, "") // organizations: B, "", A, B, ""
	// A row of another type inside the range is not an audit record.
	other := business.JournalRow{Position: business.JournalPosition{Xact: 10, Seq: 35}, EventID: uuid.NewString(), Type: "saas.test.other", Data: []byte(`{}`)}
	all := append(append(append([]business.JournalRow{}, audit[:3]...), other), audit[3:]...)
	store := &rangeJournalStore{rows: all, frontier: 11}
	storage := &deliveryStorage{}
	svc, transport := deliveryService(t, store, storage)

	// (record 1, record 4]: records 2, 3 and 4, and not the other row between 3 and 4.
	from, to := positionOf(audit[0]), positionOf(audit[3])
	spy := &commitSpy{}
	result, err := svc.DeliverAuditEvents(admitted(from), from, to, spy.commit)
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 3 || spy.calls != 1 || spy.result != result || len(storage.appended) != 3 {
		t.Fatalf("delivered %+v, commits %d, appended %d", result, spy.calls, len(storage.appended))
	}
	for i, want := range audit[1:4] {
		if storage.appended[i].ID != entryIDOf(t, want) {
			t.Fatalf("appended[%d] = %s, want record %d", i, storage.appended[i].ID, i+2)
		}
		// The organization is the row's, "" for the row that has none.
		if storage.appended[i].OrgID != want.TenantID {
			t.Fatalf("appended[%d] organization %q, want the row's %q", i, storage.appended[i].OrgID, want.TenantID)
		}
	}
	if storage.appended[0].OrgID != "" || storage.appended[1].OrgID != moduleTenantA || storage.appended[2].OrgID != moduleTenantB {
		t.Fatalf("organizations %q %q %q", storage.appended[0].OrgID, storage.appended[1].OrgID, storage.appended[2].OrgID)
	}
	if len(transport.published) != 0 {
		t.Fatalf("delivering the audit trail wrote to it: %d events published", len(transport.published))
	}
}

func entryIDOf(t *testing.T, row business.JournalRow) string {
	t.Helper()
	entry, err := business.AuditEntryFromJournal(row.Data)
	if err != nil {
		t.Fatal(err)
	}
	return entry.ID
}

func TestDeliverAuditEventsFromTheStartAndAnEmptyRange(t *testing.T) {
	rows := auditJournalRows(t, 3, moduleTenantA)
	store := &rangeJournalStore{rows: rows, frontier: 11}
	storage := &deliveryStorage{}
	svc, _ := deliveryService(t, store, storage)

	spy := &commitSpy{}
	result, err := svc.DeliverAuditEvents(admitted(business.JournalStart), business.JournalStart, positionOf(rows[2]), spy.commit)
	if err != nil || result.Delivered != 3 || len(storage.appended) != 3 {
		t.Fatalf("from the start: %+v, %v, appended %d", result, err, len(storage.appended))
	}
	if business.AuditDeliveryEffectID(business.JournalStart) != "saas.audit.recorded.v1/start" {
		t.Fatalf("effect id = %q", business.AuditDeliveryEffectID(business.JournalStart))
	}

	// A range with no audit record still commits its receipt.
	storage.appended, spy.calls = nil, 0
	from := positionOf(rows[2])
	later := business.JournalPosition{Xact: 10, Seq: 999}
	result, err = svc.DeliverAuditEvents(admitted(from), from, later.String(), spy.commit)
	if err != nil || result.Delivered != 0 || spy.calls != 1 || len(storage.appended) != 0 {
		t.Fatalf("empty range: %+v, %v, commits %d", result, err, spy.calls)
	}
}

func TestDeliverAuditEventsRefusesWhatItIsNotAsked(t *testing.T) {
	rows := auditJournalRows(t, 3, moduleTenantA)
	store := &rangeJournalStore{rows: rows, frontier: 11}
	storage := &deliveryStorage{}
	svc, _ := deliveryService(t, store, storage)
	from, to := positionOf(rows[0]), positionOf(rows[2])
	spy := &commitSpy{}

	for name, tc := range map[string]struct {
		ctx      context.Context
		from, to string
		code     codes.Code
	}{
		"no admitted effect":     {context.Background(), from, to, codes.FailedPrecondition},
		"another range's effect": {admitted(positionOf(rows[1])), from, to, codes.FailedPrecondition},
		"another method's effect": {receipts.WithEffect(context.Background(), receipts.Effect{
			ID: business.AuditDeliveryEffectID(from), Tenant: deliveryTenant, Method: business.ReconcileSourceMethod, RequestDigest: []byte("d")}), from, to, codes.FailedPrecondition},
		"an effect with no tenant": {receipts.WithEffect(context.Background(), receipts.Effect{
			ID: business.AuditDeliveryEffectID(from), Method: business.DeliverAuditEventsMethod, RequestDigest: []byte("d")}), from, to, codes.FailedPrecondition},
		"an empty from":          {admitted(""), "", to, codes.InvalidArgument},
		"a malformed from":       {admitted("v1.1"), "v1.1", to, codes.InvalidArgument},
		"a malformed to":         {admitted(from), from, "v1.x.2", codes.InvalidArgument},
		"an empty to":            {admitted(from), from, "", codes.InvalidArgument},
		"start as to":            {admitted(from), from, business.JournalStart, codes.InvalidArgument},
		"to equal to from":       {admitted(from), from, from, codes.InvalidArgument},
		"to before from":         {admitted(to), to, from, codes.InvalidArgument},
		"an end not yet visible": {admitted(from), from, business.JournalPosition{Xact: 11, Seq: 1}.String(), codes.FailedPrecondition},
	} {
		if _, err := svc.DeliverAuditEvents(tc.ctx, tc.from, tc.to, spy.commit); status.Code(err) != tc.code {
			t.Errorf("%s: %v, want %s", name, err, tc.code)
		}
	}
	if _, err := svc.DeliverAuditEvents(admitted(from), from, to, nil); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("no commit: %v", err)
	}
	if len(storage.appended) != 0 || spy.calls != 0 {
		t.Fatalf("a refused delivery appended %d and committed %d", len(storage.appended), spy.calls)
	}
}

func TestDeliverAuditEventsRefusesAnEndThatIsNotVisibleWithTheReason(t *testing.T) {
	rows := auditJournalRows(t, 2, moduleTenantA)
	store := &rangeJournalStore{rows: rows, frontier: 10} // transaction 10 may still be running
	svc, _ := deliveryService(t, store, &deliveryStorage{})
	_, err := svc.DeliverAuditEvents(admitted(business.JournalStart), business.JournalStart, positionOf(rows[1]), (&commitSpy{}).commit)
	requireCode(t, err, codes.FailedPrecondition)
	requireReason(t, err, business.AuditDeliveryRangeNotVisibleReason)
}

func requireReason(t *testing.T, err error, reason string) {
	t.Helper()
	for _, d := range status.Convert(err).Details() {
		if info, ok := d.(interface{ GetReason() string }); ok && info.GetReason() == reason {
			return
		}
	}
	t.Fatalf("error %v does not carry reason %s", err, reason)
}

func TestDeliverAuditEventsCapsTheRange(t *testing.T) {
	rows := auditJournalRows(t, business.AuditDeliveryMaxRange+1, moduleTenantA)
	store := &rangeJournalStore{rows: rows, frontier: 11}
	storage := &deliveryStorage{}
	svc, _ := deliveryService(t, store, storage)
	spy := &commitSpy{}

	// 501 records: refused before anything is appended.
	_, err := svc.DeliverAuditEvents(admitted(business.JournalStart), business.JournalStart, positionOf(rows[len(rows)-1]), spy.commit)
	requireCode(t, err, codes.InvalidArgument)
	requireReason(t, err, business.AuditDeliveryRangeTooWideReason)
	if len(storage.appended) != 0 || spy.calls != 0 {
		t.Fatalf("a range too wide appended %d and committed %d", len(storage.appended), spy.calls)
	}

	// Exactly 500, read in several pages, are all delivered once.
	result, err := svc.DeliverAuditEvents(admitted(business.JournalStart), business.JournalStart, positionOf(rows[len(rows)-2]), spy.commit)
	if err != nil || result.Delivered != business.AuditDeliveryMaxRange || len(storage.appended) != business.AuditDeliveryMaxRange {
		t.Fatalf("a full range: %+v, %v, appended %d", result, err, len(storage.appended))
	}
}

func TestDeliverAuditEventsTakesTheOrganizationFromTheRowNeverTheRecord(t *testing.T) {
	rows := auditJournalRows(t, 3, moduleTenantA)
	// The record in row 2 names organization B; the row says A.
	entry, err := business.AuditEntryFromJournal(rows[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	entry.OrgID = moduleTenantB
	forged, err := business.NewAuditJournalEnvelope(entry)
	if err != nil {
		t.Fatal(err)
	}
	rows[1].Data = forged.Data
	store := &rangeJournalStore{rows: rows, frontier: 11}
	storage := &deliveryStorage{}
	svc, _ := deliveryService(t, store, storage)

	spy := &commitSpy{}
	_, err = svc.DeliverAuditEvents(admitted(business.JournalStart), business.JournalStart, positionOf(rows[2]), spy.commit)
	requireCode(t, err, codes.FailedPrecondition)
	requireReason(t, err, business.AuditDeliveryRecordRefusedReason)
	if spy.calls != 0 {
		t.Fatal("a refused record was committed")
	}
	for _, appended := range storage.appended {
		if appended.OrgID == moduleTenantB {
			t.Fatal("the record's organization reached the warehouse")
		}
	}
}

func TestDeliverAuditEventsFailureLeavesNoReceiptAndAReplayAppendsAtLeastOnce(t *testing.T) {
	rows := auditJournalRows(t, 4, moduleTenantA)
	store := &rangeJournalStore{rows: rows, frontier: 11}
	storage := &deliveryStorage{failOn: entryIDOf(t, rows[2])}
	svc, _ := deliveryService(t, store, storage)
	to := positionOf(rows[3])

	spy := &commitSpy{}
	_, err := svc.DeliverAuditEvents(admitted(business.JournalStart), business.JournalStart, to, spy.commit)
	requireCode(t, err, codes.Unavailable)
	if spy.calls != 0 || len(storage.appended) != 2 {
		t.Fatalf("commits %d, appended %d: the records before the failure are appended and no receipt is written", spy.calls, len(storage.appended))
	}

	// The same range again, once the warehouse is back: the records before the
	// failure are appended a second time, and every read returns each once.
	storage.failOn = ""
	result, err := svc.DeliverAuditEvents(admitted(business.JournalStart), business.JournalStart, to, spy.commit)
	if err != nil || result.Delivered != 4 || spy.calls != 1 || len(storage.appended) != 6 {
		t.Fatalf("retry: %+v, %v, commits %d, appended %d", result, err, spy.calls, len(storage.appended))
	}
	listed, _, _ := storage.List(context.Background(), business.AuditRead{})
	if len(listed) != 4 {
		t.Fatalf("a read returns %d entries, want each of the 4 once", len(listed))
	}

	// A commit that fails is not a delivery.
	spy.err = errors.New("receipt store down")
	_, err = svc.DeliverAuditEvents(admitted(business.JournalStart), business.JournalStart, to, spy.commit)
	requireCode(t, err, codes.Unavailable)
}

func TestOverlappingRangesAppendAtLeastOnceAndReadsDeduplicate(t *testing.T) {
	rows := auditJournalRows(t, 5, moduleTenantA)
	store := &rangeJournalStore{rows: rows, frontier: 11}
	storage := &deliveryStorage{}
	svc, _ := deliveryService(t, store, storage)
	spy := &commitSpy{}

	// (start, 3] and (2, 5] overlap on record 3.
	if _, err := svc.DeliverAuditEvents(admitted(business.JournalStart), business.JournalStart, positionOf(rows[2]), spy.commit); err != nil {
		t.Fatal(err)
	}
	from := positionOf(rows[1])
	if _, err := svc.DeliverAuditEvents(admitted(from), from, positionOf(rows[4]), spy.commit); err != nil {
		t.Fatal(err)
	}
	if len(storage.appended) != 6 {
		t.Fatalf("appended %d, want 3 + 3 with record 3 twice", len(storage.appended))
	}
	listed, _, _ := storage.List(context.Background(), business.AuditRead{})
	if len(listed) != 5 {
		t.Fatalf("a read returns %d entries, want the 5 once each", len(listed))
	}
}

// A host whose audit is Postgres has no audit record in the journal, and
// appending to its storage from here would write audit_events outside the writer's
// transaction: refused.
func TestDeliverAuditEventsRefusesAHostThatKeepsAuditInPostgres(t *testing.T) {
	rows := auditJournalRows(t, 2, moduleTenantA)
	store := &rangeJournalStore{rows: rows, frontier: 11}
	svc := newModuleServiceWithStore(t, store, &fakeJobBackend{}, true) // no AuditService wired
	_, err := svc.DeliverAuditEvents(admitted(business.JournalStart), business.JournalStart, positionOf(rows[1]), (&commitSpy{}).commit)
	requireCode(t, err, codes.FailedPrecondition)
	requireReason(t, err, business.AuditDeliveryNotWarehouseReason)

	postgres, err := business.NewAuditService(business.AuditServiceConfig{Backend: business.AuditBackendPostgres, Store: store, Producer: idleProducer{}})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAuditService(postgres)
	_, err = svc.DeliverAuditEvents(admitted(business.JournalStart), business.JournalStart, positionOf(rows[1]), (&commitSpy{}).commit)
	requireCode(t, err, codes.FailedPrecondition)
	requireReason(t, err, business.AuditDeliveryNotWarehouseReason)
}
