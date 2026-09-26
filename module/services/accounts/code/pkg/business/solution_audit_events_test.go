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
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"
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
			{Name: "requester_email", Kind: FieldString, PII: true},
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
		{"marks a field pii", with(PayloadField{Name: "count", Kind: FieldInt, PII: true}, stage), true},
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

func (f *declaredAuditStore) TransferAuditEventNamespace(_ context.Context, namespace, from, to string) error {
	for eventType, row := range f.rows {
		if row.namespace == namespace && row.owner == from {
			row.owner = to
			if id, ok := SolutionIDFromAuditOwner(to); ok {
				row.declared.SolutionID = id
			}
			f.rows[eventType] = row
		}
	}
	return nil
}

// bindings is the operator's namespace binding per solution id: the
// `namespaces` of each solution's MODULE_PRINCIPALS entry.
type bindings map[string][]string

func (b bindings) registry() ModulePrincipalRegistry {
	registry := ModulePrincipalRegistry{}
	for solutionID, namespaces := range b {
		registry[ModulePrincipalID(solutionID)] = ModulePrincipalGrant{Prefix: solutionID, Namespaces: namespaces}
	}
	return registry
}

func newDeclaringService(t *testing.T, store *declaredAuditStore, bound bindings) *Service {
	t.Helper()
	svc, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetModulePrincipals(bound.registry())
	return svc
}

func registerDeclaring(t *testing.T, svc *Service, id string, expected *int64, events string) (*SolutionRegistration, error) {
	t.Helper()
	write := frontendWrite(id, "publisher-"+id, declaringManifest(id, events))
	write.ExpectedRevision = expected
	return svc.PutSolutionRegistration(context.Background(), write)
}

func TestPutSolutionRegistration_AdmitsDeclaredTypes(t *testing.T) {
	store := newDeclaredAuditStore()
	svc := newDeclaringService(t, store, bindings{"acme": {"acme"}, "other": {"acme"}, "third": {"legacy"}})
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
	svc := newDeclaringService(t, store, bindings{"acme": {"acme"}, "other": {"acme"}, "third": {"legacy"}})
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
	svc := newDeclaringService(t, store, bindings{"acme": {"acme"}, "other": {"acme"}, "third": {"legacy"}})
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
	svc := newDeclaringService(t, store, bindings{"acme": {"acme"}, "other": {"acme"}, "third": {"legacy"}})
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
	svc := newDeclaringService(t, store, bindings{"acme": {"acme"}, "other": {"acme"}, "third": {"legacy"}})
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

// A solution admits types only into the namespaces the operator bound to it,
// so a registration credential alone claims nothing.
func TestPutSolutionRegistration_RefusesAnUnboundNamespace(t *testing.T) {
	for name, bound := range map[string]bindings{
		"no principal entry":        {},
		"entry binds no namespace":  {"acme": nil},
		"entry binds another space": {"acme": {"other"}},
	} {
		store := newDeclaredAuditStore()
		svc := newDeclaringService(t, store, bound)
		_, err := registerDeclaring(t, svc, "acme", nil, exampleDeclaredEvent)
		if !errors.Is(err, ErrSolutionAuditNamespaceUnbound) || !errors.Is(err, ErrSolutionAuditDeclarationRejected) {
			t.Fatalf("%s: err = %v, want the unbound namespace refused", name, err)
		}
		if _, stored := store.registrations["acme"]; stored || len(store.rows) != 0 {
			t.Fatalf("%s: a refused declaration must store nothing", name)
		}
	}
	// A manifest that declares nothing needs no binding.
	store := newDeclaredAuditStore()
	svc := newDeclaringService(t, store, bindings{})
	if _, err := registerDeclaring(t, svc, "acme", nil, `{"name":"login","type":"saas.auth.login"}`); err != nil {
		t.Fatalf("a fieldless declaration: %v", err)
	}
}

// Ownership follows the binding: once the operator unbinds a namespace from
// its holder and binds it to another solution, that solution's next admission
// takes every type in it over, and the registration event records it.
func TestPutSolutionRegistration_TakesOverAReleasedNamespace(t *testing.T) {
	store := newDeclaredAuditStore()
	svc := newDeclaringService(t, store, bindings{"acme": {"acme"}})
	if _, err := registerDeclaring(t, svc, "acme", nil, exampleDeclaredEvent); err != nil {
		t.Fatalf("first holder: %v", err)
	}

	// Rebound: the holder no longer lists the namespace; the successor does.
	for name, bound := range map[string]bindings{
		"holder keeps an entry": {"acme": {"elsewhere"}, "successor": {"acme"}},
		"holder has no entry":   {"successor": {"acme"}},
	} {
		store := newDeclaredAuditStore()
		svc := newDeclaringService(t, store, bindings{"acme": {"acme"}})
		if _, err := registerDeclaring(t, svc, "acme", nil, exampleDeclaredEvent); err != nil {
			t.Fatalf("%s: first holder: %v", name, err)
		}
		emitter := &recordingEmitter{}
		svc.SetAuditEmitter(emitter)
		svc.SetModulePrincipals(bound.registry())
		// The successor re-declares the type (still additive) and adds one.
		if _, err := registerDeclaring(t, svc, "successor", nil, exampleDeclaredEvent+
			`,{"name":"deleted","type":"acme.item.deleted","fields":[]}`); err != nil {
			t.Fatalf("%s: successor: %v", name, err)
		}
		for _, eventType := range []EventType{"acme.item.created", "acme.item.deleted"} {
			if got := store.rows[eventType].owner; got != "solution:successor" {
				t.Fatalf("%s: %s owner = %q, want solution:successor", name, eventType, got)
			}
		}
		var recorded []string
		for _, entry := range emitter.entries {
			if entry.EventType == EventSolutionRegistrationUpdated {
				recorded, _ = entry.Payload["audit_namespaces_taken_over"].([]string)
			}
		}
		if !reflect.DeepEqual(recorded, []string{"acme"}) {
			t.Fatalf("%s: takeover recorded as %v, want [acme]", name, recorded)
		}
	}

	// While the holder is still bound, a second binding does not release it:
	// that is a misconfiguration, refused rather than raced.
	svc.SetModulePrincipals(bindings{"acme": {"acme"}, "successor": {"acme"}}.registry())
	if _, err := registerDeclaring(t, svc, "successor", nil, exampleDeclaredEvent); !errors.Is(err, ErrSolutionAuditNamespaceOwned) {
		t.Fatalf("holder still bound: err = %v, want refused", err)
	}

	// A code-owned namespace is never taken over.
	store.rows["legacy.item.created"] = declaredAuditRow{owner: "accounts", namespace: "legacy"}
	svc.SetModulePrincipals(bindings{"successor": {"legacy"}}.registry())
	if _, err := registerDeclaring(t, svc, "successor", nil, `{"name":"e","type":"legacy.item.other","fields":[]}`); !errors.Is(err, ErrSolutionAuditNamespaceOwned) {
		t.Fatalf("code-owned namespace: err = %v, want refused", err)
	}
}

// recordingEmitter captures the transactional audit entries a registration
// write emits.
type recordingEmitter struct{ entries []AuditEntry }

func (e *recordingEmitter) Emit(_ context.Context, entry AuditEntry) {
	e.entries = append(e.entries, entry)
}

func (e *recordingEmitter) EmitTx(_ context.Context, entry AuditEntry) error {
	e.entries = append(e.entries, entry)
	return nil
}

// A pii field is read from the declaration, and once admitted as pii it stays so.
func TestDeclaredAuditEventType_PIIIsDeclaredAndOnlyTightens(t *testing.T) {
	declared, err := ParseDeclaredAuditEventTypes("acme", declaringManifest("acme",
		`{"name":"created","type":"acme.item.created","fields":[{"name":"email","kind":"string","pii":true},{"name":"count","kind":"int"}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fields := declared[0].Fields
	if fields[0].Name != "count" || fields[0].PII || fields[1].Name != "email" || !fields[1].PII {
		t.Fatalf("fields = %#v, want email marked pii", fields)
	}
	unmarked := DeclaredAuditEventType{Type: declared[0].Type, Fields: []PayloadField{
		{Name: "count", Kind: FieldInt}, {Name: "email", Kind: FieldString},
	}}
	if err := checkAdditiveAuditFieldChange(declared[0], unmarked); !errors.Is(err, ErrSolutionAuditDeclarationRejected) {
		t.Fatalf("unmarking pii: err = %v, want refused", err)
	}
}

// The one type lookup resolves a declared type: it is registered, carries the
// host-stamped solution field, and its pii fields are stripped on export —
// neither redacted whole nor dead-lettered as unregistered.
func TestAuditEventResolver_ResolvesDeclaredTypes(t *testing.T) {
	store := newDeclaredAuditStore()
	declared := DeclaredAuditEventType{
		Type: "acme.item.created", Namespace: "acme", SolutionID: "acme",
		Fields: []PayloadField{{Name: "count", Kind: FieldInt}, {Name: "email", Kind: FieldString, PII: true}},
	}
	store.rows[declared.Type] = declaredAuditRow{owner: "solution:acme", namespace: "acme", declared: declared}

	resolved, err := NewAuditEventResolver(store).Resolve(t.Context(), declared.Type)
	if err != nil || !resolved.Registered || resolved.Category() != string(CategorySolution) {
		t.Fatalf("resolved = %#v, %v", resolved, err)
	}
	payload := map[string]any{"solution": "acme", "count": 2.0, "email": "jane@example.com"}
	if err := resolved.Validate(declared.Type, payload); err != nil {
		t.Fatalf("a well-typed declared payload: %v", err)
	}
	if got := resolved.Redact(payload); !reflect.DeepEqual(got, map[string]any{"solution": "acme", "count": 2.0}) {
		t.Fatalf("redacted = %v, want only the pii field stripped", got)
	}

	// The same through the export tee and the sink it feeds.
	entry := AuditEntry{ID: NewIDString(), OrgID: teeOrgID, EventType: declared.Type, Payload: payload}
	request, ok := buildAuditExportJob(t.Context(), entry, resolved)
	if !ok {
		t.Fatal("the export job was not built")
	}
	sink := &capturingSink{}
	handler, err := NewAuditExportJobHandler(sink, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler(t.Context(), exportEnvelopeFor(request)); err != nil {
		t.Fatalf("a declared type must be exported, not dead-lettered: %v", err)
	}
	if _, leaked := sink.last.Payload["email"]; leaked || sink.last.Payload["count"] != 2.0 {
		t.Fatalf("sink payload = %v", sink.last.Payload)
	}

	// The same through the webhook body and the CSV/JSON download.
	data, err := AuditEventWebhookData(entry, resolved)
	if err != nil || strings.Contains(string(data), "jane@example.com") {
		t.Fatalf("webhook data = %s, %v", data, err)
	}
	exported, err := auditToJSON(t.Context(), NewAuditEventResolver(store), []AuditEntry{entry})
	if err != nil || strings.Contains(string(exported), "jane@example.com") || !strings.Contains(string(exported), `"solution"`) {
		t.Fatalf("json export = %s, %v", exported, err)
	}

	// A registry that cannot be read is retried, never dead-lettered.
	failing, err := NewAuditExportJobHandler(sink, failingDeclaredReader{})
	if err != nil {
		t.Fatal(err)
	}
	if err := failing(t.Context(), exportEnvelopeFor(request)); err == nil || isProcessingError(err) {
		t.Fatalf("an unreadable registry: err = %v, want a retryable error", err)
	}
}

type failingDeclaredReader struct{}

func (failingDeclaredReader) GetDeclaredAuditEventType(context.Context, EventType) (*DeclaredAuditEventType, error) {
	return nil, errors.New("registry unreachable")
}

// exportEnvelopeFor is the envelope the worker leases for an enqueued export job.
func exportEnvelopeFor(request *jobsv1.EnqueueJobRequest) *jobsv1.JobEnvelope {
	job := request.GetJob()
	return &jobsv1.JobEnvelope{
		Id:             NewIDString(),
		Direction:      job.GetDirection(),
		Scope:          job.GetScope(),
		Queue:          job.GetQueue(),
		Topic:          job.GetTopic(),
		Source:         job.GetSource(),
		IdempotencyKey: job.GetIdempotencyKey(),
		SchemaVersion:  job.GetSchemaVersion(),
		Payload:        job.GetPayload(),
		ContentType:    job.GetContentType(),
		State:          jobsv1.JobState_JOB_STATE_PENDING,
		MaxAttempts:    job.GetMaxAttempts(),
	}
}

func isProcessingError(err error) bool {
	var processing *jobs.ProcessingError
	return errors.As(err, &processing)
}
