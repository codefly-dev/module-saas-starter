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
// rather than an attribute of the record itself.
var actorFieldMarkers = []string{"_by", "actor", "principal", "triggered"}

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
	fields := actorProvenanceFields((&accountsv1.Datasource{}).ProtoReflect().Descriptor().Fields())
	if len(fields) == 0 {
		return
	}
	catalog, err := cataloggen.BuildAuthorizationCatalog(readFixture(t, "../../../generated/service-catalog.json"))
	require.NoError(t, err)

	for _, procedure := range []string{
		"/saas.accounts.v1.DatasourceService/ListSources",
		"/saas.accounts.v1.DatasourceService/GetSource",
	} {
		method := methodByProcedure(catalog, procedure)
		require.NotNil(t, method, procedure)
		require.Contains(t, method.GetPolicy().GetPermissions(), "audit:read",
			"Datasource projects actor provenance (%s) but %s declares no audit:read: "+
				"see docs/adr/0008-datasource-sync-actor-provenance.md",
			strings.Join(fields, ", "), procedure)
	}
}

// The audit trail's own projection is the control: AuditEvent carries the actor
// and QueryAuditLog gates it, which is exactly what the datasource read does not
// do. It also keeps the marker list honest — a marker set that matched nothing
// would let the test above pass vacuously.
func TestAuditProjectionGatesItsActorField(t *testing.T) {
	fields := actorProvenanceFields((&accountsv1.AuditEvent{}).ProtoReflect().Descriptor().Fields())
	require.NotEmpty(t, fields, "AuditEvent declares no actor field; actorFieldMarkers no longer matches actor provenance")

	catalog, err := cataloggen.BuildAuthorizationCatalog(readFixture(t, "../../../generated/service-catalog.json"))
	require.NoError(t, err)
	method := methodByProcedure(catalog, "/saas.accounts.v1.AuditService/QueryAuditLog")
	require.NotNil(t, method)
	require.Contains(t, method.GetPolicy().GetPermissions(), "audit:read",
		"QueryAuditLog projects %s without audit:read", strings.Join(fields, ", "))
}

func actorProvenanceFields(fields protoreflect.FieldDescriptors) []string {
	var out []string
	for i := 0; i < fields.Len(); i++ {
		name := string(fields.Get(i).Name())
		for _, marker := range actorFieldMarkers {
			if strings.Contains(name, marker) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}
