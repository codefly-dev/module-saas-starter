package auth_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// sourceSitesSettingWoolIdentity returns every non-test file under the service
// that writes wool's identity keys, as a repository-relative path.
//
// Derived from the syntax tree rather than grepped: a grep for the key name also
// matches a read, and the question here is specifically who WRITES.
func sourceSitesSettingWoolIdentity(t *testing.T) []string {
	t.Helper()
	identityKeys := map[string]bool{"OrgIDKey": true, "UserIDKey": true}
	found := map[string]bool{}

	root := "../.."
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", "gen", "generated", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return nil
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			// context.WithValue(ctx, wool.OrgIDKey, …)
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "WithValue" && len(call.Args) == 3 {
				if key, ok := call.Args[1].(*ast.SelectorExpr); ok && identityKeys[key.Sel.Name] {
					if pkg, ok := key.X.(*ast.Ident); ok && pkg.Name == "wool" {
						found[filepath.ToSlash(filepath.Clean(path))] = true
					}
				}
			}
			// w.WithOrgID(…) / w.WithUserID(…) — a no-op as the API is built,
			// but a writer in intent, so it counts.
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				if selector.Sel.Name == "WithOrgID" || selector.Sel.Name == "WithUserID" {
					found[filepath.ToSlash(filepath.Clean(path))] = true
				}
			}
			return true
		})
		return nil
	}))

	sites := make([]string, 0, len(found))
	for path := range found {
		sites = append(sites, strings.TrimPrefix(path, "../../"))
	}
	sort.Strings(sites)
	return sites
}
