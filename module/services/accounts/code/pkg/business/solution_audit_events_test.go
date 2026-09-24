package business

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"accounts/pkg/eventcatalog"
)

// Admission rules for solution-declared audit event types, exercised without a
// database: the manifest parser, the additive-change rule, the stored-schema
// round trip, and admission through PutSolutionRegistration against an
// in-memory store that rolls a failed transaction back. The SQL itself is
// covered in solution_audit_events_integration_test.go.

func declaringManifest(id, events string) string {
	return `{"id":"` + id + `","dashboard":{"events":[` + events + `],"metrics":[],"dashboards":[]}}`
}

const exampleDeclaredEvent = `{"name":"created","type":"acme.item.created","description":"An item was created.",
	"fields":[{"name":"stage","kind":"enum","values":["draft","final"]},{"name":"score","kind":"number"},{"name":"count","kind":"int"}]}`

func TestParseDeclaredAuditEventTypes_DeclaresNothingWithoutFields(t *testing.T) {
	for name, manifest := range map[string]string{
		"no dashboard":     `{"id":"acme"}`,
		"fieldless events": declaringManifest("acme", `{"name":"login","type":"saas.auth.login"},{"name":"viewed","type":"acme.page.viewed"}`),
		"empty events":     declaringManifest("acme", ``),
	} {
		declared, err := ParseDeclaredAuditEventTypes("acme", manifest)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(declared) != 0 {
			t.Fatalf("%s: declared %v, want none — an event without fields binds an existing type", name, declared)
		}
	}
}

func TestParseDeclaredAuditEventTypes_ReadsTypedFields(t *testing.T) {
	declared, err := ParseDeclaredAuditEventTypes("acme", declaringManifest("acme",
		exampleDeclaredEvent+`,{"name":"closed","type":"acme.item.closed","fields":[]},{"name":"created_alias","type":"acme.item.created"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []DeclaredAuditEventType{
		{Type: "acme.item.closed", Namespace: "acme", SolutionID: "acme", Fields: []PayloadField{}},
		{
			Type: "acme.item.created", Namespace: "acme", SolutionID: "acme", Description: "An item was created.",
			Fields: []PayloadField{
				{Name: "count", Kind: FieldInt},
				{Name: "score", Kind: FieldNumber},
				{Name: "stage", Kind: FieldEnum, Enum: []string{"draft", "final"}},
			},
		},
	}
	if !reflect.DeepEqual(declared, want) {
		t.Fatalf("declared = %#v\nwant %#v", declared, want)
	}
}

func TestParseDeclaredAuditEventTypes_Rejections(t *testing.T) {
	field := func(fields string) string {
		return `{"name":"e","type":"acme.item.created","fields":[` + fields + `]}`
	}
	cases := map[string]string{
		"two-segment type":         `{"name":"e","type":"acme.created","fields":[]}`,
		"uppercase type":           `{"name":"e","type":"Acme.item.created","fields":[]}`,
		"digit-leading segment":    `{"name":"e","type":"acme.1item.created","fields":[]}`,
		"overlong type":            `{"name":"e","type":"acme.item.` + strings.Repeat("x", 128) + `","fields":[]}`,
		"reserved namespace":       `{"name":"e","type":"saas.item.created","fields":[]}`,
		"platform type":            `{"name":"e","type":"saas.auth.login","fields":[]}`,
		"duplicate declaration":    field(``) + `,` + `{"name":"f","type":"acme.item.created","fields":[]}`,
		"unknown field kind":       field(`{"name":"amount","kind":"decimal"}`),
		"host-stamped field":       field(`{"name":"solution","kind":"string"}`),
		"duplicate field":          field(`{"name":"a","kind":"int"},{"name":"a","kind":"int"}`),
		"malformed field name":     field(`{"name":"Amount","kind":"int"}`),
		"enum without values":      field(`{"name":"stage","kind":"enum"}`),
		"enum with empty values":   field(`{"name":"stage","kind":"enum","values":[]}`),
		"enum with repeated value": field(`{"name":"stage","kind":"enum","values":["a","a"]}`),
		"values on a non-enum":     field(`{"name":"count","kind":"int","values":["1"]}`),
		"unknown key on a field":   field(`{"name":"count","kind":"int","required":true}`),
		"fields null":              `{"name":"e","type":"acme.item.created","fields":null}`,
		"fields not an array":      `{"name":"e","type":"acme.item.created","fields":{"name":"a"}}`,
		"too many fields":          field(strings.TrimSuffix(manyFields(maxDeclaredAuditEventFields+1), ",")),
	}
	for name, events := range cases {
		_, err := ParseDeclaredAuditEventTypes("acme", declaringManifest("acme", events))
		if !errors.Is(err, ErrSolutionAuditDeclarationRejected) {
			t.Errorf("%s: err = %v, want a rejected declaration", name, err)
		}
	}
	for name, manifest := range map[string]string{
		"manifest that is not JSON":  "{not json",
		"dashboard events malformed": `{"dashboard":{"events":{"name":"e"}}}`,
	} {
		if _, err := ParseDeclaredAuditEventTypes("acme", manifest); !errors.Is(err, ErrSolutionAuditDeclarationRejected) {
			t.Errorf("%s: err = %v, want a rejected declaration", name, err)
		}
	}
}

func manyFields(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(`{"name":"f` + string(rune('a'+i%26)) + strings.Repeat("x", i/26) + `","kind":"int"},`)
	}
	return b.String()
}

func TestDeclaredAuditEventType_SchemaRoundTrip(t *testing.T) {
	declared := DeclaredAuditEventType{
		Type: "acme.item.created", Namespace: "acme", SolutionID: "acme", Description: "An item was created.",
		Fields: []PayloadField{
			{Name: "count", Kind: FieldInt},
			{Name: "enabled", Kind: FieldBool},
			{Name: "item_id", Kind: FieldUUID},
			{Name: "label", Kind: FieldString},
			{Name: "score", Kind: FieldNumber},
			{Name: "stage", Kind: FieldEnum, Enum: []string{"draft", "final"}},
			{Name: "tags", Kind: FieldStringArray},
		},
	}
	back, err := DeclaredAuditEventTypeFromSchema(declared.Type, declared.Namespace,
		SolutionAuditOwner("acme"), declared.PayloadSchemaJSON())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !reflect.DeepEqual(back, declared) {
		t.Fatalf("round trip = %#v\nwant %#v", back, declared)
	}
	if _, err := DeclaredAuditEventTypeFromSchema(declared.Type, declared.Namespace, "accounts", declared.PayloadSchemaJSON()); err == nil {
		t.Fatal("a code-owned row must not read back as a declared type")
	}
}

func TestCheckAdditiveAuditFieldChange(t *testing.T) {
	admitted := DeclaredAuditEventType{Type: "acme.item.created", Fields: []PayloadField{
		{Name: "count", Kind: FieldInt},
		{Name: "stage", Kind: FieldEnum, Enum: []string{"draft", "final"}},
	}}
	with := func(fields ...PayloadField) DeclaredAuditEventType {
		return DeclaredAuditEventType{Type: admitted.Type, Fields: fields}
	}
	count := PayloadField{Name: "count", Kind: FieldInt}
	stage := PayloadField{Name: "stage", Kind: FieldEnum, Enum: []string{"draft", "final"}}
	cases := []struct {
		name  string
		next  DeclaredAuditEventType
		allow bool
	}{
		{"identical", with(count, stage), true},
		{"adds a field", with(count, stage, PayloadField{Name: "score", Kind: FieldNumber}), true},
		{"adds an enum value", with(count, PayloadField{Name: "stage", Kind: FieldEnum, Enum: []string{"archived", "draft", "final"}}), true},
		{"drops a field", with(stage), false},
		{"retypes a field", with(PayloadField{Name: "count", Kind: FieldNumber}, stage), false},
		{"drops an enum value", with(count, PayloadField{Name: "stage", Kind: FieldEnum, Enum: []string{"draft"}}), false},
	}
	for _, tc := range cases {
		err := checkAdditiveAuditFieldChange(admitted, tc.next)
		if tc.allow && err != nil {
			t.Errorf("%s: refused: %v", tc.name, err)
		}
		if !tc.allow && !errors.Is(err, ErrSolutionAuditDeclarationRejected) {
			t.Errorf("%s: err = %v, want a rejected declaration", tc.name, err)
		}
	}
}

func TestValidatePayloadFields_NumberIsFinite(t *testing.T) {
	fields := DeclaredAuditEventType{Fields: []PayloadField{{Name: "score", Kind: FieldNumber}}}.validationFields()
	for name, value := range map[string]any{"fraction": 0.25, "integer": 3, "negative": -1.5} {
		if err := validatePayloadFields("acme.item.created", fields, map[string]any{"solution": "acme", "score": value}); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	for name, value := range map[string]any{"NaN": math.NaN(), "+Inf": math.Inf(1), "-Inf": math.Inf(-1), "string": "0.5", "bool": true} {
		if err := validatePayloadFields("acme.item.created", fields, map[string]any{"solution": "acme", "score": value}); err == nil {
			t.Errorf("%s: accepted, want refused", name)
		}
	}
	if err := validatePayloadFields("acme.item.created", fields, map[string]any{"score": 1.0}); err == nil {
		t.Error("a payload without the host-stamped solution must be refused")
	}
	if err := validatePayloadFields("acme.item.created", fields, map[string]any{"solution": "acme", "extra": 1}); err == nil {
		t.Error("an undeclared field must be refused")
	}
}

// An int arrives as a float64 whenever it crossed a protobuf Struct or JSON, so
// the float64 must be whole and inside the range a float64 counts exactly.
func TestValidatePayloadFields_IntIsWhole(t *testing.T) {
	fields := DeclaredAuditEventType{Fields: []PayloadField{{Name: "count", Kind: FieldInt}}}.validationFields()
	const maxSafe = 1<<53 - 1
	for name, value := range map[string]any{
		"int": 2, "int64": int64(2), "whole float": 2.0, "negative whole float": -7.0, "zero": 0.0,
		"largest safe float": float64(maxSafe), "smallest safe float": -float64(maxSafe),
		"int64 beyond 2^53": int64(1) << 60,
	} {
		if err := validatePayloadFields("acme.item.created", fields, map[string]any{"solution": "acme", "count": value}); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	for name, value := range map[string]any{
		"fraction": 1.5, "negative fraction": -0.5, "tiny fraction": 1e-9,
		"2^53 float": float64(1 << 53), "-2^53 float": -float64(1 << 53), "huge float": 1e300,
		"NaN": math.NaN(), "+Inf": math.Inf(1), "string": "2", "bool": true,
	} {
		if err := validatePayloadFields("acme.item.created", fields, map[string]any{"solution": "acme", "count": value}); err == nil {
			t.Errorf("%s: accepted, want refused", name)
		}
	}
}

// declaredAuditStore is an in-memory Store for the registration write and the
// declared-type methods. WithControlPlane restores the prior state when fn
// fails, which is the rollback the admission contract depends on.
type declaredAuditStore struct {
	Store
	registrations map[string]SolutionRegistration
	revision      int64
	rows          map[EventType]declaredAuditRow
	puts          int
}

type declaredAuditRow struct {
	owner, namespace string
	declared         DeclaredAuditEventType
}

func newDeclaredAuditStore() *declaredAuditStore {
	return &declaredAuditStore{registrations: map[string]SolutionRegistration{}, rows: map[EventType]declaredAuditRow{}}
}

func (f *declaredAuditStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	registrations := make(map[string]SolutionRegistration, len(f.registrations))
	for k, v := range f.registrations {
		registrations[k] = v
	}
	rows := make(map[EventType]declaredAuditRow, len(f.rows))
	for k, v := range f.rows {
		rows[k] = v
	}
	revision, puts := f.revision, f.puts
	if err := fn(ctx); err != nil {
		f.registrations, f.rows, f.revision, f.puts = registrations, rows, revision, puts
		return err
	}
	return nil
}

func (f *declaredAuditStore) GetSolutionRegistrationForUpdate(_ context.Context, id string) (*SolutionRegistration, error) {
	record, ok := f.registrations[id]
	if !ok {
		return nil, nil
	}
	return &record, nil
}

func (f *declaredAuditStore) NextSolutionRegistryRevision(context.Context) (int64, error) {
	f.revision++
	return f.revision, nil
}

func (f *declaredAuditStore) SaveSolutionRegistration(_ context.Context, record *SolutionRegistration) error {
	f.registrations[record.SolutionID] = *record
	return nil
}

func (f *declaredAuditStore) LockAuditEventNamespace(context.Context, string) error { return nil }

func (f *declaredAuditStore) ListAuditEventNamespaceOwners(_ context.Context, namespace string) ([]string, error) {
	seen := map[string]bool{}
	var owners []string
	for _, row := range f.rows {
		if row.namespace == namespace && !seen[row.owner] {
			seen[row.owner] = true
			owners = append(owners, row.owner)
		}
	}
	sort.Strings(owners)
	return owners, nil
}

func (f *declaredAuditStore) GetDeclaredAuditEventType(_ context.Context, t EventType) (*DeclaredAuditEventType, error) {
	row, ok := f.rows[t]
	if !ok || !strings.HasPrefix(row.owner, SolutionAuditOwnerPrefix) {
		return nil, nil
	}
	declared := row.declared
	return &declared, nil
}

func (f *declaredAuditStore) ListDeclaredAuditEventTypes(context.Context) ([]DeclaredAuditEventType, error) {
	var out []DeclaredAuditEventType
	for _, row := range f.rows {
		if strings.HasPrefix(row.owner, SolutionAuditOwnerPrefix) {
			out = append(out, row.declared)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

func (f *declaredAuditStore) PutDeclaredAuditEventType(_ context.Context, declared DeclaredAuditEventType) error {
	owner := SolutionAuditOwner(declared.SolutionID)
	if row, ok := f.rows[declared.Type]; ok && row.owner != owner {
		return ErrSolutionAuditNamespaceOwned
	}
	f.rows[declared.Type] = declaredAuditRow{owner: owner, namespace: declared.Namespace, declared: declared}
	f.puts++
	return nil
}

func registerDeclaring(t *testing.T, svc *Service, id string, expected *int64, events string) (*SolutionRegistration, error) {
	t.Helper()
	write := frontendWrite(id, "publisher-"+id, declaringManifest(id, events))
	write.ExpectedRevision = expected
	return svc.PutSolutionRegistration(context.Background(), write)
}

func TestPutSolutionRegistration_AdmitsDeclaredTypes(t *testing.T) {
	store := newDeclaredAuditStore()
	svc, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	record, err := registerDeclaring(t, svc, "acme", nil, exampleDeclaredEvent)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	admitted, _ := store.GetDeclaredAuditEventType(context.Background(), "acme.item.created")
	if admitted == nil || admitted.SolutionID != "acme" || len(admitted.Fields) != 3 {
		t.Fatalf("admitted = %#v", admitted)
	}
	if got := store.rows["acme.item.created"].owner; got != "solution:acme" {
		t.Fatalf("owner = %q, want solution:acme", got)
	}

	// An identical declaration in a changed manifest is idempotent: the
	// registration advances, the admitted type is not rewritten.
	puts := store.puts
	revision := record.Revision
	record, err = registerDeclaring(t, svc, "acme", &revision,
		exampleDeclaredEvent+`,{"name":"login","type":"saas.auth.login"}`)
	if err != nil {
		t.Fatalf("re-register identical declaration: %v", err)
	}
	if store.puts != puts {
		t.Fatalf("an identical declaration rewrote %d types", store.puts-puts)
	}

	// Adding a field is admitted.
	revision = record.Revision
	record, err = registerDeclaring(t, svc, "acme", &revision,
		`{"name":"created","type":"acme.item.created","fields":[{"name":"stage","kind":"enum","values":["draft","final"]},{"name":"score","kind":"number"},{"name":"count","kind":"int"},{"name":"owner_id","kind":"uuid"}]}`)
	if err != nil {
		t.Fatalf("additive change: %v", err)
	}
	if admitted, _ := store.GetDeclaredAuditEventType(context.Background(), "acme.item.created"); len(admitted.Fields) != 4 {
		t.Fatalf("additive change not admitted: %#v", admitted)
	}

	// Dropping a field refuses the whole write: the registration keeps the
	// revision and manifest it had.
	revision = record.Revision
	before := store.registrations["acme"]
	if _, err := registerDeclaring(t, svc, "acme", &revision,
		`{"name":"created","type":"acme.item.created","fields":[{"name":"count","kind":"int"}]}`); !errors.Is(err, ErrSolutionAuditDeclarationRejected) {
		t.Fatalf("dropping a field: err = %v, want rejected", err)
	}
	if after := store.registrations["acme"]; after.Revision != before.Revision || after.Frontend.Manifest != before.Frontend.Manifest {
		t.Fatal("a refused declaration must leave the last admitted registration in place")
	}
}

func TestPutSolutionRegistration_RefusesAnotherProducersNamespace(t *testing.T) {
	store := newDeclaredAuditStore()
	svc, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registerDeclaring(t, svc, "acme", nil, exampleDeclaredEvent); err != nil {
		t.Fatalf("first solution: %v", err)
	}
	// A second solution may neither take the type nor add one beside it.
	for _, events := range []string{
		exampleDeclaredEvent,
		`{"name":"deleted","type":"acme.item.deleted","fields":[]}`,
	} {
		_, err := registerDeclaring(t, svc, "other", nil, events)
		if !errors.Is(err, ErrSolutionAuditNamespaceOwned) || !errors.Is(err, ErrSolutionAuditDeclarationRejected) {
			t.Fatalf("second solution: err = %v, want the namespace refused", err)
		}
		if _, stored := store.registrations["other"]; stored {
			t.Fatal("a refused declaration must not store the registration")
		}
	}
	// A namespace the code catalog holds is refused the same way.
	store.rows["legacy.item.created"] = declaredAuditRow{owner: "accounts", namespace: "legacy"}
	if _, err := registerDeclaring(t, svc, "third", nil, `{"name":"e","type":"legacy.item.other","fields":[]}`); !errors.Is(err, ErrSolutionAuditNamespaceOwned) {
		t.Fatalf("code-owned namespace: err = %v, want refused", err)
	}
}

// A namespace the composed event catalog publishes domain events under already
// has its producer, even though no audit_event_types row names it.
func TestPutSolutionRegistration_RefusesPublishedEventNamespaces(t *testing.T) {
	store := newDeclaredAuditStore()
	svc, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	// installation and scope are the platform's own domain-event namespaces;
	// every other namespace in the catalog has a producer just the same. saas is
	// left out: it is reserved, and refused before ownership is consulted.
	namespaces := []string{"installation", "scope"}
	for _, published := range eventcatalog.Published() {
		if published.Namespace != AuditNamespace {
			namespaces = append(namespaces, published.Namespace)
		}
	}
	for _, namespace := range namespaces {
		_, err := registerDeclaring(t, svc, "acme", nil,
			`{"name":"e","type":"`+namespace+`.item.created","fields":[]}`)
		if !errors.Is(err, ErrSolutionAuditNamespaceOwned) || !errors.Is(err, ErrSolutionAuditDeclarationRejected) {
			t.Fatalf("%s: err = %v, want the namespace refused", namespace, err)
		}
		if _, stored := store.registrations["acme"]; stored {
			t.Fatalf("%s: a refused declaration must not store the registration", namespace)
		}
		if len(store.rows) != 0 {
			t.Fatalf("%s: a refused declaration admitted %d types", namespace, len(store.rows))
		}
	}
	// A namespace no producer publishes under is still free.
	if _, err := registerDeclaring(t, svc, "acme", nil, `{"name":"e","type":"acme.item.created","fields":[]}`); err != nil {
		t.Fatalf("unpublished namespace: %v", err)
	}
}

// A renewal carries a manifest already admitted and is not re-read, so a
// heartbeat never touches the audit registry.
func TestPutSolutionRegistration_RenewalDoesNotReadmit(t *testing.T) {
	store := newDeclaredAuditStore()
	svc, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registerDeclaring(t, svc, "acme", nil, exampleDeclaredEvent); err != nil {
		t.Fatalf("register: %v", err)
	}
	puts := store.puts
	delete(store.rows, "acme.item.created")
	if _, err := registerDeclaring(t, svc, "acme", nil, exampleDeclaredEvent); err != nil {
		t.Fatalf("renewal: %v", err)
	}
	if store.puts != puts {
		t.Fatal("a lease renewal must not re-admit declared types")
	}
}

func TestAuditEventTypes_ListsCatalogAndDeclaredTypes(t *testing.T) {
	store := newDeclaredAuditStore()
	svc, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registerDeclaring(t, svc, "acme", nil, exampleDeclaredEvent); err != nil {
		t.Fatalf("register: %v", err)
	}
	definitions, err := svc.AuditEventTypes(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(definitions) != len(AuditEventCatalog())+1 {
		t.Fatalf("listed %d types, want the catalog plus one declared", len(definitions))
	}
	if !sort.SliceIsSorted(definitions, func(i, j int) bool { return definitions[i].Type < definitions[j].Type }) {
		t.Fatal("the listing must be sorted by type")
	}
	var found *AuditEventDefinition
	for i := range definitions {
		if definitions[i].Type == "acme.item.created" {
			found = &definitions[i]
		}
	}
	if found == nil || found.Owner != "solution:acme" || found.Category != CategorySolution ||
		found.Namespace != "acme" || found.Description != "An item was created." || found.Version != DeclaredAuditEventVersion {
		t.Fatalf("declared type listed as %#v", found)
	}
}
