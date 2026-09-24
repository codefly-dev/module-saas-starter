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
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		declaredItemCreated, moduleUserA, "example-solution", "entry-1", "",
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
	// No verified person reaches this path, so the emitter-supplied actor id is
	// recorded as the agent it arrived through — never relabelled as a user.
	if entry.ActorType != business.ActorTypeAgent || entry.ActorID != moduleUserA {
		t.Fatalf("attribution = %s/%s, want agent/%s", entry.ActorType, entry.ActorID, moduleUserA)
	}
}

func TestModuleEmitAuditEvent_AnotherSolutionsDeclaredTypeRejected(t *testing.T) {
	svc, emitter := newDeclaredEmitService(t, "acme")
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		declaredItemCreated, "actor-1", "other-solution", "entry-1", "", nil)
	requireCode(t, err, codes.PermissionDenied)
	if len(emitter.entries) != 0 {
		t.Fatal("a refused emission must write nothing")
	}
}

func TestModuleEmitAuditEvent_DeclaredTypeNeedsNamespaceGrant(t *testing.T) {
	svc, _ := newDeclaredEmitService(t /* no namespaces */)
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		declaredItemCreated, "actor-1", "example-solution", "entry-1", "", nil)
	requireCode(t, err, codes.PermissionDenied)
}

func TestModuleEmitAuditEvent_UndeclaredTypeRejected(t *testing.T) {
	svc, _ := newDeclaredEmitService(t, "acme")
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		"acme.item.deleted", "actor-1", "example-solution", "entry-1", "", nil)
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
			declaredItemCreated, "actor-1", "example-solution", "entry-1", "", value)
		requireCode(t, err, codes.InvalidArgument)
	}
	if len(emitter.entries) != 0 {
		t.Fatalf("a mistyped payload wrote %d entries", len(emitter.entries))
	}
}
