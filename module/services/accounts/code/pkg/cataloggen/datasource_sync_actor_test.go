package cataloggen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"accounts/pkg/cataloggen"
	accountsv1 "accounts/pkg/gen/saas/accounts/v1"
)

// actorFieldMarkers name a field that identifies the principal behind an action
// rather than an attribute of the record itself. "user"/"subject" are in the set
// because the natural spellings of this field avoid "_by" entirely
// (last_sync_user_id), and "initiator"/"requester" because they avoid both.
var actorFieldMarkers = []string{
	"_by", "actor", "principal", "triggered", "initiator", "requester", "user", "subject",
}

// docs/adr/0008-datasource-sync-actor-provenance.md keeps the actor of a
// datasource sync in the audit trail. The asymmetry that decision rests on is
// enforceable: SyncSource is ORG_ADMIN, reading who performed it goes through
// QueryAuditLog's audit:read gate, and ListSources/GetSource project Datasource
// to any org member under no permission at all. An actor field on Datasource
// would hand an admin action's principal to every member without the gate ever
// being consulted.
//
// This is the conditional form of the invariant, not a freeze: such a field may
// exist once the methods projecting it require audit:read — at which point ADR
// 0008 is due a superseding decision anyway.
func TestDatasourceProjectionCarriesNoUngatedActorField(t *testing.T) {
	fields := actorProvenanceFields((&accountsv1.Datasource{}).ProtoReflect().Descriptor())
	catalog, err := cataloggen.BuildAuthorizationCatalog(readFixture(t, "../../../generated/service-catalog.json"))
	require.NoError(t, err)

	datasourceReads := []string{
		"/saas.accounts.v1.DatasourceService/ListSources",
		"/saas.accounts.v1.DatasourceService/GetSource",
	}
	for _, procedure := range datasourceReads {
		method := methodByProcedure(catalog, procedure)
		require.NotNil(t, method, procedure)
		gated := containsPermission(method.GetPolicy().GetPermissions(), "audit:read")

		if len(fields) > 0 {
			require.True(t, gated,
				"Datasource projects actor provenance (%s) but %s declares no audit:read: "+
					"see docs/adr/0008-datasource-sync-actor-provenance.md",
				strings.Join(fields, ", "), procedure)
			continue
		}
		// Both halves of the invariant are asserted, so neither state leaves the
		// test green having checked nothing. Datasource carrying no actor field is
		// only half of ADR 0008; the other half is that these reads stay ungated.
		// If one acquires audit:read, the classification premise the decision
		// rests on has changed and the ADR is due a revisit — exactly the trigger
		// its "Revisiting" section names.
		require.False(t, gated,
			"%s now requires audit:read, so the premise ADR 0008 rests on has changed: "+
				"re-read docs/adr/0008-datasource-sync-actor-provenance.md before relying on it",
			procedure)
	}
}

// The audit trail's own projection is the control: AuditEvent carries the actor
// and QueryAuditLog gates it, which is exactly what the datasource read does not
// do. It also keeps the marker list honest — a marker set that matched nothing
// would let the test above pass vacuously.
func TestAuditProjectionGatesItsActorField(t *testing.T) {
	fields := actorProvenanceFields((&accountsv1.AuditEvent{}).ProtoReflect().Descriptor())
	require.NotEmpty(t, fields, "AuditEvent declares no actor field; actorFieldMarkers no longer matches actor provenance")

	catalog, err := cataloggen.BuildAuthorizationCatalog(readFixture(t, "../../../generated/service-catalog.json"))
	require.NoError(t, err)
	method := methodByProcedure(catalog, "/saas.accounts.v1.AuditService/QueryAuditLog")
	require.NotNil(t, method)
	require.Contains(t, method.GetPolicy().GetPermissions(), "audit:read",
		"QueryAuditLog projects %s without audit:read", strings.Join(fields, ", "))
}

// actorProvenanceFields walks the message's whole field tree, not just its direct
// fields: Datasource already nests four provider-config messages, so a
// `DatasourceSyncInfo last_sync` carrying the actor one level down is the
// idiomatic shape here and a single-level scan would never see it. Returned names
// are dotted paths so the failure names the nesting.
func actorProvenanceFields(message protoreflect.MessageDescriptor) []string {
	return appendActorProvenanceFields(nil, message, "", map[protoreflect.FullName]bool{})
}

func appendActorProvenanceFields(
	out []string,
	message protoreflect.MessageDescriptor,
	prefix string,
	// Guards against a message that reaches itself; a proto field graph may be
	// cyclic even though a serialized message is not.
	visiting map[protoreflect.FullName]bool,
) []string {
	if visiting[message.FullName()] {
		return out
	}
	visiting[message.FullName()] = true
	defer delete(visiting, message.FullName())

	fields := message.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		path := prefix + string(field.Name())
		if matchesActorMarker(string(field.Name())) {
			out = append(out, path)
		}
		// A map field's value carries the payload; its synthetic entry message's
		// "key"/"value" names never match a marker, so recurse through the value.
		if field.IsMap() {
			if value := field.MapValue(); value.Kind() == protoreflect.MessageKind {
				out = appendActorProvenanceFields(out, value.Message(), path+".", visiting)
			}
			continue
		}
		if field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind {
			out = appendActorProvenanceFields(out, field.Message(), path+".", visiting)
		}
	}
	return out
}

// containsPermission is a local membership check rather than require.Contains so
// the result can be branched on, which the two-sided assertion above needs.
func containsPermission(permissions []string, want string) bool {
	for _, permission := range permissions {
		if permission == want {
			return true
		}
	}
	return false
}

func matchesActorMarker(name string) bool {
	for _, marker := range actorFieldMarkers {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}
