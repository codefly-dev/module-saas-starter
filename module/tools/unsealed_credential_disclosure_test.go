package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// The rules file must say what a module credential is actually held to.
//
// `checkAgainstSealed` returns on its first line when the seal is nil. That
// branch is how the host's OWN decisions — a mint resolving the installation it
// is about to seal, a fence, a revision recheck — read live authority with no
// credential in hand. What decides whether a module CREDENTIAL is held to its
// installation is whether verifying one yields a sealed caller:
// `VerifyModuleWorkContext` builds the ModuleCaller every capability path acts
// as, and the core capability it verifies carries the installation, its
// revision and the principal's epoch.
//
// That is the most dangerous kind of claim to get wrong, because the code looks
// the same either way: there is a live read, a comparison function, a
// conjunction of terms and tests for each. A reader of `accounts/AGENTS.md`
// deciding whether uninstalling a solution revokes a module capability in flight
// gets the answer from that file.
//
// This gate caught the same paragraph claiming too much three times, in three
// wordings, while every credential was unsealed — so it keys on the
// CONSEQUENCES rather than on a phrase, in both directions:
//
//   - a credential verifies to an UNSEALED caller and the nil branch is
//     permissive: the file must say that the mechanism enforces nothing on it
//     and that uninstalling does not revoke it;
//   - a credential verifies to a SEALED caller: the file must say so, and must
//     not keep the disclosure that an uninstall leaves it live — understating an
//     enforcement that holds makes the next reader add a redundant check or
//     distrust a real one.
func TestUnsealedCredentialDisclosureMatchesTheCode(t *testing.T) {
	moduleDir := findModuleDir(t)
	enforcement := filepath.Join(moduleDir, "services", "accounts", "code",
		"pkg", "business", "module_capability_enforcement.go")
	verification := filepath.Join(moduleDir, "services", "accounts", "code",
		"pkg", "adapters", "work_context_rpcs.go")

	permissive := nilSealReturnsNoError(t, enforcement)
	sealed := verifiedCallerIsSealed(t, verification)

	document := readFileOrFail(t, filepath.Join(moduleDir, "services", "accounts", "AGENTS.md"))
	prose := strings.Join(strings.Fields(document), " ")

	const (
		enforcesNothing = "enforces NOTHING on a module capability"
		uninstallIsNoop = "Uninstalling a solution does not revoke a module capability in flight"
		credentialsSeal = "Every module credential is sealed"
	)

	if permissive && !sealed {
		for _, required := range []string{enforcesNothing, uninstallIsNoop} {
			if strings.Contains(prose, required) {
				continue
			}
			t.Fatalf("a module credential verifies to an unsealed caller and checkAgainstSealed accepts it, so "+
				"every real module capability is decided on the declared grant alone — but accounts/AGENTS.md "+
				"does not say %q.\n\nSay so, or populate the seal.", required)
		}
		return
	}
	if !sealed {
		return
	}
	for _, stale := range []string{enforcesNothing, uninstallIsNoop} {
		if strings.Contains(prose, stale) {
			t.Fatalf("a module credential now verifies to a SEALED caller, so every use compares the live "+
				"installation against the one it was minted for — but accounts/AGENTS.md still says %q.\n\n"+
				"Re-read that paragraph against the code: understating an enforcement is its own defect.", stale)
		}
	}
	if !strings.Contains(prose, credentialsSeal) {
		t.Fatalf("a module credential verifies to a sealed caller, and accounts/AGENTS.md does not say %q: "+
			"the file a reader asks whether an uninstall revokes a capability in flight must answer it.", credentialsSeal)
	}
}

// verifiedCallerIsSealed reports whether VerifyModuleWorkContext returns a
// ModuleCaller whose Sealed field is set to a SealedModuleAuthority naming the
// installation, its revision and the producer epoch.
//
// Structural, like nilSealReturnsNoError: the property is "a verified credential
// carries a seal", and a rewrite that keeps it while renaming a variable must not
// read as its absence.
func verifiedCallerIsSealed(t *testing.T, path string) bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name == nil || function.Name.Name != "VerifyModuleWorkContext" || function.Body == nil {
			continue
		}
		// The seal may be built in a local first (`sealed := &...{}`, then
		// `Sealed: sealed`); resolve such a name to the literal it was assigned.
		assigned := map[string]ast.Expr{}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if assign, ok := node.(*ast.AssignStmt); ok && len(assign.Lhs) == len(assign.Rhs) {
				for i, lhs := range assign.Lhs {
					if name, ok := lhs.(*ast.Ident); ok {
						assigned[name.Name] = assign.Rhs[i]
					}
				}
			}
			return true
		})
		sealed := false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			field, ok := node.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := field.Key.(*ast.Ident)
			if !ok || key.Name != "Sealed" {
				return true
			}
			value := field.Value
			if name, ok := value.(*ast.Ident); ok && assigned[name.Name] != nil {
				value = assigned[name.Name]
			}
			literal, ok := value.(*ast.UnaryExpr)
			if !ok {
				return true
			}
			composite, ok := literal.X.(*ast.CompositeLit)
			if !ok {
				return true
			}
			named := map[string]bool{}
			for _, element := range composite.Elts {
				if pair, ok := element.(*ast.KeyValueExpr); ok {
					if name, ok := pair.Key.(*ast.Ident); ok {
						named[name.Name] = true
					}
				}
			}
			sealed = named["InstallationID"] && named["InstallationRevision"] && named["ProducerEpoch"]
			return !sealed
		})
		return sealed
	}
	t.Fatal("VerifyModuleWorkContext is not declared: there is no credential verification for this gate to judge")
	return false
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
