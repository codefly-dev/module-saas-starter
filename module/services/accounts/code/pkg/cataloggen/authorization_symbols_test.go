package cataloggen_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "accounts/pkg/gen/saas/accounts/v1"
)

// authorization.proto waives MESSAGE_NO_DELETE and RPC_NO_DELETE in
// proto/buf.yaml, because moving the accessible-scopes shapes out of it into
// accessible_scopes.proto trips both even though the package, the
// fully-qualified names and the wire format are unchanged. buf ignores are
// path-scoped, so those waivers cannot be narrowed to the symbols that moved:
// with them in place buf no longer catches ANY deletion from this file, and it
// is the file that defines the authorization API.
//
// These lists restore that guard at the symbol level. They are the inventory of
// authorization.proto at the commit the waivers were added, minus the four
// symbols this move relocated — which are asserted to live in
// accessible_scopes.proto instead, so the move stays a move and cannot decay
// into a deletion.
//
// The assertions are one-directional on purpose: a symbol added to the file
// needs no edit here, exactly as buf's deletion rules behave. Removing an entry
// is how a deliberate deletion is declared, and it should be as deliberate as
// removing the buf rule would have been.
var authorizationMessages = []string{
	"AssignRoleRequest",
	"AssignRoleResponse",
	"CheckAccessRequest",
	"CheckAccessResponse",
	"CheckPermissionRequest",
	"CheckPermissionResponse",
	"CreateAgentPrincipalRequest",
	"CreateRoleRequest",
	"CreateRoleResponse",
	"DecideRequest",
	"DecideResponse",
	"DeleteRoleRequest",
	"DisableAgentPrincipalRequest",
	"EnableAgentPrincipalRequest",
	"GetAgentPrincipalRequest",
	"GetPrincipalRequest",
	"GrantScopeRequest",
	"GrantScopeResponse",
	"ListAccessibleScopesRequest",
	"ListPrincipalsRequest",
	"ListPrincipalsResponse",
	"ListRoleAssignmentsRequest",
	"ListRoleAssignmentsResponse",
	"ListRolesRequest",
	"ListRolesResponse",
	"ListSharesRequest",
	"ListSharesResponse",
	"RecordShare",
	"RegisterScopeNodeRequest",
	"RegisterScopeNodeResponse",
	"RevokePrincipalRequest",
	"RevokeRoleRequest",
	"RevokeScopeRequest",
	"RevokeShareRequest",
	"ScopeGrant",
	"ScopeNode",
	"ShareRecordRequest",
	"ShareRecordResponse",
}

var authorizationRPCs = map[string][]string{
	"PermissionService": {
		"AssignRole",
		"CheckAccess",
		"CheckPermission",
		"CreateRole",
		"Decide",
		"DeleteRole",
		"GrantScope",
		"ListAccessibleScopes",
		"ListRoleAssignments",
		"ListRoles",
		"ListShares",
		"RegisterScopeNode",
		"RevokeRole",
		"RevokeScope",
		"RevokeShare",
		"ShareRecord",
	},
	"PrincipalService": {
		"CreateAgentPrincipal",
		"DisableAgentPrincipal",
		"EnableAgentPrincipal",
		"GetAgentPrincipal",
		"GetPrincipal",
		"ListPrincipals",
		"RevokePrincipal",
	},
}

// The symbols the accessible-scopes split relocated. Asserting where they landed
// is what makes the shorter authorizationMessages/authorizationRPCs lists above a
// record of a move rather than a licence to delete.
var relocatedAccessibleScopeMessages = []string{
	"AccessibleScope",
	"ListAccessibleScopesResponse",
	"ListMyAccessibleScopesRequest",
}

func protoFile(t *testing.T, path string) protoreflect.FileDescriptor {
	t.Helper()
	file, err := protoregistry.GlobalFiles.FindFileByPath(path)
	require.NoErrorf(t, err, "%s is not registered", path)
	return file
}

func messageNames(file protoreflect.FileDescriptor) []string {
	var names []string
	for i := 0; i < file.Messages().Len(); i++ {
		names = append(names, string(file.Messages().Get(i).Name()))
	}
	sort.Strings(names)
	return names
}

func serviceMethodNames(file protoreflect.FileDescriptor) map[string][]string {
	out := map[string][]string{}
	for i := 0; i < file.Services().Len(); i++ {
		service := file.Services().Get(i)
		var methods []string
		for j := 0; j < service.Methods().Len(); j++ {
			methods = append(methods, string(service.Methods().Get(j).Name()))
		}
		sort.Strings(methods)
		out[string(service.Name())] = methods
	}
	return out
}

func TestAuthorizationProtoRetainsEveryDeclaredMessage(t *testing.T) {
	present := map[string]struct{}{}
	for _, name := range messageNames(protoFile(t, "saas/accounts/v1/authorization.proto")) {
		present[name] = struct{}{}
	}
	var deleted []string
	for _, name := range authorizationMessages {
		if _, ok := present[name]; !ok {
			deleted = append(deleted, name)
		}
	}
	require.Emptyf(t, deleted,
		"messages deleted from authorization.proto: %v — buf's MESSAGE_NO_DELETE is waived for this file, so this test is the only guard. Move them and record the new home, or delete this entry deliberately.",
		deleted)
}

func TestAuthorizationProtoRetainsEveryDeclaredRPC(t *testing.T) {
	present := serviceMethodNames(protoFile(t, "saas/accounts/v1/authorization.proto"))
	for service, methods := range authorizationRPCs {
		got, ok := present[service]
		require.Truef(t, ok, "service %s was deleted from authorization.proto", service)
		index := map[string]struct{}{}
		for _, name := range got {
			index[name] = struct{}{}
		}
		var deleted []string
		for _, name := range methods {
			if _, ok := index[name]; !ok {
				deleted = append(deleted, name)
			}
		}
		require.Emptyf(t, deleted,
			"RPCs deleted from %s in authorization.proto: %v — buf's RPC_NO_DELETE is waived for this file, so this test is the only guard.",
			service, deleted)
	}
}

func TestRelocatedAccessibleScopeMessagesLiveInTheirNewFile(t *testing.T) {
	present := map[string]struct{}{}
	for _, name := range messageNames(protoFile(t, "saas/accounts/v1/accessible_scopes.proto")) {
		present[name] = struct{}{}
	}
	var missing []string
	for _, name := range relocatedAccessibleScopeMessages {
		if _, ok := present[name]; !ok {
			missing = append(missing, name)
		}
	}
	require.Emptyf(t, missing,
		"messages moved out of authorization.proto are absent from accessible_scopes.proto: %v — the move became a deletion.",
		missing)
}
