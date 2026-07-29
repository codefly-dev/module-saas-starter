package cataloggen_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"accounts/pkg/cataloggen"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
)

func TestFrontendCatalogIsDeterministicAndCurrent(t *testing.T) {
	catalogDocument := readFixture(t, "../../../generated/service-catalog.json")
	first, err := cataloggen.RenderFrontendCatalog(catalogDocument)
	require.NoError(t, err)
	second, err := cataloggen.RenderFrontendCatalog(catalogDocument)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, string(readFixture(t, "../../../../frontend/code/src/gen/saas/accounts/v1/frontend_catalog.ts")), string(first), "run: go generate ./pkg/cataloggen")

	source := string(first)
	require.Equal(t, 26, strings.Count(source, ": createClient("))
	require.Contains(t, source, `ROLES_WRITE: "roles:write"`)
	require.Contains(t, source, `API_CALLS_MONTHLY: "api_calls_monthly"`)
	require.Contains(t, source, "export type PermissionGrant")
	require.Contains(t, source, "export interface AccountsClients")
}

func TestFrontendCatalogRejectsUnsafeOrUnrepresentableInput(t *testing.T) {
	document := readFixture(t, "../../../generated/service-catalog.json")
	catalog := &catalogv1.ServiceCatalog{}
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(document, catalog))

	unknownPermission := proto.Clone(catalog).(*catalogv1.ServiceCatalog)
	unknownPermission.Methods[0].Policy.Permissions = append(unknownPermission.Methods[0].Policy.Permissions, "unknown:read")
	unknownDocument, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(unknownPermission)
	require.NoError(t, err)
	_, err = cataloggen.RenderFrontendCatalog(unknownDocument)
	require.ErrorContains(t, err, "unknown permission")

	badSource := proto.Clone(catalog).(*catalogv1.ServiceCatalog)
	badSource.Methods[0].SourceProto = "other/package/api_keys.proto"
	badSourceDocument, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(badSource)
	require.NoError(t, err)
	_, err = cataloggen.RenderFrontendCatalog(badSourceDocument)
	require.ErrorContains(t, err, "spans TypeScript source modules")

	collision := proto.Clone(catalog).(*catalogv1.ServiceCatalog)
	collision.Permissions = append(collision.Permissions,
		&catalogv1.PermissionDefinition{Permission: "foo:bar_read", Resource: "foo", Action: "bar_read", Description: "Collision one."},
		&catalogv1.PermissionDefinition{Permission: "foo_bar:read", Resource: "foo_bar", Action: "read", Description: "Collision two."},
	)
	sort.Slice(collision.Permissions, func(i, j int) bool {
		return collision.Permissions[i].GetPermission() < collision.Permissions[j].GetPermission()
	})
	collisionDocument, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(collision)
	require.NoError(t, err)
	_, err = cataloggen.RenderFrontendCatalog(collisionDocument)
	require.ErrorContains(t, err, "collide on TypeScript identifier")
}
