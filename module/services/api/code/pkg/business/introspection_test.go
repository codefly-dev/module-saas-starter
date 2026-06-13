package business_test

import (
	"context"
	"sort"
	"testing"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"api/pkg/business"
	"api/pkg/gen"
)

// authedCtx returns a context that GetServiceInfo will treat as
// authenticated (callerIsAuthenticated returns true). Used by
// drift-guard tests so the redaction pass doesn't strip privileged
// RPCs.
//
// wool's WithUserAuthID mutates the Wool's internal ctx; we extract
// it back via w.Context() so subsequent wool.Get(ctx) sees the value.
func authedCtx() context.Context {
	w := wool.Get(context.Background())
	w.WithUserAuthID("test-authed-user")
	return w.Context()
}

// TestIntrospection_GetServiceInfo — pins the catalog endpoint
// shape. The RPC list derives from gRPC service descriptors at
// runtime; this test asserts:
//
//   - Service info (name="api", module="saas-starter", version) set.
//   - Every RLS-protected table has fail_closed = true.
//   - Public RPCs are advertised as such.
//   - Built-in 'admin' role holds the wildcard permission.
func TestIntrospection_GetServiceInfo(t *testing.T) {
	resp, err := testService.GetServiceInfo(authedCtx(), &gen.GetServiceInfoRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	caps := resp.Capabilities
	require.NotNil(t, caps)
	require.NotNil(t, caps.Info)

	require.Equal(t, "api", caps.Info.Name)
	require.Equal(t, "saas-starter", caps.Info.Module)
	require.NotEmpty(t, caps.Info.Version)
	require.NotEmpty(t, caps.Rpcs)
	require.NotEmpty(t, caps.Permissions)
	require.NotEmpty(t, caps.RlsTables)
	require.NotEmpty(t, caps.Scopes)

	// Every RLS table claims fail-closed semantics — the load-bearing
	// safety property of the BeforeAcquire pattern.
	for _, tbl := range caps.RlsTables {
		require.True(t, tbl.FailClosed,
			"table %q must declare fail_closed=true", tbl.Table)
		require.NotEmpty(t, tbl.Table)
		require.Contains(t,
			[]string{"direct", "join", "polymorphic", "self_referential"},
			tbl.PolicyShape,
			"table %q has unexpected policy_shape %q", tbl.Table, tbl.PolicyShape)
	}

	// admin has wildcard.
	var adminWildcard bool
	for _, p := range caps.Permissions {
		if p.Resource == "*" && p.Action == "*" {
			for _, r := range p.BuiltInRoles {
				if r == "admin" {
					adminWildcard = true
				}
			}
		}
	}
	require.True(t, adminWildcard,
		"built-in 'admin' role must hold wildcard *:* permission")
}

// TestIntrospection_NoMissingMetadata — drift guard. The RPC list
// is auto-derived from gRPC service descriptors; per-RPC metadata
// is hand-maintained in introspection.go's rpcMetadata. This test
// asserts every (Service, Method) the proto exposes has a metadata
// entry — adding a new RPC without filling metadata fails loud.
func TestIntrospection_NoMissingMetadata(t *testing.T) {
	resp, err := testService.GetServiceInfo(authedCtx(), &gen.GetServiceInfoRequest{})
	require.NoError(t, err)

	var missing []string
	for _, r := range resp.Capabilities.Rpcs {
		if r.HandlerAuthz == "" {
			missing = append(missing, r.Service+"/"+r.Method)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("RPCs without metadata in introspection.go's rpcMetadata map (%d):\n  %v\nAdd entries.", len(missing), missing)
	}
}

// TestIntrospection_PublicRPCsAdvertised — the gRPC auth interceptor
// publicGrpcMethods skip-list and the catalog must agree on which
// RPCs are public.
func TestIntrospection_PublicRPCsAdvertised(t *testing.T) {
	resp, err := testService.GetServiceInfo(authedCtx(), &gen.GetServiceInfoRequest{})
	require.NoError(t, err)

	got := map[string]string{}
	for _, r := range resp.Capabilities.Rpcs {
		got[r.Service+"/"+r.Method] = r.HandlerAuthz
	}

	for _, key := range []string{
		"IntrospectionService/GetServiceInfo",
		"UserService/Version",
		"AuthService/GetJWKS",
		"AuthService/Authenticate",
		"AuthService/RefreshToken",
		"AuthService/Logout",
		"PermissionService/CheckPermission",
	} {
		require.Equal(t, "public", got[key],
			"RPC %s must be advertised as handler_authz=public", key)
	}
}

// TestIntrospection_RPCListMatchesDescriptors — strongest drift
// guard. Builds the expected (service, method) set from gRPC
// descriptors and compares to the response.
func TestIntrospection_RPCListMatchesDescriptors(t *testing.T) {
	resp, err := testService.GetServiceInfo(authedCtx(), &gen.GetServiceInfoRequest{})
	require.NoError(t, err)

	wanted := map[string]struct{}{}
	for _, sd := range testServiceDescs() {
		svc := stripPkg(sd.ServiceName)
		for _, m := range sd.Methods {
			wanted[svc+"/"+m.MethodName] = struct{}{}
		}
	}

	got := map[string]struct{}{}
	for _, r := range resp.Capabilities.Rpcs {
		got[r.Service+"/"+r.Method] = struct{}{}
	}

	var missing []string
	for k := range wanted {
		if _, ok := got[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing,
		"catalog response missing %d RPCs that exist in proto: %v", len(missing), missing)

	var extra []string
	for k := range got {
		if _, ok := wanted[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	require.Empty(t, extra,
		"catalog response includes %d RPCs that don't exist in proto: %v", len(extra), extra)
}

// TestIntrospection_UnauthenticatedRedacts — anonymous callers
// should not see platform_admin / mfa-tier rows.
func TestIntrospection_UnauthenticatedRedacts(t *testing.T) {
	anonResp, err := testService.GetServiceInfo(context.Background(), &gen.GetServiceInfoRequest{})
	require.NoError(t, err)
	for _, r := range anonResp.Capabilities.Rpcs {
		require.NotEqual(t, "platform_admin", r.HandlerAuthz,
			"anonymous response must not include platform_admin RPCs (got %s)", r.Service+"/"+r.Method)
		require.NotEqual(t, "mfa", r.HandlerAuthz,
			"anonymous response must not include mfa-tier RPCs (got %s)", r.Service+"/"+r.Method)
	}
	require.NotEmpty(t, anonResp.Capabilities.Rpcs,
		"anonymous still gets non-privileged RPCs")
}

// testServiceDescs mirrors introspection.go's allServiceDescs for
// the test pkg. Divergence will trip
// TestIntrospection_RPCListMatchesDescriptors — which is the right
// thing to fail on.
func testServiceDescs() []*grpc.ServiceDesc {
	return []*grpc.ServiceDesc{
		&gen.IntrospectionService_ServiceDesc,
		&gen.AuthService_ServiceDesc,
		&gen.UserService_ServiceDesc,
		&gen.OrganizationService_ServiceDesc,
		&gen.TeamService_ServiceDesc,
		&gen.PermissionService_ServiceDesc,
		&gen.IdentityService_ServiceDesc,
		&gen.APIKeyService_ServiceDesc,
		&gen.AuditService_ServiceDesc,
		&gen.AuditExportService_ServiceDesc,
		&gen.InvitationService_ServiceDesc,
		&gen.WebhookService_ServiceDesc,
		&gen.NotificationService_ServiceDesc,
		&gen.OnboardingService_ServiceDesc,
		&gen.GDPRService_ServiceDesc,
		&gen.ConsentService_ServiceDesc,
		&gen.SSOAdminService_ServiceDesc,
		&gen.BillingService_ServiceDesc,
		&gen.UserSettingsService_ServiceDesc,
		&gen.MFAService_ServiceDesc,
		&gen.PlatformAdminService_ServiceDesc,
	}
}

func stripPkg(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return s[i+1:]
		}
	}
	return s
}

var _ = business.ServiceVersion // keep business import alive
