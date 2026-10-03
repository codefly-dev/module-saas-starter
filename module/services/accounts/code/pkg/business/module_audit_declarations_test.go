package business

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// recordingAudit keeps every entry the service commits, so a test can say what
// a declaration recorded without a database.
type recordingAudit struct{ entries []AuditEntry }

func (r *recordingAudit) Emit(_ context.Context, e AuditEntry) { r.entries = append(r.entries, e) }

func (r *recordingAudit) EmitTx(_ context.Context, e AuditEntry) error {
	r.entries = append(r.entries, e)
	return nil
}

// sharedConversationTypes is a module's declaration of three types in its own
// namespace, with every field shape a module needs: identifiers, an enum and a
// whole-number count.
func sharedConversationTypes() []AuditEventTypeDeclaration {
	shareFields := []AuditFieldDeclaration{
		{Name: "conversation_id", Kind: "string"},
		{Name: "share_id", Kind: "string"},
		{Name: "grantee_kind", Kind: "enum", Values: []string{"user", "team"}},
		{Name: "grantee_id", Kind: "string", PII: true},
	}
	return []AuditEventTypeDeclaration{
		{Type: "acme.chat.conversation_shared", Description: "A conversation was shared.", Fields: shareFields},
		{Type: "acme.chat.conversation_unshared", Description: "A conversation share was removed.", Fields: shareFields},
		{Type: "acme.chat.shared_conversation_read", Description: "A shared conversation was read.", Fields: []AuditFieldDeclaration{
			{Name: "conversation_id", Kind: "string"},
			{Name: "share_id", Kind: "string"},
			{Name: "owner_id", Kind: "string", PII: true},
			{Name: "withheld_turns", Kind: "int"},
		}},
	}
}

func newModuleDeclaringService(t *testing.T, store *declaredAuditStore, grants ModulePrincipalRegistry) (*Service, *recordingAudit) {
	t.Helper()
	svc, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	withCurrentAuthority(svc)
	withCurrentAuthority(svc)
	svc.SetModulePrincipals(grants)
	audit := &recordingAudit{}
	svc.SetAuditEmitter(audit)
	return svc, audit
}

func moduleGrants(prefix string, namespaces ...string) ModulePrincipalRegistry {
	return ModulePrincipalRegistry{
		ModulePrincipalID(prefix): {Prefix: prefix, Namespaces: namespaces, CrossTenant: true},
	}
}

func requireStatus(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("code = %v (%v), want %v", got, err, want)
	}
}

func TestModuleDeclareAuditEventTypes_AdmitsIntoTheBoundNamespace(t *testing.T) {
	store := newDeclaredAuditStore()
	svc, audit := newModuleDeclaringService(t, store, moduleGrants("acme", "acme"))
	caller := ModuleCaller{PrincipalID: ModulePrincipalID("acme")}

	admitted, takenOver, err := svc.ModuleDeclareAuditEventTypes(context.Background(), caller, "acme", sharedConversationTypes())
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if len(admitted) != 3 || len(takenOver) != 0 {
		t.Fatalf("admitted = %v, taken over = %v", admitted, takenOver)
	}
	row := store.rows["acme.chat.shared_conversation_read"]
	if row.owner != "solution:acme" || row.declared.SolutionID != "acme" {
		t.Fatalf("row = %#v", row)
	}
	// The pii flag is admitted with the field, so every export path strips it.
	pii := map[string]bool{}
	for _, f := range store.rows["acme.chat.conversation_shared"].declared.Fields {
		pii[f.Name] = f.PII
	}
	if !pii["grantee_id"] || pii["conversation_id"] {
		t.Fatalf("pii = %v", pii)
	}
	if len(audit.entries) != 1 || audit.entries[0].EventType != EventModuleAuditTypesDeclared {
		t.Fatalf("recorded = %#v", audit.entries)
	}

	// A module declares on every start: an identical declaration writes and
	// records nothing.
	admitted, _, err = svc.ModuleDeclareAuditEventTypes(context.Background(), caller, "acme", sharedConversationTypes())
	if err != nil || len(admitted) != 0 {
		t.Fatalf("re-declare: admitted = %v, err = %v", admitted, err)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("a re-declaration recorded %d entries", len(audit.entries)-1)
	}
}

func TestModuleDeclareAuditEventTypes_FailsClosed(t *testing.T) {
	caller := ModuleCaller{PrincipalID: ModulePrincipalID("acme")}
	for name, tc := range map[string]struct {
		grants ModulePrincipalRegistry
		caller ModuleCaller
		prefix string
		types  []AuditEventTypeDeclaration
		code   codes.Code
	}{
		"a namespace the operator did not bind": {
			grants: moduleGrants("acme", "other"), caller: caller, prefix: "acme",
			types: sharedConversationTypes(), code: codes.InvalidArgument,
		},
		"no namespace bound at all": {
			grants: moduleGrants("acme"), caller: caller, prefix: "acme",
			types: sharedConversationTypes(), code: codes.InvalidArgument,
		},
		"another module's prefix": {
			grants: ModulePrincipalRegistry{
				ModulePrincipalID("acme"):  {Prefix: "acme", Namespaces: []string{"acme"}},
				ModulePrincipalID("other"): {Prefix: "other", Namespaces: []string{"acme"}},
			},
			caller: caller, prefix: "other", types: sharedConversationTypes(), code: codes.PermissionDenied,
		},
		"a principal with no entry": {
			grants: ModulePrincipalRegistry{}, caller: caller, prefix: "acme",
			types: sharedConversationTypes(), code: codes.PermissionDenied,
		},
		"the host's own namespace": {
			grants: moduleGrants("acme", "saas"), caller: caller, prefix: "acme",
			types: []AuditEventTypeDeclaration{{Type: "saas.chat.shared", Fields: []AuditFieldDeclaration{{Name: "id", Kind: "string"}}}},
			code:  codes.InvalidArgument,
		},
		"an unknown kind": {
			grants: moduleGrants("acme", "acme"), caller: caller, prefix: "acme",
			types: []AuditEventTypeDeclaration{{Type: "acme.chat.shared", Fields: []AuditFieldDeclaration{{Name: "id", Kind: "blob"}}}},
			code:  codes.InvalidArgument,
		},
		"an enum with no values": {
			grants: moduleGrants("acme", "acme"), caller: caller, prefix: "acme",
			types: []AuditEventTypeDeclaration{{Type: "acme.chat.shared", Fields: []AuditFieldDeclaration{{Name: "kind", Kind: "enum"}}}},
			code:  codes.InvalidArgument,
		},
		"the host-stamped solution field": {
			grants: moduleGrants("acme", "acme"), caller: caller, prefix: "acme",
			types: []AuditEventTypeDeclaration{{Type: "acme.chat.shared", Fields: []AuditFieldDeclaration{{Name: "solution", Kind: "string"}}}},
			code:  codes.InvalidArgument,
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newDeclaredAuditStore()
			svc, audit := newModuleDeclaringService(t, store, tc.grants)
			_, _, err := svc.ModuleDeclareAuditEventTypes(context.Background(), tc.caller, tc.prefix, tc.types)
			requireStatus(t, err, tc.code)
			if len(store.rows) != 0 || len(audit.entries) != 0 {
				t.Fatalf("a refused declaration wrote %d types and %d records", len(store.rows), len(audit.entries))
			}
		})
	}
}

// A declared type only grows, whichever path declares it: dropping a field a
// module already declared is refused, and nothing is written.
func TestModuleDeclareAuditEventTypes_OnlyGrows(t *testing.T) {
	store := newDeclaredAuditStore()
	svc, _ := newModuleDeclaringService(t, store, moduleGrants("acme", "acme"))
	caller := ModuleCaller{PrincipalID: ModulePrincipalID("acme")}
	if _, _, err := svc.ModuleDeclareAuditEventTypes(context.Background(), caller, "acme", sharedConversationTypes()); err != nil {
		t.Fatal(err)
	}
	narrowed := sharedConversationTypes()
	narrowed[0].Fields = narrowed[0].Fields[:2]
	_, _, err := svc.ModuleDeclareAuditEventTypes(context.Background(), caller, "acme", narrowed)
	requireStatus(t, err, codes.InvalidArgument)
	if got := len(store.rows["acme.chat.conversation_shared"].declared.Fields); got != 4 {
		t.Fatalf("fields after a refused narrowing = %d, want 4", got)
	}
}

// Once declared, the module's events are accepted by EmitAuditEvent under its
// prefix and checked against the declared fields, exactly as a solution's are.
func TestModuleDeclareAuditEventTypes_ThenEmit(t *testing.T) {
	store := newDeclaredAuditStore()
	svc, audit := newModuleDeclaringService(t, store, moduleGrants("acme", "acme"))
	caller := ModuleCaller{PrincipalID: ModulePrincipalID("acme")}
	if _, _, err := svc.ModuleDeclareAuditEventTypes(context.Background(), caller, "acme", sharedConversationTypes()); err != nil {
		t.Fatal(err)
	}
	emit := func(payload map[string]any) error {
		fields, err := structpb.NewStruct(payload)
		if err != nil {
			t.Fatal(err)
		}
		return svc.ModuleEmitAuditEvent(context.Background(), caller, "", "acme.chat.shared_conversation_read",
			"system:chat", "acme", "conversation-1", "", fields)
	}
	if err := emit(map[string]any{"conversation_id": "conversation-1", "share_id": "share-1", "owner_id": "owner-1", "withheld_turns": 2.0}); err != nil {
		t.Fatalf("a declared type should be accepted: %v", err)
	}
	if last := audit.entries[len(audit.entries)-1]; last.EventType != "acme.chat.shared_conversation_read" {
		t.Fatalf("recorded %q", last.EventType)
	}
	requireStatus(t, emit(map[string]any{"conversation_id": "c", "withheld_turns": 1.5}), codes.InvalidArgument)
	requireStatus(t, emit(map[string]any{"conversation_id": "c", "undeclared": "x"}), codes.InvalidArgument)

	// The declared pii field never leaves the store: the one lookup every
	// export path redacts through strips it.
	resolved, err := svc.AuditEventResolver().Resolve(context.Background(), "acme.chat.shared_conversation_read")
	if err != nil || !resolved.Registered {
		t.Fatalf("resolve: registered = %v, err = %v", resolved.Registered, err)
	}
	redacted := resolved.Redact(map[string]any{"conversation_id": "c", "owner_id": "owner-1"})
	if _, leaked := redacted["owner_id"]; leaked || redacted["conversation_id"] != "c" {
		t.Fatalf("redacted = %v", redacted)
	}
}
