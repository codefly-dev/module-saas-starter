package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The trust anchor's location must be out of the composition's reach BY
// CONSTRUCTION, and this is the gate that keeps it there.
//
// The hole this closes was real and shipped: the keyless verifier read its
// directory from `SOLUTION_HOST_TRUST_MOUNT`, a workspace environment value.
// Workspace environment is delivered by the COMPOSITION, so a composition could
// repoint the verifier at a verification policy and a trust root it wrote
// itself, sign its own presence documents with a key that policy listed, and
// pass every downstream check — because every check was against an anchor the
// composition chose.
//
// "A document never nominates its own anchor" is therefore not a property a
// check can enforce: the thing under attack is where the check's own inputs come
// from. It holds only if the path is not configurable at all.
//
// A code review cannot keep that. Re-adding an override is a one-line change
// that looks like a convenience, reads as a deployability improvement, and
// silently restores the hole — so the rule is mechanical.
func TestTrustAnchorPathIsNotConfigurable(t *testing.T) {
	moduleDir := findModuleDir(t)
	accountsCode := filepath.Join(moduleDir, "services", "accounts", "code")

	// 1. No environment value may name the anchor. Any workspaceEnv key
	//    containing TRUST and MOUNT/DIR/PATH/ROOT is the shape of the defect.
	work := readFileOrFail(t, filepath.Join(accountsCode, "work.go"))
	envPattern := regexp.MustCompile(`workspaceEnv\([^)]*"([A-Z_]*TRUST[A-Z_]*)"\)`)
	for _, match := range envPattern.FindAllStringSubmatch(work, -1) {
		key := match[1]
		for _, suffix := range []string{"MOUNT", "DIR", "PATH", "ROOT", "ANCHOR"} {
			if strings.Contains(key, suffix) {
				t.Fatalf("work.go reads %q from workspace environment, which the COMPOSITION delivers.\n\n"+
					"That lets a composition point the verifier at a policy and root it wrote itself, so its own "+
					"documents are checked against an anchor it chose — and no downstream check recovers from "+
					"that, because the checks use those inputs.\n\n"+
					"The anchor's location is a constant (infra.SolutionHostTrustAnchorPath). A deployment that "+
					"needs a different path changes the pod spec that mounts it.", key)
			}
		}
	}

	// 2. The constant must exist and be a literal. A const whose value came from
	//    os.Getenv would satisfy a grep for "const" and none of the property.
	verifier := filepath.Join(accountsCode, "pkg", "infra", "solution_host_bundle_verifier.go")
	requireLiteralStringConst(t, verifier, "SolutionHostTrustAnchorPath")

	// 3. The constructor must take no path. An optional path is a configurable
	//    path with extra steps: a caller that CAN pass one is a caller that can
	//    be given one, and the next edit gives it the env var back.
	requireConstructorTakesNoPath(t, verifier, "NewSolutionHostBundleVerifier")
}

// requireLiteralStringConst checks a named const is declared with a string
// literal rather than a computed value.
func requireLiteralStringConst(t *testing.T, path, name string) {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, declaration := range file.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, spec := range group.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range value.Names {
				if ident.Name != name || i >= len(value.Values) {
					continue
				}
				literal, ok := value.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("%s must be a string LITERAL: a const computed from os.Getenv or a variable "+
						"satisfies \"it is a const\" while restoring exactly the configurability this forbids", name)
				}
				if !strings.HasPrefix(strings.Trim(literal.Value, `"`), "/") {
					t.Fatalf("%s must be an absolute path; a relative one resolves against the working "+
						"directory, which is not a property of the pod spec", name)
				}
				return
			}
		}
	}
	t.Fatalf("%s is not declared in %s — the fixed anchor path is gone, and with it the only thing "+
		"keeping the anchor out of the composition's reach", name, filepath.Base(path))
}

// requireConstructorTakesNoPath checks the verifier constructor accepts no
// path-shaped parameter.
func requireConstructorTakesNoPath(t *testing.T, path, name string) {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name == nil || function.Name.Name != name {
			continue
		}
		if function.Type.Params == nil {
			return
		}
		for _, param := range function.Type.Params.List {
			for _, ident := range param.Names {
				lower := strings.ToLower(ident.Name)
				for _, banned := range []string{"mount", "path", "dir", "root", "anchor"} {
					if strings.Contains(lower, banned) {
						t.Fatalf("%s takes a parameter %q, so the anchor's location is a caller's choice again.\n"+
							"An optional path is a configurable path with extra steps.", name, ident.Name)
					}
				}
			}
		}
		return
	}
	t.Fatalf("%s is not declared in %s", name, filepath.Base(path))
}
