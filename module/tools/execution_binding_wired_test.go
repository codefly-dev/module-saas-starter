package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// A SECURITY MECHANISM NOTHING CALLS IS INDISTINGUISHABLE FROM ONE THAT PASSES,
// and this is the gate on that.
//
// The defect it closes shipped, and it is the one a reviewer is least likely to
// find by reading the mechanism: execution-bound minting — `BindExecution`, the
// three separate digest types, the monotonicity guard on the approved build,
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
// SetExecutionBinding to `SetExecutionBinding(nil, nil)` would satisfy a grep
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
		t.Fatal("work.go never calls SetExecutionBinding, so on a running host the execution reviewer and the " +
			"approved-build authority are both nil.\n\n" +
			"BindExecution then answers ErrExecutionUnbound for every caller and the whole execution-binding " +
			"mechanism is unreachable — while every unit test of it still passes, because each wires the " +
			"sources itself. That is the state this gate exists to refuse.\n\n" +
			"Wire it where both independent sources exist: the Kubernetes client that reviews a delivery " +
			"carrier, and the reconciler's ApprovedBuilds().")
	}

	for _, call := range calls {
		if len(call.Args) != 2 {
			t.Fatalf("SetExecutionBinding is called with %d arguments; it takes the reviewer and the authority",
				len(call.Args))
		}
		for position, argument := range call.Args {
			which := []string{"the execution reviewer", "the approved-build authority"}[position]
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
	source := readFileOrFail(t, work)
	// The call must be in a function that something else in work.go calls, so a
	// dead helper cannot satisfy this gate.
	if !strings.Contains(source, enclosing+"(") || countOccurrences(source, enclosing+"(") < 2 {
		t.Fatalf("SetExecutionBinding is wired inside %q, which nothing else in work.go calls.\n\n"+
			"A call in a function the startup path never reaches is the same hole as no call at all, and it "+
			"passes an AST check identically.", enclosing)
	}
}

// TestTheExecutionReviewerAndAuthorityAreDifferentSources is the gate on the
// property that makes the check mean anything.
//
// THE TAUTOLOGY IS THE FAILURE MODE HERE. Filling both sides of "approved ==
// running" from one source compares a value to itself: it type-checks, it
// passes for every caller including the superseded pod the check exists to
// refuse, and no test fails. The two arguments must therefore come from
// genuinely different places — the api server for what is running, signed
// documents for what is approved — and a future edit that passed the same
// expression twice, or derived one from the other, would be the defect.
func TestTheExecutionReviewerAndAuthorityAreDifferentSources(t *testing.T) {
	moduleDir := findModuleDir(t)
	work := filepath.Join(moduleDir, "services", "accounts", "code", "work.go")

	source := readFileOrFail(t, work)
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, work, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse work.go: %v", err)
	}

	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "SetExecutionBinding" || len(call.Args) != 2 {
			return true
		}
		reviewer := expressionText(source, fileSet, call.Args[0])
		authority := expressionText(source, fileSet, call.Args[1])
		if reviewer == authority {
			t.Fatalf("SetExecutionBinding is given the same expression for both sources (%q).\n\n"+
				"Then 'approved == running' compares a value to itself. It passes for every caller, including "+
				"a pod from a superseded generation, and nothing fails. The approved digest must come from the "+
				"signed authority documents and the running digest from the api server.", reviewer)
		}
		return true
	})
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

func countOccurrences(haystack, needle string) int {
	return strings.Count(haystack, needle)
}

// expressionText returns the source text of an expression, which is what makes
// "the same expression twice" answerable without reimplementing printing.
func expressionText(source string, fileSet *token.FileSet, expression ast.Expr) string {
	start := fileSet.Position(expression.Pos()).Offset
	end := fileSet.Position(expression.End()).Offset
	if start < 0 || end > len(source) || start >= end {
		return ""
	}
	return strings.TrimSpace(source[start:end])
}
