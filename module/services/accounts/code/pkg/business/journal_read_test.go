package business_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestJournalPositionEncoding(t *testing.T) {
	// The encoding is versioned text, one spelling per position.
	for _, p := range []business.JournalPosition{
		{Xact: 0, Seq: 7},
		{Xact: 1, Seq: 0},
		{Xact: 812, Seq: 99},
		{Xact: 1<<64 - 1, Seq: 1<<63 - 1},
	} {
		got, err := business.ParseJournalPosition(p.String())
		if err != nil || got != p {
			t.Errorf("round trip of %+v through %q = %+v, %v", p, p.String(), got, err)
		}
	}
	if (business.JournalPosition{}).String() != "" {
		t.Error("the zero position is the empty string")
	}
	for _, start := range []string{"", business.JournalStart} {
		if got, err := business.ParseJournalPosition(start); err != nil || !got.IsZero() {
			t.Errorf("%q is the beginning: %+v, %v", start, got, err)
		}
	}
	if business.JournalStart != "start" {
		t.Error("the start position is the literal \"start\"")
	}
	if (business.JournalPosition{Xact: 5, Seq: 9}).String() != "v1.5.9" {
		t.Errorf("the wire form is v1.<xact_id>.<seq>: %q", business.JournalPosition{Xact: 5, Seq: 9}.String())
	}
	for _, malformed := range []string{
		"garbage", "Start", "START", "start ", "starts", "1.2", "v1", "v1.", "v1.1", "v1.1.", "v1..1", "v1.1.2.3", "v2.1.2", "V1.1.2",
		"v1.-1.2", "v1.1.-2", "v1.+1.2", "v1.1.+2", "v1.01.2", "v1.1.02", "v1.0.0", " v1.1.2", "v1.1.2 ",
		"v1.1.2\n", "v1.0x1.2", "v1.1.9223372036854775808", "v1.18446744073709551616.1",
		"v1." + strings.Repeat("9", 200), "v1.1.2;DROP TABLE domain_events",
	} {
		_, err := business.ParseJournalPosition(malformed)
		requireCode(t, err, codes.InvalidArgument)
	}
	if !(business.JournalPosition{Xact: 1, Seq: 9}).Less(business.JournalPosition{Xact: 2, Seq: 1}) ||
		!(business.JournalPosition{Xact: 2, Seq: 1}).Less(business.JournalPosition{Xact: 2, Seq: 2}) ||
		(business.JournalPosition{Xact: 2, Seq: 2}).Less(business.JournalPosition{Xact: 2, Seq: 2}) {
		t.Error("positions order by transaction, then seq")
	}
}

// auditRow is a journal row of type saas.audit.recorded.v1 carrying a real audit
// entry, built by the publisher's own envelope.
func auditRow(t *testing.T, eventType business.EventType, actor string, payload map[string]any) business.JournalRow {
	t.Helper()
	entry := business.AuditEntry{
		ID: uuid.NewString(), ActorID: actor, ActorType: business.ActorTypeSystem,
		EventType: eventType, Resource: "module", ResourceID: "runtime",
		Payload: payload, CreatedAt: time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC),
	}
	envelope, err := business.NewAuditJournalEnvelope(entry)
	if err != nil {
		t.Fatal(err)
	}
	return business.JournalRow{
		EventID: envelope.Id, Type: envelope.Type, ContentType: envelope.Datacontenttype, Data: envelope.Data,
	}
}

// The delivery_machinery rule is one function. Each case is a row as the journal
// holds it, read from the caller's side: the caller is the module principal
// callerID, its delivery runs under binding delivery-binding.
func TestJournalRowIsDeliveryMachinery(t *testing.T) {
	const (
		callerID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		otherID  = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	delivery := map[string]struct{}{"delivery-binding": {}}
	exchange := func(binding any, outcome string) map[string]any {
		return map[string]any{
			"module_principal_id": callerID, "binding_kind": "operation", "binding_id": binding,
			"lookup": false, "outcome": outcome, "actor_principal_id": otherID,
		}
	}
	mint := func(binding any) map[string]any {
		return map[string]any{"prefix": "runtime", "tenant": "t", "binding_id": binding, "audience": "saas-audit"}
	}
	nonAudit := auditRow(t, business.EventModuleOperationContextMint, callerID, mint("delivery-binding"))
	nonAudit.Type = "saas.something.else.v1"

	for _, tc := range []struct {
		name     string
		row      business.JournalRow
		caller   string
		bindings map[string]struct{}
		want     bool
	}{
		{"operation mint for a delivery binding", auditRow(t, business.EventModuleOperationContextMint, callerID, mint("delivery-binding")), callerID, delivery, true},
		{"operation mint for another binding", auditRow(t, business.EventModuleOperationContextMint, callerID, mint("customer-binding")), callerID, delivery, false},
		{"operation mint with no binding", auditRow(t, business.EventModuleOperationContextMint, callerID, map[string]any{"prefix": "runtime"}), callerID, delivery, false},
		{"operation mint with a binding that is not text", auditRow(t, business.EventModuleOperationContextMint, callerID, mint(7)), callerID, delivery, false},
		{"operation mint and no delivery bindings named", auditRow(t, business.EventModuleOperationContextMint, callerID, mint("delivery-binding")), callerID, nil, false},
		{"exchange on a delivery binding, issued", auditRow(t, business.EventDelegatedAudienceExchange, otherID, exchange("delivery-binding", business.DelegatedAudienceExchangeIssued)), callerID, delivery, true},
		{"exchange on a delivery binding, refused", auditRow(t, business.EventDelegatedAudienceExchange, otherID, exchange("delivery-binding", business.DelegatedAudienceExchangeRefused)), callerID, delivery, true},
		{"exchange on another binding", auditRow(t, business.EventDelegatedAudienceExchange, otherID, exchange("customer-binding", business.DelegatedAudienceExchangeIssued)), callerID, delivery, false},
		{"exchange with a binding that is not text", auditRow(t, business.EventDelegatedAudienceExchange, otherID, exchange(true, business.DelegatedAudienceExchangeIssued)), callerID, delivery, false},
		{"exchange and no delivery bindings named", auditRow(t, business.EventDelegatedAudienceExchange, otherID, exchange("delivery-binding", business.DelegatedAudienceExchangeIssued)), callerID, map[string]struct{}{}, false},
		{"work context mint of the caller's own principal", auditRow(t, business.EventModuleWorkContextMint, callerID, map[string]any{"prefix": "runtime", "tenant": "t"}), callerID, delivery, true},
		{"work context mint of the caller's own principal, spelled in capitals", auditRow(t, business.EventModuleWorkContextMint, strings.ToUpper(callerID), map[string]any{"prefix": "runtime"}), callerID, nil, true},
		{"work context mint of another module's principal", auditRow(t, business.EventModuleWorkContextMint, otherID, map[string]any{"prefix": "other"}), callerID, delivery, false},
		{"work context mint and no caller", auditRow(t, business.EventModuleWorkContextMint, callerID, map[string]any{"prefix": "runtime"}), "", delivery, false},
		{"work context mint with no actor", auditRow(t, business.EventModuleWorkContextMint, "", map[string]any{"prefix": "runtime"}), callerID, delivery, false},
		{"an ordinary audit entry", auditRow(t, business.EventMFATOTPSetupStarted, callerID, map[string]any{"binding_id": "delivery-binding"}), callerID, delivery, false},
		{"a row that is not an audit record", nonAudit, callerID, delivery, false},
		{"a record that cannot be read", business.JournalRow{Type: business.AuditRecordedEventType, Data: []byte("not json")}, callerID, delivery, false},
		{"a record that names no entry", business.JournalRow{Type: business.AuditRecordedEventType, Data: []byte("{}")}, callerID, delivery, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := business.JournalRowIsDeliveryMachinery(tc.row, tc.caller, tc.bindings); got != tc.want {
				t.Fatalf("delivery_machinery = %v, want %v", got, tc.want)
			}
		})
	}
}

// journalStore answers the cross-tenant read from rows it holds and records what
// it was asked.
type journalStore struct {
	fakeTxStore
	rows    []business.JournalRow
	queries []business.JournalPageQuery
	err     error
}

func (j *journalStore) ReadJournalPage(_ context.Context, q business.JournalPageQuery) ([]business.JournalRow, error) {
	j.queries = append(j.queries, q)
	if j.err != nil {
		return nil, j.err
	}
	return j.rows, nil
}

func newJournalService(t *testing.T, store business.Store, crossTenant bool) *business.Service {
	t.Helper()
	return newModuleServiceWithStore(t, store, &fakeJobBackend{}, crossTenant)
}

// The read is the system-scoped gate of ModuleEmitAuditEvent: a principal bound to
// one tenant is refused before the store is asked anything, and so is a principal
// the registry does not name, and so is a caller naming none.
func TestModuleReadJournalRefusesWhoIsNotCrossTenant(t *testing.T) {
	store := &journalStore{rows: []business.JournalRow{{EventID: "e1", Type: "saas.audit.recorded.v1"}}}
	in := business.ModuleReadJournalInput{Types: []string{business.AuditRecordedEventType}}

	svc := newJournalService(t, store, false)
	_, err := svc.ModuleReadJournal(context.Background(), moduleCaller(), in)
	requireCode(t, err, codes.PermissionDenied)

	_, err = svc.ModuleReadJournal(context.Background(), business.ModuleCaller{PrincipalID: moduleUserA, BoundOrg: moduleTenantA}, in)
	requireCode(t, err, codes.PermissionDenied)

	_, err = svc.ModuleReadJournal(context.Background(), business.ModuleCaller{}, in)
	requireCode(t, err, codes.Unauthenticated)

	// A bad request from a principal that may not read is still a refusal of the
	// principal: the gate runs before the input is looked at.
	_, err = svc.ModuleReadJournal(context.Background(), moduleCaller(), business.ModuleReadJournalInput{After: "garbage"})
	requireCode(t, err, codes.PermissionDenied)

	if len(store.queries) != 0 {
		t.Fatalf("a refused caller reached the store: %+v", store.queries)
	}
}

func TestModuleReadJournalRefusesBadInput(t *testing.T) {
	store := &journalStore{}
	svc := newJournalService(t, store, true)
	audit := []string{business.AuditRecordedEventType}
	many := make([]string, 17)
	for i := range many {
		many[i] = "saas.test.type" + strings.Repeat("x", i)
	}
	bindings := make([]string, 65)
	for i := range bindings {
		bindings[i] = strings.Repeat("b", i+1)
	}
	for name, in := range map[string]business.ModuleReadJournalInput{
		"a malformed position":      {After: "v1.1", Types: audit},
		"a negative limit":          {Limit: -1, Types: audit},
		"no type":                   {},
		"a pattern, not a type":     {Types: []string{"saas.audit.*"}},
		"an uppercase type":         {Types: []string{"Saas.Audit"}},
		"a type that is one word":   {Types: []string{"audit"}},
		"too many types":            {Types: many},
		"an empty delivery binding": {Types: audit, DeliveryBindings: []string{""}},
		"too many bindings":         {Types: audit, DeliveryBindings: bindings},
		"an oversized binding":      {Types: audit, DeliveryBindings: []string{strings.Repeat("b", 257)}},
	} {
		_, err := svc.ModuleReadJournal(context.Background(), moduleCaller(), in)
		if err == nil {
			t.Errorf("%s was served", name)
			continue
		}
		requireCode(t, err, codes.InvalidArgument)
	}
	if len(store.queries) != 0 {
		t.Fatalf("a refused request reached the store: %+v", store.queries)
	}
}

func TestModuleReadJournalServesEntriesAndResumes(t *testing.T) {
	const caller = modulePrincSvc
	minted := auditRow(t, business.EventModuleOperationContextMint, caller, map[string]any{"binding_id": "delivery-binding"})
	minted.Position = business.JournalPosition{Xact: 40, Seq: 3}
	minted.OccurredAt = time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC)
	ordinary := auditRow(t, business.EventMFATOTPSetupStarted, moduleUserA, map[string]any{"n": 1})
	ordinary.Position = business.JournalPosition{Xact: 41, Seq: 2}
	ordinary.TenantID = moduleTenantA
	opaque := business.JournalRow{
		Position: business.JournalPosition{Xact: 41, Seq: 5}, EventID: uuid.NewString(), Type: "saas.test.opaque",
		ContentType: "application/octet-stream", Data: []byte{0xff, 0x00},
	}

	store := &journalStore{rows: []business.JournalRow{minted, ordinary, opaque}}
	svc := newJournalService(t, store, true)
	got, err := svc.ModuleReadJournal(context.Background(), moduleCaller(), business.ModuleReadJournalInput{
		After: "v1.39.1", Limit: 10_000, Types: []string{business.AuditRecordedEventType, "saas.test.opaque", business.AuditRecordedEventType},
		DeliveryBindings: []string{"delivery-binding"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// What the store was asked: the position decoded, the limit held to the page
	// maximum, the types exactly once each, a byte bound on the page.
	if len(store.queries) != 1 {
		t.Fatalf("queries = %+v", store.queries)
	}
	q := store.queries[0]
	if q.After != (business.JournalPosition{Xact: 39, Seq: 1}) || q.Through != nil ||
		q.Limit != business.ModuleJournalMaxPage || q.MaxBytes != business.ModuleJournalMaxPageBytes ||
		strings.Join(q.Types, ",") != business.AuditRecordedEventType+",saas.test.opaque" {
		t.Fatalf("query = %+v", q)
	}

	if len(got.Entries) != 3 || got.NextAfter != "v1.41.5" {
		t.Fatalf("entries = %d, next_after = %q", len(got.Entries), got.NextAfter)
	}
	first := got.Entries[0]
	if first.Position != "v1.40.3" || first.EventID != minted.EventID || first.Type != business.AuditRecordedEventType ||
		first.OrganizationID != "" || !first.DeliveryMachinery || !first.OccurredAt.Equal(minted.OccurredAt) {
		t.Fatalf("first = %+v", first)
	}
	if first.Data["event_type"] != string(business.EventModuleOperationContextMint) {
		t.Fatalf("data is the event's JSON object: %+v", first.Data)
	}
	if _, err := structpb.NewStruct(first.Data); err != nil {
		t.Fatalf("data does not fit a google.protobuf.Struct: %v", err)
	}
	if second := got.Entries[1]; second.OrganizationID != moduleTenantA || second.DeliveryMachinery {
		t.Fatalf("second = %+v", second)
	}
	if third := got.Entries[2]; len(third.Data) != 0 || third.DeliveryMachinery {
		t.Fatalf("an opaque payload is served as an empty object: %+v", third)
	}

	// An empty page resumes where it was asked from, not from the beginning.
	store.rows = nil
	empty, err := svc.ModuleReadJournal(context.Background(), moduleCaller(), business.ModuleReadJournalInput{After: "v1.39.1", Types: []string{"saas.test.opaque"}})
	if err != nil || len(empty.Entries) != 0 || empty.NextAfter != "v1.39.1" || store.queries[1].Limit != business.ModuleJournalDefaultPage {
		t.Fatalf("empty page = %+v, %v; query %+v", empty, err, store.queries[1])
	}
	// From the beginning, spelled either way: the same query, and an empty page
	// resumes where it was asked from.
	for _, start := range []string{"", business.JournalStart} {
		store.queries = nil
		none, err := svc.ModuleReadJournal(context.Background(), moduleCaller(), business.ModuleReadJournalInput{After: start, Types: []string{"saas.test.opaque"}})
		if err != nil || none.NextAfter != start || len(store.queries) != 1 || !store.queries[0].After.IsZero() {
			t.Fatalf("empty page from %q = %+v, %v; queries %+v", start, none, err, store.queries)
		}
	}
}
