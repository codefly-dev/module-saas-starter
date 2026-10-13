package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// A live read is insufficient if the method drops its lock before the effect.
// Cover the RPC boundary too: audience resolvers return before adapter signing.
func TestModuleCapabilityEntryPointsHoldAuthorityThroughUse(t *testing.T) {
	root := findModuleDir(t)
	checked := 0
	for _, pkg := range []string{"business", "adapters"} {
		paths, err := filepath.Glob(filepath.Join(root, "services/accounts/code/pkg", pkg, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				if fn.Recv == nil {
					if pkg != "adapters" || (fn.Name.Name != "streamDatasourceBlob" && fn.Name.Name != "streamDatasourceFiles") {
						continue
					}
				} else {
					receiver, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
					if !ok {
						continue
					}
					name, ok := receiver.X.(*ast.Ident)
					if !ok {
						continue
					}
					if pkg == "business" && name.Name != "Service" || pkg == "adapters" && name.Name != "ModuleCapabilitiesServer" {
						continue
					}
				}
				// These three return the shared decision; their entrypoint
				// callers below own the complete use, and are checked here.
				if pkg == "business" && (fn.Name.Name == "moduleCapability" || fn.Name.Name == "authorizeModuleCapability" || fn.Name.Name == "authorizeModuleCapabilityFromSnapshot") {
					continue
				}
				needsFence := pkg == "adapters"
				begins, releases := false, false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
							switch selector.Sel.Name {
							case "moduleCapability", "authorizeModuleCapability", "authorizeModuleCapabilityFromSnapshot", "AuthorizeModuleCapability", "ExecutableArtifactPolicyFor":
								needsFence = true
							case "BeginModuleAuthorityUse":
								begins = true
							}
						}
					}
					if d, ok := n.(*ast.DeferStmt); ok {
						if id, ok := d.Call.Fun.(*ast.Ident); ok && id.Name == "releaseAuthority" {
							releases = true
						}
					}
					return true
				})
				if !needsFence {
					continue
				}
				checked++
				if !begins || !releases {
					t.Errorf("%s:%s must span authority decision and effect with a deferred release", filepath.Base(path), fn.Name.Name)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no module capability entrypoints found")
	}
}
