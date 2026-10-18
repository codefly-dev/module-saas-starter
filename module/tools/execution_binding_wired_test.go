package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A SECURITY MECHANISM NOTHING CALLS IS INDISTINGUISHABLE FROM ONE THAT PASSES,
// and this is the gate on that.
//
// The defect it closes shipped, and it is the one a reviewer is least likely to
// find by reading the mechanism: execution-bound minting — `BindExecution` and
// the TokenReview client — was built, documented, guarded and unit-tested, and
// `SetExecutionBinding` was called from NOWHERE. On a running host the reviewer
// was nil, `BindExecution` answered ErrExecutionUnbound for every caller, and
// the whole mechanism was unreachable. Every unit test passed, because every
// unit test wired the sources itself.
//
// That is why this gate exists at the CALL SITE rather than on the mechanism.
// Reading `module_execution_binding.go` tells you the check is correct; it
// cannot tell you the check runs. Nothing in a code review distinguishes
// "wired" from "wireable", and the deletion that unwires it is a one-line
// change that compiles, passes every test, and silently removes a boundary.
//
// WHY NOT-NIL IS PART OF THE RULE. This repository deliberately wires one
// mechanism with nil arguments — `service.SetPolicyLog(nil, nil)` — and says why
// in a long comment: a host holding a log it can never reach is worse than a
// host holding none. So "the setter is called" is not the property; the property
// is that it is called with real sources. A future edit that reduced
// SetExecutionBinding to `SetExecutionBinding(nil)` would satisfy a grep
// for the call and restore the hole exactly.
func TestExecutionBindingIsWiredInProduction(t *testing.T) {
	moduleDir := findModuleDir(t)
	work := filepath.Join(moduleDir, "services", "accounts", "code", "work.go")

	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, work, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse work.go: %v", err)
	}

	var calls []*ast.CallExpr
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "SetExecutionBinding" {
			calls = append(calls, call)
		}
		return true
	})

	if len(calls) == 0 {
		t.Fatal("work.go never calls SetExecutionBinding, so on a running host the execution reviewer is nil.\n\n" +
			"BindExecution then answers ErrExecutionUnbound for every caller and the whole execution-binding " +
			"mechanism is unreachable — while every unit test of it still passes, because each wires the " +
			"sources itself. That is the state this gate exists to refuse.\n\n" +
			"Wire it where the reviewer exists: the Kubernetes client that reviews a delivery carrier.")
	}

	for _, call := range calls {
		if len(call.Args) != 1 {
			t.Fatalf("SetExecutionBinding is called with %d arguments; it takes the workload reviewer",
				len(call.Args))
		}
		for _, argument := range call.Args {
			which := "the workload reviewer"
			if identifier, ok := argument.(*ast.Ident); ok && identifier.Name == "nil" {
				t.Fatalf("SetExecutionBinding is called with nil for %s at %s.\n\n"+
					"A nil source makes BindExecution answer ErrExecutionUnbound for every caller, which is the "+
					"same observable as not wiring it at all — so this reads as enforcement while enforcing "+
					"nothing.\n\n"+
					"This repository DOES wire SetPolicyLog(nil, nil) deliberately, and explains why in place. "+
					"If there is an equivalent argument for leaving execution binding unwired, it belongs in a "+
					"comment and in this gate, not in a silent nil.",
					which, fileSet.Position(argument.Pos()))
			}
		}
	}

	// NON-VACUITY. Everything above would also pass if the call sat in a
	// function nothing reaches — a `func wireLater()` nobody calls satisfies the
	// AST exactly as the real wiring does, and is the second shape of the same
	// defect. So the enclosing function must be one the service's own startup
	// path reaches.
	enclosing := enclosingFunction(parsed, calls[0].Pos())
	if enclosing == "" {
		t.Fatal("SetExecutionBinding is called outside any function in work.go")
	}
	if enclosing == "init" {
		return
	}
	// The call must be in a function something else in work.go CALLS, so a dead
	// helper cannot satisfy this gate.
	if !callsFunction(parsed, enclosing) {
		t.Fatalf("SetExecutionBinding is wired inside %q, which nothing else in work.go calls.\n\n"+
			"A call in a function the startup path never reaches is the same hole as no call at all, and it "+
			"passes an AST check identically.", enclosing)
	}
}

// callsFunction reports whether any call in the file names `function` from
// OUTSIDE that function's own declaration.
//
// A CallExpr walk, because the substring count this replaced asserted the wrong
// thing. It required `enclosing+"("` to appear at least twice in work.go, which
// is satisfied by a doc comment mentioning `wireLater(`, or by a second
// declaration sharing the name as a prefix — so it checked "the name appears
// twice", not "something calls this", while the gate's entire purpose is the
// second claim. It survived its mutations and held for the tree as it stood,
// which is exactly why it was worth replacing rather than trusting.
//
// A call from inside the function itself does not count: recursion says nothing
// about whether anything reaches it.
func callsFunction(file *ast.File, function string) bool {
	var own *ast.FuncDecl
	for _, declaration := range file.Decls {
		if declared, ok := declaration.(*ast.FuncDecl); ok && declared.Name.Name == function {
			own = declared
			break
		}
	}
	called := false
	ast.Inspect(file, func(node ast.Node) bool {
		if called {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		var named string
		switch invoked := call.Fun.(type) {
		case *ast.Ident:
			named = invoked.Name
		case *ast.SelectorExpr:
			named = invoked.Sel.Name
		}
		if named != function {
			return true
		}
		if own != nil && own.Pos() <= call.Pos() && call.Pos() <= own.End() {
			return true
		}
		called = true
		return false
	})
	return called
}

// enclosingFunction names the function declaration containing a position.
func enclosingFunction(file *ast.File, position token.Pos) string {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if function.Pos() <= position && position <= function.End() {
			return function.Name.Name
		}
	}
	return ""
}

// TestEveryModuleMintIsExecutionBound is the gate on #1056: every way a module
// obtains a Work Context goes through BindExecution, and nothing else proves a
// module's identity.
//
// The wired gate above holds that BindExecution has real sources; this one holds
// that the mints CALL it. Both are needed — the mechanism was once wired and
// called by nothing, and a mint that skipped it would pass every unit test of
// the mechanism. The chain is checked link by link, by call graph rather than
// by name: the identity resolver calls BindExecution; the operation and source
// resolvers call the identity resolver; and the four entrypoints — the three
// mint RPCs and the installer's token route — each call their resolver.
func TestEveryModuleMintIsExecutionBound(t *testing.T) {
	moduleDir := findModuleDir(t)
	code := filepath.Join(moduleDir, "services", "accounts", "code", "pkg")
	links := []struct {
		file, function, callee string
	}{
		{"business/module_identity.go", "ModuleAuthorizeWorkContext", "authorizeModuleIdentity"},
		{"business/module_identity.go", "authorizeModuleIdentity", "BindExecution"},
		{"business/module_operation_context.go", "ModuleAuthorizeOperationContext", "authorizeModuleIdentity"},
		{"business/source_delegation.go", "AuthorizeSourceOperationContext", "authorizeModuleIdentity"},
		{"adapters/module_capabilities_server.go", "MintModuleWorkContext", "ModuleAuthorizeWorkContext"},
		{"adapters/module_capabilities_server.go", "MintModuleOperationContext", "ModuleAuthorizeOperationContext"},
		{"adapters/source_delegation.go", "MintSourceOperationContext", "AuthorizeSourceOperationContext"},
		{"adapters/module_installations_http.go", "token", "ModuleAuthorizeWorkContext"},
		{"adapters/module_capabilities_server.go", "MintModuleWorkContext", "FenceModuleMint"},
		{"adapters/module_capabilities_server.go", "MintModuleOperationContext", "FenceModuleMint"},
		{"adapters/source_delegation.go", "MintSourceOperationContext", "FenceModuleMint"},
		{"adapters/module_installations_http.go", "token", "FenceModuleMint"},
	}
	for _, link := range links {
		path := filepath.Join(code, link.file)
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", link.file, err)
		}
		if !functionCalls(parsed, link.function, link.callee) {
			t.Errorf("%s: %s does not call %s.\n\n"+
				"Every module mint is authenticated by the pod's projected ServiceAccount token through "+
				"BindExecution — TokenReview, the pod's running image, the approved build. A mint that "+
				"skips the link issues a capability to whoever reaches it.",
				link.file, link.function, link.callee)
		}
	}
}

// functionCalls reports whether the named function's body contains a call to
// callee (as a bare name or a selector) that EXECUTES when the function runs.
//
// A call inside a function literal counts only when that literal is itself
// invoked where it stands (`func() { ... }()`). Review round 1 found the gate
// accepting `_ = func() { service.ModuleAuthorizeWorkContext(...) }` followed by
// an unchecked mint: the identity call was syntactically present and never ran.
// A literal handed to something else (a goroutine, a callback, a deferred
// call) is not followed either — whether it runs is no longer this function's
// to say — so this gate stays supplemental to the behavioural tests that drive
// each entrypoint (TestEveryModuleMintIsBoundToTheRunningBuild in accounts).
func functionCalls(file *ast.File, function, callee string) bool {
	for _, declaration := range file.Decls {
		declared, ok := declaration.(*ast.FuncDecl)
		if !ok || declared.Name.Name != function || declared.Body == nil {
			continue
		}
		return blockCalls(declared.Body, callee)
	}
	return false
}

// blockCalls walks node for a call to callee, entering a function literal only
// where it is invoked in place.
func blockCalls(node ast.Node, callee string) bool {
	found := false
	ast.Inspect(node, func(current ast.Node) bool {
		if found {
			return false
		}
		switch typed := current.(type) {
		case *ast.FuncLit:
			// Reached only when not invoked in place: skipped.
			return false
		case *ast.DeferStmt, *ast.GoStmt:
			// Runs after the function returns, or concurrently with it: never
			// before the mint it would have to gate.
			return false
		case *ast.CallExpr:
			switch invoked := typed.Fun.(type) {
			case *ast.Ident:
				found = invoked.Name == callee
			case *ast.SelectorExpr:
				found = invoked.Sel.Name == callee
			case *ast.FuncLit:
				// Invoked in place: its body runs, so walk it, then the
				// arguments, and do not let Inspect reach the literal again.
				if blockCalls(invoked.Body, callee) {
					found = true
					return false
				}
				for _, argument := range typed.Args {
					if blockCalls(argument, callee) {
						found = true
						return false
					}
				}
				return false
			}
		}
		return !found
	})
	return found
}

// TestTheMintGateIgnoresAClosureThatNeverRuns is the review's probe, kept: a
// call in a literal nobody invokes satisfied the gate.
func TestTheMintGateIgnoresAClosureThatNeverRuns(t *testing.T) {
	for source, want := range map[string]bool{
		"func token() {\n _ = func() { service.ModuleAuthorizeWorkContext() }\n mint()\n}":                           false,
		"func token() {\n go func() { service.ModuleAuthorizeWorkContext() }()\n mint()\n}":                          false,
		"func token() {\n defer func() { service.ModuleAuthorizeWorkContext() }()\n mint()\n}":                       false,
		"func token() {\n func() { service.ModuleAuthorizeWorkContext() }()\n mint()\n}":                             true,
		"func token() {\n if _, err := service.ModuleAuthorizeWorkContext(); err != nil {\n  return\n }\n mint()\n}": true,
	} {
		parsed, err := parser.ParseFile(token.NewFileSet(), "example.go", "package example\n"+source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := functionCalls(parsed, "token", "ModuleAuthorizeWorkContext"); got != want {
			t.Errorf("functionCalls = %v, want %v for:\n%s", got, want, source)
		}
	}
}

// TestNoModuleOrSolutionSecretIsRead holds the deletion (#1056): the three
// secret maps, their parsing and the header that carried a module's secret have
// no reader in either service, the configuration declares none of them, and the
// two registration mints they authenticated are gone from the contract.
//
// It reads CODE, comments stripped, because the failure it guards is a reader
// coming back — a configuration lookup, a header read — not prose that names
// what was deleted.
func TestNoModuleOrSolutionSecretIsRead(t *testing.T) {
	moduleDir := findModuleDir(t)
	forbidden := []string{
		"MODULE_IDENTITY_SECRETS", "MODULE_REGISTRATION_SECRETS", "SOLUTION_REGISTRATION_SECRETS",
		"MODULE_IDENTITY_SECRET", "MODULE_IDENTITY_PREFIX",
		"ParseRegistrationSecrets", "SetModuleIdentitySecrets", "SetModuleRegistrar", "SetSolutionRegistrar",
		"X-Codefly-Module-Secret", "MintModuleRegistration", "MintSolutionRegistration",
	}
	for _, root := range []string{"services/accounts/code", "services/auth-gateway/code"} {
		err := filepath.WalkDir(filepath.Join(moduleDir, root), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if name := entry.Name(); name == "gen" || name == "node_modules" || name == "external" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			code := codeOnly(readFileOrFail(t, path))
			for _, name := range forbidden {
				if strings.Contains(code, name) {
					relative, _ := filepath.Rel(moduleDir, path)
					t.Errorf("%s reads %s, which #1056 deleted: a module proves which module it is with its "+
						"pod's projected ServiceAccount token, held against its running build — never a secret.",
						relative, name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	// The configuration declares none of the keys: a `KEY=` line, not the
	// comment that says they are gone.
	for _, line := range strings.Split(readFileOrFail(t, filepath.Join(moduleDir, "configurations/local/federation.env")), "\n") {
		for _, name := range []string{"MODULE_IDENTITY_SECRETS", "MODULE_REGISTRATION_SECRETS", "SOLUTION_REGISTRATION_SECRETS"} {
			if strings.HasPrefix(strings.TrimSpace(line), name+"=") {
				t.Errorf("configurations/local/federation.env still declares %s (#1056 deleted it)", name)
			}
		}
	}
	for _, path := range []string{
		"services/accounts/proto/saas/accounts/v1/module_capabilities.proto",
		"services/accounts/proto/saas/accounts/v1/module_registration.proto",
	} {
		content := codeOnly(readFileOrFail(t, filepath.Join(moduleDir, path)))
		for _, name := range []string{"rpc MintModuleRegistration", "rpc MintSolutionRegistration", "string secret"} {
			if strings.Contains(content, name) {
				t.Errorf("%s still declares %q (#1056 deleted it)", path, name)
			}
		}
	}
}
