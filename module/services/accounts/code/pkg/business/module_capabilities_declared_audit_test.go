package business_test

import (
	"context"
	"math"
	"testing"

	"accounts/pkg/business"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"
)

// Emission of solution-declared audit event types through the module surface,
// without a database: the declared type is served from a fake store and the
// written entry is captured.

// declaredTypeStore answers the one declared-type read the emission path makes.
type declaredTypeStore struct {
	fakeTxStore
	declared map[business.EventType]business.DeclaredAuditEventType
}

func (f declaredTypeStore) GetDeclaredAuditEventType(_ context.Context, t business.EventType) (*business.DeclaredAuditEventType, error) {
	d, ok := f.declared[t]
	if !ok {
		return nil, nil
	}
	return &d, nil
}

type capturingAuditEmitter struct{ entries []business.AuditEntry }

func (c *capturingAuditEmitter) Emit(_ context.Context, e business.AuditEntry) {
	c.entries = append(c.entries, e)
}

func (c *capturingAuditEmitter) EmitTx(_ context.Context, e business.AuditEntry) error {
	c.entries = append(c.entries, e)
	return nil
}

const declaredItemCreated = "acme.item.created"

func newDeclaredEmitService(t *testing.T, namespaces ...string) (*business.Service, *capturingAuditEmitter) {
	t.Helper()
	store := declaredTypeStore{declared: map[business.EventType]business.DeclaredAuditEventType{
		declaredItemCreated: {
			Type: declaredItemCreated, Namespace: "acme", SolutionID: "example-solution",
			Fields: []business.PayloadField{
				{Name: "count", Kind: business.FieldInt},
				{Name: "score", Kind: business.FieldNumber},
				{Name: "stage", Kind: business.FieldEnum, Enum: []string{"draft", "final"}},
			},
		},
	}}
	svc, err := business.NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	emitter := &capturingAuditEmitter{}
	svc.SetAuditEmitter(emitter)
	backend := &fakeJobBackend{}
	svc.SetModuleCapabilities(backend, backend, business.ModulePrincipalRegistry{
		modulePrincSvc: {Prefix: "content", Namespaces: namespaces},
	})
	return svc, emitter
}

func declaredFields(t *testing.T, values map[string]any) *structpb.Struct {
	t.Helper()
	fields, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	return fields
}

func TestModuleEmitAuditEvent_OwnDeclaredTypeAccepted(t *testing.T) {
	svc, emitter := newDeclaredEmitService(t, "acme")
	// The declared field set does not name `solution`; the host stamps it, and a
	// client-supplied value is overwritten rather than trusted.
	// The module acts on its own and names its own principal as the actor: the
	// form both the verbatim actor and a resolved one (a module's own
	// principal, or system:<process> mapped to it) record identically.
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		declaredItemCreated, modulePrincSvc, "example-solution", "entry-1", "",
		declaredFields(t, map[string]any{"count": 3, "score": 0.75, "stage": "final", "solution": "someone-else"}))
	if err != nil {
		t.Fatalf("own declared type: %v", err)
	}
	if len(emitter.entries) != 1 {
		t.Fatalf("wrote %d entries, want 1", len(emitter.entries))
	}
	entry := emitter.entries[0]
	if entry.EventType != declaredItemCreated || entry.Resource != "example-solution" || entry.OrgID != moduleTenantA {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Payload["solution"] != "example-solution" {
		t.Fatalf("payload solution = %v, want the stamped scope", entry.Payload["solution"])
	}
	// A declared type is attributed exactly as a catalog type is; the actor
	// type is the module surface's rule, not this type's, so only the principal
	// is pinned here.
	if entry.ActorID != modulePrincSvc {
		t.Fatalf("actor = %s, want the module's own principal %s", entry.ActorID, modulePrincSvc)
	}
	if entry.ActorType == business.ActorTypeUser {
		t.Fatal("no verified person reaches this path; the row must never claim a user")
	}
}

// A process label of the module's own is accepted for a declared type just as
// for a catalog one.
func TestModuleEmitAuditEvent_OwnDeclaredTypeAcceptsAProcessLabel(t *testing.T) {
	svc, emitter := newDeclaredEmitService(t, "acme")
	if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		declaredItemCreated, "system:ingest", "example-solution", "entry-1", "",
		declaredFields(t, map[string]any{"count": 1})); err != nil {
		t.Fatalf("process-label actor: %v", err)
	}
	if len(emitter.entries) != 1 {
		t.Fatalf("wrote %d entries, want 1", len(emitter.entries))
	}
}

func TestModuleEmitAuditEvent_AnotherSolutionsDeclaredTypeRejected(t *testing.T) {
	svc, emitter := newDeclaredEmitService(t, "acme")
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		declaredItemCreated, modulePrincSvc, "other-solution", "entry-1", "", nil)
	requireCode(t, err, codes.PermissionDenied)
	if len(emitter.entries) != 0 {
		t.Fatal("a refused emission must write nothing")
	}
}

func TestModuleEmitAuditEvent_DeclaredTypeNeedsNamespaceGrant(t *testing.T) {
	svc, _ := newDeclaredEmitService(t /* no namespaces */)
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		declaredItemCreated, modulePrincSvc, "example-solution", "entry-1", "", nil)
	requireCode(t, err, codes.PermissionDenied)
}

func TestModuleEmitAuditEvent_UndeclaredTypeRejected(t *testing.T) {
	svc, _ := newDeclaredEmitService(t, "acme")
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		"acme.item.deleted", modulePrincSvc, "example-solution", "entry-1", "", nil)
	requireCode(t, err, codes.InvalidArgument)
}

func TestModuleEmitAuditEvent_DeclaredPayloadTyped(t *testing.T) {
	svc, emitter := newDeclaredEmitService(t, "acme")
	for name, fields := range map[string]map[string]any{
		"non-finite number":   {"score": math.Inf(1)},
		"string for a number": {"score": "0.5"},
		"value outside enum":  {"stage": "archived"},
		"undeclared field":    {"colour": "blue"},
		"string for an int":   {"count": "3"},
		"NaN for an int":      {"count": math.NaN()},
		// A Struct carries every number as a double, so only its value says
		// whether it is an int.
		"fraction for an int":      {"count": 1.5},
		"inexact float for an int": {"count": float64(1 << 53)},
	} {
		value, err := structpb.NewStruct(fields)
		if err != nil {
			t.Fatalf("%s: fields: %v", name, err)
		}
		err = svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
			declaredItemCreated, modulePrincSvc, "example-solution", "entry-1", "", value)
		requireCode(t, err, codes.InvalidArgument)
	}
	if len(emitter.entries) != 0 {
		t.Fatalf("a mistyped payload wrote %d entries", len(emitter.entries))
	}
}
