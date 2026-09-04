package adapters

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// handlersRoutingThroughRequireRoleScope discovers, from the handler source, the
// PermissionService RPCs whose handler gates through requireRoleScope — the
// global-scope escape hatch (org admin on the request's org, OR platform admin on
// an empty org). It scans every non-test source file in the handler package (not
// just rpcs.go) so a handler added or moved to another file cannot slip past the
// drift guard and silently classify ok. It reads the handlers themselves so the
// classifier's escape set cannot diverge from the routes it is meant to describe.
func handlersRoutingThroughRequireRoleScope(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	var methods []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoErrorf(t, err, "parse %s", name)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			routes := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "requireRoleScope" {
					routes = true
					return false
				}
				return true
			})
			if !routes {
				continue
			}
			require.Equalf(t, "PermServer", receiverTypeName(fn.Recv.List[0].Type),
				"requireRoleScope caller %s is not on PermServer; extend this test's service mapping", fn.Name.Name)
			methods = append(methods, "/saas.accounts.v1.PermissionService/"+fn.Name.Name)
		}
	}
	sort.Strings(methods)
	return methods
}

// Every handler that routes through requireRoleScope must classify as gap and be
// left to the handler by the central interceptor. A newly added requireRoleScope
// route that is missing from the escape set would classify ok (centrally
// enforced) and fail here, catching the drift before the enforce flip could
// over-deny it.
func TestGlobalScopeEscapeHatchMatchesHandlers(t *testing.T) {
	methods := handlersRoutingThroughRequireRoleScope(t)
	require.NotEmpty(t, methods, "no requireRoleScope handlers found — the scan is broken")

	for _, method := range methods {
		policy, ok := business.LookupRPCPolicy(method)
		require.Truef(t, ok, "%s is not classified", method)
		require.Equalf(t, business.CoverageGap, business.ClassifyCentralCoverage(policy),
			"%s routes through the requireRoleScope global-scope escape and must classify gap, not be centrally enforced", method)
		_, enforced := business.CentralTenantEnforcement(policy)
		require.Falsef(t, enforced, "%s must not be centrally enforced (global-scope escape)", method)
	}

	// Control: a clean org-admin method with no escape stays centrally enforced,
	// so the escape carve-out has not over-broadened into ordinary org-tenant RPCs.
	addMember, ok := business.LookupRPCPolicy(orgAdminMethod)
	require.True(t, ok)
	require.Equal(t, business.CoverageOK, business.ClassifyCentralCoverage(addMember))
	_, enforced := business.CentralTenantEnforcement(addMember)
	require.True(t, enforced)
}

// The regression the escape hatch exists to avoid: under enforce mode the central
// interceptor must defer the requireRoleScope family to the handler rather than
// enforce the declared org-admin floor. With no membership and no platform role
// in the store — and even with no verified org at all (the platform-admin
// global-scope case) — enforceCentralPolicy must be a no-op for these routes,
// where a clean org-admin method would deny.
func TestEnforceDefersGlobalScopeEscapeMethods(t *testing.T) {
	enableEnforcement(t)
	installEnforceService(t, &enforceStore{})

	methods := handlersRoutingThroughRequireRoleScope(t)
	require.NotEmpty(t, methods)

	ctxWithOrg := enforceActorCtx()
	ctxNoOrg := stampVerifiedIdentity(context.Background(), enforceActorID, "", auth.Assurance{})
	for _, method := range methods {
		require.NoErrorf(t, enforceCentralPolicy(ctxWithOrg, method), "%s must defer to the handler under enforce", method)
		require.NoErrorf(t, enforceCentralPolicy(ctxNoOrg, method), "%s must not over-deny a caller with no verified org", method)
	}
}
