package adapters

import (
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
