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
// an empty org). It scans every non-test source file in the handler package and
// follows the intra-package call graph (the same gating-aware edges the lockstep
// gate uses, via calledNodes), so a handler that reaches requireRoleScope through
// a wrapper — not only a direct call — is still found. That the discovered set
// cannot drift from the routes the classifier's escape set names is asserted by
// TestGlobalScopeEscapeHatchMatchesHandlers.
func handlersRoutingThroughRequireRoleScope(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	callees := map[string][]string{}
	methodDecls := map[string]*ast.FuncDecl{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoErrorf(t, err, "parse %s", name)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			node := funcNodeID(fn)
			callees[node] = calledNodes(fn)
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				methodDecls[node] = fn
			}
		}
	}

	// reaches reports whether a call-graph node can reach requireRoleScope through
	// gating calls. Cycles resolve to false, matching the lockstep resolver.
	reaches := map[string]bool{}
	inProgress := map[string]bool{}
	var visit func(string) bool
	visit = func(node string) bool {
		if result, ok := reaches[node]; ok {
			return result
		}
		if inProgress[node] {
			return false
		}
		inProgress[node] = true
		result := false
		for _, callee := range callees[node] {
			if callee == "requireRoleScope" || visit(callee) {
				result = true
				break
			}
		}
		delete(inProgress, node)
		reaches[node] = result
		return result
	}

	var methods []string
	for node, fn := range methodDecls {
		if !visit(node) {
			continue
		}
		require.Equalf(t, "PermServer", receiverTypeName(fn.Recv.List[0].Type),
			"requireRoleScope handler %s is not on PermServer; extend this test's service mapping", fn.Name.Name)
		full := "/saas.accounts.v1.PermissionService/" + fn.Name.Name
		if _, ok := business.LookupRPCPolicy(full); ok {
			methods = append(methods, full)
		}
	}
	sort.Strings(methods)
	return methods
}

// The classifier's escape set must equal — in both directions — the handlers that
// gate through requireRoleScope. A new requireRoleScope route missing from the set
// would classify ok (centrally enforced) and re-open the over-deny; a set entry
// whose handler no longer gates through requireRoleScope would leave a route
// classified gap that no handler backs. ElementsMatch catches either drift.
func TestGlobalScopeEscapeHatchMatchesHandlers(t *testing.T) {
	methods := handlersRoutingThroughRequireRoleScope(t)
	require.NotEmpty(t, methods, "no requireRoleScope handlers found — the scan is broken")
	require.ElementsMatch(t, business.GlobalScopeEscapeHatchMethods(), methods,
		"the global-scope escape set must equal the RPCs whose handler gates through requireRoleScope")

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
