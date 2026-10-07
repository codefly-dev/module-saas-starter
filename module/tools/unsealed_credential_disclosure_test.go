package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// While an unsealed credential is ACCEPTED, the rules file must say that the
// mechanism enforces nothing.
//
// `checkAgainstSealed` returns on its first line when the seal is nil. Every
// real module credential is unsealed — `StartModuleTask` seals nothing and
// sdk-go's token has no field to seal into — so in a deployment the live read
// runs and its answer is discarded, and the declared grant alone binds.
//
// That is the most dangerous kind of claim to get wrong, because the code looks
// like enforcement: there is a live read, a comparison function, a conjunction of
// terms and tests for each. A reader of `accounts/AGENTS.md` deciding whether
// uninstalling a solution revokes a module capability in flight gets the answer
// from that file, and the answer is no.
//
// This gate has already caught the same paragraph claiming too much three times,
// in three different wordings, so it keys on the CONSEQUENCES rather than on a
// phrase: whichever way the prose is rewritten, it must still say that
// uninstalling does not revoke. When the seal is populated and the nil branch
// stops being permissive, this gate inverts — see the second half.
func TestUnsealedCredentialDisclosureMatchesTheCode(t *testing.T) {
	moduleDir := findModuleDir(t)
	enforcement := filepath.Join(moduleDir, "services", "accounts", "code",
		"pkg", "business", "module_capability_enforcement.go")

	permissive := nilSealReturnsNoError(t, enforcement)

	document := readFileOrFail(t, filepath.Join(moduleDir, "services", "accounts", "AGENTS.md"))
	prose := strings.Join(strings.Fields(document), " ")

	const (
		enforcesNothing = "enforces NOTHING on a module capability"
		uninstallIsNoop = "Uninstalling a solution does not revoke a module capability in flight"
	)

	if permissive {
		for _, required := range []string{enforcesNothing, uninstallIsNoop} {
			if strings.Contains(prose, required) {
				continue
			}
			t.Fatalf("checkAgainstSealed accepts an unsealed credential, so every real module capability is "+
				"decided on the declared grant alone — but accounts/AGENTS.md does not say %q.\n\n"+
				"The code LOOKS like enforcement: a live read, a comparison, a conjunction of terms, a test per "+
				"term. Someone deciding whether an uninstall revokes a capability in flight reads that file, and "+
				"the answer is no. Say so, or populate the seal.", required)
		}
		return
	}

	// The nil branch is no longer permissive. The disclosure is now WRONG in the
	// other direction — understating an enforcement that holds makes the next
	// reader add a redundant check or distrust a real one.
	if strings.Contains(prose, enforcesNothing) {
		t.Fatalf("checkAgainstSealed no longer accepts an unsealed credential, but accounts/AGENTS.md still says "+
			"%q.\n\nRe-read that paragraph against the code: understating an enforcement is its own defect.",
			enforcesNothing)
	}
}

// nilSealReturnsNoError reports whether checkAgainstSealed's first statement is
// a nil check that returns a nil error.
//
// Checked structurally rather than by grepping for "sealed == nil", because the
// property is "an unsealed credential is accepted" and a rewrite that keeps the
// behaviour while changing the spelling must not slip past.
func nilSealReturnsNoError(t *testing.T, path string) bool {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name == nil || function.Name.Name != "checkAgainstSealed" {
			continue
		}
		if function.Body == nil || len(function.Body.List) == 0 {
			t.Fatal("checkAgainstSealed has no body, so this gate cannot judge what an unsealed credential gets")
		}
		branch, ok := function.Body.List[0].(*ast.IfStmt)
		if !ok {
			return false
		}
		for _, statement := range branch.Body.List {
			ret, ok := statement.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			if ident, ok := ret.Results[0].(*ast.Ident); ok && ident.Name == "nil" {
				return true
			}
		}
		return false
	}
	t.Fatal("checkAgainstSealed is not declared: the sealed half of min(sealed, live) is gone, " +
		"and with it the only thing this gate measures")
	return false
}
