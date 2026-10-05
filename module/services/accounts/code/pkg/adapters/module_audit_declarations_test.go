package adapters

import (
	"strings"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Every wire kind but UNSPECIFIED maps to a registry kind, so a kind added to
// the proto without a mapping fails here rather than reaching the registry as
// an empty kind.
func TestModuleAuditFieldKinds_CoverEveryWireKind(t *testing.T) {
	for value, name := range gen.ModuleAuditFieldKind_name {
		kind := gen.ModuleAuditFieldKind(value)
		if kind == gen.ModuleAuditFieldKind_MODULE_AUDIT_FIELD_KIND_UNSPECIFIED {
			continue
		}
		if _, ok := moduleAuditFieldKinds[kind]; !ok {
			t.Errorf("%s has no registry kind", name)
		}
	}
}

func TestModuleAuditDeclarations_CarryEveryFieldAttribute(t *testing.T) {
	out := moduleAuditDeclarations([]*gen.ModuleAuditEventTypeDeclaration{{
		Type:        "acme.chat.conversation_shared",
		Description: "A conversation was shared.",
		Fields: []*gen.ModuleAuditFieldDeclaration{
			{Name: "grantee_kind", Kind: gen.ModuleAuditFieldKind_MODULE_AUDIT_FIELD_KIND_ENUM, Values: []string{"user", "team"}},
			{Name: "grantee_id", Kind: gen.ModuleAuditFieldKind_MODULE_AUDIT_FIELD_KIND_STRING, Pii: true},
			{Name: "withheld_turns", Kind: gen.ModuleAuditFieldKind_MODULE_AUDIT_FIELD_KIND_INT},
		},
	}})
	declared, err := business.ValidateAuditEventTypeDeclarations("acme", out)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	fields := map[string]business.PayloadField{}
	for _, f := range declared[0].Fields {
		fields[f.Name] = f
	}
	if f := fields["grantee_kind"]; f.Kind != business.FieldEnum || len(f.Enum) != 2 {
		t.Errorf("grantee_kind = %#v", f)
	}
	if f := fields["grantee_id"]; f.Kind != business.FieldString || !f.PII {
		t.Errorf("grantee_id = %#v", f)
	}
	if f := fields["withheld_turns"]; f.Kind != business.FieldInt || f.Enum != nil {
		t.Errorf("withheld_turns = %#v", f)
	}
}

// Every wire retention class but UNSPECIFIED maps to the registry's, and
// UNSPECIFIED reads as the registry's default, content.
func TestModuleAuditRetentions_CoverEveryWireClass(t *testing.T) {
	for value, name := range gen.ModuleAuditEventRetention_name {
		retention := gen.ModuleAuditEventRetention(value)
		if retention == gen.ModuleAuditEventRetention_MODULE_AUDIT_EVENT_RETENTION_UNSPECIFIED {
			continue
		}
		if _, ok := moduleAuditRetentions[retention]; !ok {
			t.Errorf("%s has no registry class", name)
		}
	}
	out := moduleAuditDeclarations([]*gen.ModuleAuditEventTypeDeclaration{
		{Type: "acme.access.granted", Retention: gen.ModuleAuditEventRetention_MODULE_AUDIT_EVENT_RETENTION_SECURITY},
		{Type: "acme.page.viewed", Retention: gen.ModuleAuditEventRetention_MODULE_AUDIT_EVENT_RETENTION_CONTENT},
		{Type: "acme.page.opened"},
	})
	declared, err := business.ValidateAuditEventTypeDeclarations("acme", out)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	want := map[business.EventType]business.AuditRetentionClass{
		"acme.access.granted": business.RetentionSecurity,
		"acme.page.viewed":    business.RetentionContent,
		"acme.page.opened":    business.RetentionContent,
	}
	for _, d := range declared {
		if d.Retention != want[d.Type] {
			t.Errorf("%s retention = %q, want %q", d.Type, d.Retention, want[d.Type])
		}
	}
}

// A wire enum is open: a newer client can send a value this build has never
// heard of. Reading it as UNSPECIFIED would file a type its author meant to
// keep for the compliance window under the shorter content window — evidence
// that expires early with no refusal anywhere. A manifest that misspells its
// retention is refused, and so is the wire.
func TestModuleAuditDeclarations_RefuseAnEnumValueThisBuildDoesNotKnow(t *testing.T) {
	unknownRetention := gen.ModuleAuditEventRetention(99)
	_, err := business.ValidateAuditEventTypeDeclarations("acme", moduleAuditDeclarations([]*gen.ModuleAuditEventTypeDeclaration{
		{Type: "acme.access.granted", Retention: unknownRetention},
	}))
	if err == nil {
		t.Fatal("an unknown retention class was filed under the default")
	}
	if !strings.Contains(err.Error(), "retention") {
		t.Errorf("the refusal does not name the retention: %v", err)
	}

	unknownVisibility := gen.ModuleAuditEventVisibility(99)
	_, err = business.ValidateAuditEventTypeDeclarations("acme", moduleAuditDeclarations([]*gen.ModuleAuditEventTypeDeclaration{
		{Type: "acme.access.granted", Visibility: unknownVisibility},
	}))
	if err == nil {
		t.Fatal("an unknown visibility was filed under the default")
	}
	if !strings.Contains(err.Error(), "visibility") {
		t.Errorf("the refusal does not name the visibility: %v", err)
	}

	// UNSPECIFIED stays the default it is documented to be.
	if _, err := business.ValidateAuditEventTypeDeclarations("acme", moduleAuditDeclarations([]*gen.ModuleAuditEventTypeDeclaration{
		{Type: "acme.access.granted"},
	})); err != nil {
		t.Errorf("an undeclared retention and visibility are refused: %v", err)
	}
}
