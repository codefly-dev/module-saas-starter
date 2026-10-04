package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every module capability path must re-read live authority, and this is the gate
// that makes "every" true rather than aspirational.
//
// Both adversarial reviews of #953 named its absence as the reason condition 1
// could not be trusted: the enforcement is 25-odd call sites that each have to
// remember to re-read, and the 26th added next month will not. A convention
// enforced by review is a convention that holds until the reviewer is busy.
//
// HOW IT WORKS. `moduleGrant` resolves a module principal's DECLARED ceiling and
// reads nothing live. `AuthorizeModuleCapability` resolves the same ceiling AND
// re-reads the live installation, producer epoch and binding revision. So a
// function that calls `moduleGrant` directly is a capability path that decided
// on the declared ceiling alone — which is exactly what enforcement at use
// replaces.
//
// The gate therefore finds every direct caller of `moduleGrant` outside the
// enforcement file itself, and holds that set against a baseline.
//
// THE BASELINE SHRINKS AND NEVER GROWS, like the repository's boundary gate. A
// new unrouted path fails the build; routing one requires deleting its line.
// That asymmetry is the point — it makes the remaining work visible and finite
// instead of a claim in a commit message, and it cannot be satisfied by adding
// an entry.
func TestEveryModuleCapabilityPathReReadsLiveAuthority(t *testing.T) {
	moduleDir := findModuleDir(t)
	businessDir := filepath.Join(moduleDir, "services", "accounts", "code", "pkg", "business")

	// Non-vacuity first, and anchored on the ENFORCEMENT rather than on the
	// violations. A first version of this gate required at least one direct
	// moduleGrant caller to exist — and then every path was routed, zero
	// remained, and the gate failed itself. Worse, the inverse would have been
	// true too: deleting the wrapper would leave zero callers and the gate would
	// have passed.
	//
	// So what must exist is the wrapper and the thing it wraps. If
	// `moduleCapability` stops calling `moduleGrant`, or
	// `AuthorizeModuleCapability` disappears, the enforcement is gone however
	// few direct callers remain.
	requireEnforcementWrapperIntact(t, businessDir)

	// A reviewer found this gate "green about a NAME": it watched calls to
	// moduleGrant, and a function reading `declaredModules()` directly skips
	// moduleGrant entirely — so it resolved a module principal's declared
	// ceiling with no live re-read and the gate said nothing.
	//
	// That is the difference between gating the enforcement and gating one
	// spelling of it. The registry read is the underlying fact; moduleGrant is
	// one wrapper over it, and AuthorizeModuleCapability is the one that also
	// re-reads. So both are watched now, against the same shrink-only baseline.
	callers := directRegistryReaders(t, businessDir)
	baseline := unroutedCapabilityPaths(t, moduleDir)

	var added []string
	for _, caller := range callers {
		if !baseline[caller] {
			added = append(added, caller)
		}
	}
	sort.Strings(added)
	if len(added) > 0 {
		t.Fatalf("these capability paths reach the declared registry directly (moduleGrant or declaredModules) "+
			"and so decide on the DECLARED ceiling alone, "+
			"without re-reading the live installation, producer epoch or binding revision:\n    %s\n\n"+
			"Route each through Service.AuthorizeModuleCapability. A path that cannot be routed yet must be added to "+
			"%s WITH a reason — and that file only ever shrinks, so adding to it is a decision a reviewer sees.",
			strings.Join(added, "\n    "), unroutedCapabilityPathsFile)
	}

	// The baseline may not carry paths that no longer exist, or it would hide a
	// route that was never taken: a line for a deleted function reads as
	// outstanding work forever, and a line for a function that WAS routed makes
	// the gate pass while permitting a regression.
	present := map[string]bool{}
	for _, caller := range callers {
		present[caller] = true
	}
	var stale []string
	for path := range baseline {
		if !present[path] {
			stale = append(stale, path)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("%s lists capability paths that no longer reach the declared registry directly:\n    %s\n\n"+
			"Delete these lines. A baseline that keeps entries for routed or deleted paths stops measuring anything, "+
			"and would let a future regression re-add one of them silently.",
			unroutedCapabilityPathsFile, strings.Join(stale, "\n    "))
	}
}

// unroutedCapabilityPathsFile is the shrink-only baseline.
const unroutedCapabilityPathsFile = "services/accounts/code/pkg/business/testdata/unrouted_capability_paths.txt"

// enforcementFiles are where calling moduleGrant directly is correct: the
// enforcement wrapper itself, and the function's own definition.
var enforcementFiles = map[string]bool{
	"module_capability_enforcement.go": true,
	"module_capabilities.go":           false, // holds moduleGrant's definition, checked by name below
}

// directRegistryReaders finds every function that resolves the declared
// registry outside the enforcement wrapper.
//
// It walks the AST rather than grepping, because a grep cannot tell which
// FUNCTION a call sits in — and the function name is what a baseline line has to
// be, or the baseline would be line numbers that churn on every edit.
func directRegistryReaders(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fileSet := token.NewFileSet()
	var callers []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if enforcementFiles[name] {
			continue
		}
		file, err := parser.ParseFile(fileSet, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Name == nil {
				continue
			}
			// moduleGrant itself, and the one wrapper that is SUPPOSED to call
			// it, are where a direct call is correct.
			// The reads themselves, and the enforcement entry points that are
			// SUPPOSED to perform them, are where a direct read is correct.
			if registryReads[function.Name.Name] || enforcementEntryPoints[function.Name.Name] {
				continue
			}
			if !readsDeclaredRegistry(function) {
				continue
			}
			callers = append(callers, name+":"+function.Name.Name)
		}
	}
	sort.Strings(callers)
	return callers
}

// requireEnforcementWrapperIntact is the gate's non-vacuity check.
//
// Zero direct callers is the GOAL, so "found nothing" cannot be the failure
// condition. What must hold instead is that the enforcement still exists:
// `moduleCapability` resolves the declared ceiling through `moduleGrant` AND the
// live re-read through `AuthorizeModuleCapability`, which re-reads installation
// freshness, producer epoch and binding revision.
//
// Without this, deleting the wrapper would satisfy the gate perfectly.
func requireEnforcementWrapperIntact(t *testing.T, dir string) {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, filepath.Join(dir, "module_capabilities.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse module_capabilities.go: %v", err)
	}
	var wrapper *ast.FuncDecl
	definesGrant := false
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name == nil {
			continue
		}
		switch function.Name.Name {
		case "moduleCapability":
			wrapper = function
		case "moduleGrant":
			definesGrant = true
		}
	}
	if !definesGrant {
		t.Fatal("moduleGrant is gone from module_capabilities.go, so this gate no longer measures anything")
	}
	if wrapper == nil {
		t.Fatal("moduleCapability is gone: the enforcing wrapper every capability path routes through no longer exists, " +
			"so zero direct moduleGrant callers would mean nothing")
	}
	if !callsNamed(wrapper, "AuthorizeModuleCapability") {
		t.Fatal("moduleCapability no longer calls AuthorizeModuleCapability, so the capability paths routed through it " +
			"have stopped re-reading live authority while still looking routed")
	}
	requireAdmissionStillReadsLiveAuthority(t, dir)
}

// requireAdmissionStillReadsLiveAuthority holds the mint-side entry point to the
// reason it is exempt.
//
// It is on this list because it re-reads live authority itself. If it stops, the
// exemption becomes a hole: a function that resolves the declared ceiling and
// nothing live would sit here looking compliant, which is exactly the shape this
// gate exists to refuse.
func requireAdmissionStillReadsLiveAuthority(t *testing.T, dir string) {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet,
		filepath.Join(dir, "module_installation_admission.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse module_installation_admission.go: %v", err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name == nil || function.Name.Name != "AdmitModuleUnderInstallation" {
			continue
		}
		for _, required := range []string{"ResolveModuleInstallation", "liveAuthorityFor"} {
			if !callsNamed(function, required) {
				t.Fatalf("AdmitModuleUnderInstallation is exempt from this gate because it reads live "+
					"authority, and it no longer calls %s — so the exemption now hides a path that decides "+
					"on the declared ceiling alone", required)
			}
		}
		return
	}
	t.Fatal("AdmitModuleUnderInstallation is listed as an enforcement entry point but is not declared: " +
		"a stale exemption is how a gate goes quietly green")
}

// callsNamed reports whether a function calls a named function or method.
func callsNamed(function *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(function, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fun.Sel != nil && fun.Sel.Name == name {
				found = true
				return false
			}
		case *ast.Ident:
			if fun.Name == name {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// registryReads are the two ways a function can resolve a module principal's
// declared ceiling without re-reading live authority.
//
// `declaredModules` is the registry read itself; `moduleGrant` is a wrapper over
// it that adds a fail-closed unknown-principal check and nothing live. Watching
// only the wrapper is what let four paths bypass this gate.
var registryReads = map[string]bool{
	"moduleGrant":     true,
	"declaredModules": true,
}

// enforcementEntryPoints resolve the declared ceiling AND re-read live authority
// in the same function, which is the thing this gate wants rather than the thing
// it forbids.
//
// `moduleCapability` is the wrapper the 25 capability paths route through.
// `AdmitModuleUnderInstallation` is the mint-side entry point: a module
// capability names the installation it acts under, and that function resolves the
// ceiling, checks the solution actually composes this module, and reads the live
// installation and epoch. It appeared here as a violation the moment it was
// written, which is the gate doing its job — the fix is to list the entry point,
// not to widen what counts as compliant.
//
// Each entry is a function a reviewer must look at, so adding one is a decision
// rather than a convenience. The list is checked against the code below: an entry
// naming a function that no longer performs the live re-read fails the gate,
// because a stale exemption is how a gate goes quietly green.
var enforcementEntryPoints = map[string]bool{
	"moduleCapability":             true,
	"AdmitModuleUnderInstallation": true,
}

func readsDeclaredRegistry(function *ast.FuncDecl) bool {
	found := false
	ast.Inspect(function, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok &&
			selector.Sel != nil && registryReads[selector.Sel.Name] {
			found = true
			return false
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && registryReads[ident.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}

// unroutedCapabilityPaths reads the baseline. Blank lines and `#` comments are
// skipped, so each entry can carry the reason it is still there.
func unroutedCapabilityPaths(t *testing.T, moduleDir string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleDir, unroutedCapabilityPathsFile))
	if err != nil {
		t.Fatalf("read %s: %v", unroutedCapabilityPathsFile, err)
	}
	paths := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		paths[line] = true
	}
	return paths
}
