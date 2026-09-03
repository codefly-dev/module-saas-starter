package adapters

// Lockstep gate: bind each RPC's declared authorization floor to a real
// enforcement site in its handler. The declaration-only coverage gate
// (tools/authz-coverage-gate.mjs) proves a policy is coherent and
// non-broadening, but nothing proved the handler actually runs the matching
// require* check — a route could declare TENANT_REQUIREMENT_ORG_ADMIN (or an
// owned-resource / platform-role / MFA floor) and simply forget the gate, and
// every other test stayed green. This gate parses the handler package and
// fails closed when a declared floor has no covering require* call reachable
// from the handler. Deliberate exceptions live in
// authz_enforcement_allowlist.json, mirroring the coverage-gate allowlist.
//
// A method is served by up to two functions that share its name and request
// type — a Connect handler and the inner gRPC *Server method it delegates to —
// and either may hold the enforcement, so the gate unions the capabilities of
// both. Enforcement reached through an intra-package wrapper (a plain function
// such as requireRoleScope, or a same-receiver method such as authorizeOwner)
// is followed via the call graph. Permissions and scopes are intentionally out
// of scope: an org permission is subsumed by the tenant gate that establishes
// membership, and the API-key scope ceiling is a separate requireScope concern,
// neither of which maps to a distinct require* site the way the
// tenant/platform-role/MFA floor does.

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

// capability is one authorization guarantee a require* site establishes. A
// declared floor is the set of capabilities a handler must provide; enforcement
// is present when the handler's reachable require* calls provide a superset.
type capability string

const (
	capOrgMember capability = "org_member"
	capOrgAdmin  capability = "org_admin"
	capPlatform  capability = "platform_role"
	capTeam      capability = "team"
	capMFACond   capability = "mfa_if_enrolled" // satisfied by the strict step-up too
	capMFAStrict capability = "mfa_recent_step_up"
)

// enforcementSeeds maps each base require* helper to the capabilities it
// establishes intrinsically (by role/lookup comparison, not by delegating to
// another helper). Every other function's capabilities are derived by
// propagating these along the intra-package call graph, so a handler that gates
// through a local wrapper is still credited. requireOrgAdmin/requireBillingAdmin
// also establish membership; requireRecentMFA is strictly stronger than the
// if-enrolled step-up, so it provides both MFA capabilities.
func enforcementSeeds() map[string]map[capability]bool {
	set := func(caps ...capability) map[capability]bool {
		m := make(map[capability]bool, len(caps))
		for _, c := range caps {
			m[c] = true
		}
		return m
	}
	return map[string]map[capability]bool{
		"requireOrgMember":     set(capOrgMember),
		"requireOrgPermission": set(capOrgMember),
		"requireOrgAdmin":      set(capOrgMember, capOrgAdmin),
		"requireBillingAdmin":  set(capOrgMember, capOrgAdmin),
		"requireTeamMember":    set(capOrgMember, capTeam),
		"requireTeamAdmin":     set(capOrgMember, capOrgAdmin, capTeam),
		"requirePlatformAdmin": set(capPlatform),
		"requirePlatformRole":  set(capPlatform),
		"requireMFA":           set(capMFACond),
		"requireRecentMFA":     set(capMFACond, capMFAStrict),
	}
}

// requiredCapabilities is the declared floor a handler must enforce, above the
// exposure tier, for an authenticated method. Public and internal methods, and
// authenticated methods whose floor reduces to "a verified user", require
// nothing.
func requiredCapabilities(p *policyv1.MethodPolicy) map[capability]bool {
	need := map[capability]bool{}
	if p.GetExposure() != policyv1.Exposure_EXPOSURE_AUTHENTICATED {
		return need
	}
	if p.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE {
		need[capPlatform] = true
	}
	switch p.GetTenant() {
	case policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN,
		policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_OWNER:
		need[capOrgAdmin] = true
	case policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_MEMBER:
		need[capOrgMember] = true
	case policyv1.TenantRequirement_TENANT_REQUIREMENT_TEAM_MEMBER:
		need[capOrgMember] = true
		need[capTeam] = true
	}
	for _, binding := range p.GetResourceBindings() {
		if binding.GetTarget() == policyv1.ResourceTarget_RESOURCE_TARGET_TEAM {
			need[capTeam] = true
		}
	}
	switch p.GetMfa() {
	case policyv1.MFARequirement_MFA_REQUIREMENT_RECENT_STEP_UP:
		need[capMFAStrict] = true
	case policyv1.MFARequirement_MFA_REQUIREMENT_IF_ENROLLED_RECENT_STEP_UP:
		need[capMFACond] = true
	}
	return need
}

// handlerFunc is one parsed function or method: its call-graph node id and the
// request types its parameters name (used to disambiguate methods that share a
// name across services).
type handlerFunc struct {
	node     string
	genTypes map[string]bool
}

// handlerScan is the enforcement view of the handler package: the capabilities
// every function provides, and the functions that serve each method name.
type handlerScan struct {
	provided map[string]map[capability]bool
	byName   map[string][]handlerFunc
}

// capabilitiesFor unions the provided capabilities of every function that
// serves method with a parameter naming reqType (the Connect handler and the
// inner gRPC method both qualify), and reports whether any handler was found.
func (s handlerScan) capabilitiesFor(method, reqType string) (map[capability]bool, bool) {
	caps := map[capability]bool{}
	found := false
	for _, fn := range s.byName[method] {
		if !fn.genTypes[reqType] {
			continue
		}
		found = true
		for c := range s.provided[fn.node] {
			caps[c] = true
		}
	}
	return caps, found
}

// scanHandlers parses the non-test Go sources in dir and computes, for every
// function, the capabilities its reachable require* calls provide.
func scanHandlers(dir string) (handlerScan, error) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return handlerScan{}, err
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return handlerScan{}, err
		}
		files = append(files, file)
	}
	return buildScan(files), nil
}

// buildScan computes, for every function in files, the capabilities its
// reachable require* calls provide, and indexes functions by method name.
func buildScan(files []*ast.File) handlerScan {
	// callees is keyed by call-graph node id: a plain function is its name; a
	// method is "<ReceiverType>.<Method>". Values are the node ids it calls
	// intra-package — plain-identifier calls and same-receiver method calls.
	callees := map[string][]string{}
	byName := map[string][]handlerFunc{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			node := funcNodeID(fn)
			callees[node] = calledNodes(fn)
			byName[fn.Name.Name] = append(byName[fn.Name.Name], handlerFunc{
				node:     node,
				genTypes: typesInParams(fn.Type.Params),
			})
		}
	}

	resolve := newCapabilityResolver(enforcementSeeds(), callees)
	provided := make(map[string]map[capability]bool, len(callees))
	for node := range callees {
		provided[node] = resolve(node)
	}
	return handlerScan{provided: provided, byName: byName}
}

// newCapabilityResolver returns a memoized resolver for a node's provided
// capabilities: a seeded require* helper yields its seed directly; any other
// function yields the union over the nodes it calls. Recursion is cycle-guarded.
func newCapabilityResolver(
	seeds map[string]map[capability]bool,
	callees map[string][]string,
) func(string) map[capability]bool {
	memo := map[string]map[capability]bool{}
	inProgress := map[string]bool{}
	var resolve func(string) map[capability]bool
	resolve = func(node string) map[capability]bool {
		if caps, ok := seeds[node]; ok {
			return caps
		}
		if caps, ok := memo[node]; ok {
			return caps
		}
		if inProgress[node] {
			return map[capability]bool{}
		}
		inProgress[node] = true
		caps := map[capability]bool{}
		for _, callee := range callees[node] {
			for c := range resolve(callee) {
				caps[c] = true
			}
		}
		delete(inProgress, node)
		memo[node] = caps
		return caps
	}
	return resolve
}

func funcNodeID(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return receiverTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

// receiverTypeName returns the bare type name of a method receiver, unwrapping
// the pointer (handler receivers are *XServer).
func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// calledNodes returns the intra-package call-graph edges out of fn: names of
// plain-identifier calls (require* helpers and package functions), plus
// same-receiver method calls (e.g. s.authorizeOwner) resolved to their
// "<ReceiverType>.<Method>" node so wrapper methods are followed.
func calledNodes(fn *ast.FuncDecl) []string {
	if fn.Body == nil {
		return nil
	}
	var recvVar, recvType string
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		recvType = receiverTypeName(fn.Recv.List[0].Type)
		if names := fn.Recv.List[0].Names; len(names) > 0 {
			recvVar = names[0].Name
		}
	}
	seen := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			seen[fun.Name] = true
		case *ast.SelectorExpr:
			if recvVar != "" {
				if base, ok := fun.X.(*ast.Ident); ok && base.Name == recvVar {
					seen[recvType+"."+fun.Sel.Name] = true
				}
			}
		}
		return true
	})
	nodes := make([]string, 0, len(seen))
	for node := range seen {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	return nodes
}

// typesInParams returns the qualified type names referenced by a parameter
// list, covering both `*pkg.FooRequest` and `*connect.Request[pkg.FooRequest]`.
// The package alias varies (gen for accounts types, jobsv1 for job types), so
// only the type name is kept and matched against the catalog's request type.
func typesInParams(params *ast.FieldList) map[string]bool {
	out := map[string]bool{}
	if params == nil {
		return out
	}
	for _, field := range params.List {
		ast.Inspect(field.Type, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				out[sel.Sel.Name] = true
			}
			return true
		})
	}
	return out
}

// loadEnforcementAllowlist indexes the ticketed exemptions by procedure. A
// malformed entry cannot silently exempt a route: both a reason and a ticket
// are mandatory, matching the coverage-gate allowlist contract.
func loadEnforcementAllowlist(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc struct {
		Enforcement []struct {
			Procedure string `json:"procedure"`
			Reason    string `json:"reason"`
			Ticket    string `json:"ticket"`
		} `json:"enforcement"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	allow := map[string]bool{}
	for _, entry := range doc.Enforcement {
		if entry.Procedure != "" && entry.Reason != "" && entry.Ticket != "" {
			allow[entry.Procedure] = true
		}
	}
	return allow
}

func sortedCaps(caps map[capability]bool) []string {
	out := make([]string, 0, len(caps))
	for c := range caps {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return out
}

// TestDeclaredPolicyEnforcedInLockstep fails closed when a method declares an
// authorization floor above its exposure tier but no handler enforces it. This
// is the lock-matches-the-label half the declaration-only coverage gate cannot
// see.
func TestDeclaredPolicyEnforcedInLockstep(t *testing.T) {
	scan, err := scanHandlers(".")
	require.NoError(t, err)
	allow := loadEnforcementAllowlist(t, "authz_enforcement_allowlist.json")

	var violations []string
	checked := 0
	for _, policy := range business.RPCPolicies() {
		if policy.PolicyError != "" || !policy.Tier.Valid() {
			continue // excluded from admission; the coverage gate owns these
		}
		need := requiredCapabilities(policy.MethodPolicy)
		if len(need) == 0 || allow[policy.FullMethod] {
			continue
		}
		checked++
		reqType := policy.InputType[strings.LastIndex(policy.InputType, ".")+1:]
		provided, found := scan.capabilitiesFor(policy.Method, reqType)
		if !found {
			violations = append(violations, fmt.Sprintf(
				"%s: no handler found for method %s(%s)", policy.FullMethod, policy.Method, reqType))
			continue
		}
		var missing []string
		for c := range need {
			if !provided[c] {
				missing = append(missing, string(c))
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			violations = append(violations, fmt.Sprintf(
				"%s: declares floor [%s] but handler enforces only [%s] (missing: %s)",
				policy.FullMethod, strings.Join(sortedCaps(need), " "),
				strings.Join(sortedCaps(provided), " "), strings.Join(missing, " ")))
		}
	}
	sort.Strings(violations)
	require.Empty(t, violations, "declared authorization floors with no matching handler enforcement "+
		"(add the require* gate, or a ticketed authz_enforcement_allowlist.json entry):\n%s",
		strings.Join(violations, "\n"))

	// Guard against the filter silently excluding everything: the fine-grained
	// floor covers the large majority of authenticated RPCs.
	require.Greater(t, checked, 80, "expected the lockstep to evaluate the fine-grained-floor RPCs")
}

// --- unit tests for the gate machinery -------------------------------------

func mkPolicy(overrides func(*policyv1.MethodPolicy)) *policyv1.MethodPolicy {
	p := &policyv1.MethodPolicy{
		Exposure:     policyv1.Exposure_EXPOSURE_AUTHENTICATED,
		Tenant:       policyv1.TenantRequirement_TENANT_REQUIREMENT_USER,
		Mfa:          policyv1.MFARequirement_MFA_REQUIREMENT_NONE,
		PlatformRole: policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE,
	}
	overrides(p)
	return p
}

func TestRequiredCapabilities(t *testing.T) {
	cases := []struct {
		name string
		p    *policyv1.MethodPolicy
		want []capability
	}{
		{"a verified user needs no floor", mkPolicy(func(p *policyv1.MethodPolicy) {}), nil},
		{"public needs no floor", mkPolicy(func(p *policyv1.MethodPolicy) {
			p.Exposure = policyv1.Exposure_EXPOSURE_PUBLIC
			p.Tenant = policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE
		}), nil},
		{"org admin", mkPolicy(func(p *policyv1.MethodPolicy) {
			p.Tenant = policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN
		}), []capability{capOrgAdmin}},
		{"org member", mkPolicy(func(p *policyv1.MethodPolicy) {
			p.Tenant = policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_MEMBER
		}), []capability{capOrgMember}},
		{"platform role", mkPolicy(func(p *policyv1.MethodPolicy) {
			p.PlatformRole = policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_SUPER_ADMIN
			p.Tenant = policyv1.TenantRequirement_TENANT_REQUIREMENT_NONE
		}), []capability{capPlatform}},
		{"strict mfa on org admin", mkPolicy(func(p *policyv1.MethodPolicy) {
			p.Tenant = policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN
			p.Mfa = policyv1.MFARequirement_MFA_REQUIREMENT_RECENT_STEP_UP
		}), []capability{capOrgAdmin, capMFAStrict}},
		{"conditional mfa", mkPolicy(func(p *policyv1.MethodPolicy) {
			p.Mfa = policyv1.MFARequirement_MFA_REQUIREMENT_IF_ENROLLED_RECENT_STEP_UP
		}), []capability{capMFACond}},
		{"team binding on org admin", mkPolicy(func(p *policyv1.MethodPolicy) {
			p.Tenant = policyv1.TenantRequirement_TENANT_REQUIREMENT_ORG_ADMIN
			p.ResourceBindings = []*policyv1.ResourceBinding{
				{Target: policyv1.ResourceTarget_RESOURCE_TARGET_TEAM},
			}
		}), []capability{capOrgAdmin, capTeam}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.ElementsMatch(t, capSlice(tc.want), sortedCaps(requiredCapabilities(tc.p)))
		})
	}
}

func capSlice(caps []capability) []string {
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = string(c)
	}
	sort.Strings(out)
	return out
}

// scanSource parses one synthetic package and returns its enforcement scan.
func scanSource(t *testing.T, src string) handlerScan {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	require.NoError(t, err)
	return buildScan([]*ast.File{file})
}

// The scanner must credit enforcement whether it is called directly, through a
// plain-function wrapper, or through a same-receiver method — and must credit
// nothing to a handler that only authenticates, so a forgotten gate is caught.
// The inner gRPC method and its Connect wrapper are unioned by request type.
func TestScanHandlersCreditsReachableEnforcement(t *testing.T) {
	scan := scanSource(t, `
package adapters

func (s *AServer) Direct(ctx context.Context, req *gen.DirectRequest) error {
	requireOrgAdmin(ctx)
	return nil
}

func (s *AServer) ViaPlainWrapper(ctx context.Context, req *gen.WrapRequest) error {
	authorizeMember(ctx)
	return nil
}
func authorizeMember(ctx context.Context) { requireOrgMember(ctx) }

func (s *AServer) ViaMethodWrapper(ctx context.Context, req *gen.MethodRequest) error {
	s.owner(ctx)
	return nil
}
func (s *AServer) owner(ctx context.Context) { requireTeamMember(ctx) }

func (s *AServer) Ungated(ctx context.Context, req *gen.UngatedRequest) error {
	requireAuth(ctx)
	return nil
}

func (h *aConnectHandler) Ungated(ctx context.Context, req *connect.Request[gen.UngatedRequest]) error {
	return unary(ctx, req, h.inner.Ungated)
}
func (h *aConnectHandler) Delegated(ctx context.Context, req *connect.Request[gen.DelegatedRequest]) error {
	requireRecentMFA(ctx)
	return nil
}
`)

	assertCaps := func(method, reqType string, want ...capability) {
		t.Helper()
		got, found := scan.capabilitiesFor(method, reqType)
		require.True(t, found, "%s(%s) should be found", method, reqType)
		require.ElementsMatch(t, capSlice(want), sortedCaps(got))
	}

	assertCaps("Direct", "DirectRequest", capOrgMember, capOrgAdmin)
	assertCaps("ViaPlainWrapper", "WrapRequest", capOrgMember)
	assertCaps("ViaMethodWrapper", "MethodRequest", capOrgMember, capTeam)
	assertCaps("Delegated", "DelegatedRequest", capMFACond, capMFAStrict)

	// A handler that only authenticates provides no floor capability, so an
	// org-admin declaration over it would be flagged — the fail-closed case.
	ungated, found := scan.capabilitiesFor("Ungated", "UngatedRequest")
	require.True(t, found)
	require.Empty(t, sortedCaps(ungated))

	// An unknown method is reported as not found rather than silently passing.
	_, found = scan.capabilitiesFor("DoesNotExist", "NoRequest")
	require.False(t, found)
}

// A method's Connect wrapper carries no gate while the inner gRPC method holds
// it; unioning by request type must credit the pair.
func TestScanHandlersUnionsConnectAndInner(t *testing.T) {
	scan := scanSource(t, `
package adapters

func (s *AServer) Guarded(ctx context.Context, req *gen.GuardedRequest) error {
	requireOrgAdmin(ctx)
	return nil
}
func (h *aConnectHandler) Guarded(ctx context.Context, req *connect.Request[gen.GuardedRequest]) error {
	return unary(ctx, req, h.inner.Guarded)
}
`)
	got, found := scan.capabilitiesFor("Guarded", "GuardedRequest")
	require.True(t, found)
	require.ElementsMatch(t, capSlice([]capability{capOrgMember, capOrgAdmin}), sortedCaps(got))
}

func TestEnforcementAllowlistRejectsMalformedEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allow.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"enforcement":[
		{"procedure":"/s/A","reason":"r","ticket":"#1"},
		{"procedure":"/s/B","reason":"","ticket":"#2"},
		{"procedure":"/s/C","reason":"r"}
	]}`), 0o600))
	allow := loadEnforcementAllowlist(t, path)
	require.True(t, allow["/s/A"])
	require.False(t, allow["/s/B"], "an entry without a reason must not exempt")
	require.False(t, allow["/s/C"], "an entry without a ticket must not exempt")
}
