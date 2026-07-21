package tools

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestProductionGoUsesCodeflySDK is a portable architecture gate for both the
// canonical module and composed workspaces. Runtime carriers belong to sdk-go;
// production services consume typed SDK methods.
func TestProductionGoUsesCodeflySDK(t *testing.T) {
	root := findProductionScanRoot(t)
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "vendor" || entry.Name() == "node_modules" || entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		resourceAliases := map[string]struct{}{}
		for _, imported := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil || importPath != "github.com/codefly-dev/core/resources" {
				continue
			}
			alias := "resources"
			if imported.Name != nil {
				alias = imported.Name.Name
			}
			resourceAliases[alias] = struct{}{}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.BasicLit:
				if value.Kind != token.STRING {
					return true
				}
				literal, unquoteErr := strconv.Unquote(value.Value)
				if unquoteErr == nil && (strings.Contains(literal, "CODEFLY__") || strings.Contains(literal, "CODEFLY_SCOPED_AUTH_SECRET")) {
					reportSDKBoundaryViolation(t, fset, root, path, value.Pos(), "hard-codes Codefly runtime carrier %q", literal)
				}
			case *ast.SelectorExpr:
				qualifier, ok := value.X.(*ast.Ident)
				if !ok {
					return true
				}
				if _, ok := resourceAliases[qualifier.Name]; ok && codeflyCarrierResourceSelectors[value.Sel.Name] {
					reportSDKBoundaryViolation(t, fset, root, path, value.Pos(), "accesses Codefly carrier constant %s.%s", qualifier.Name, value.Sel.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go code: %v", err)
	}
}

var codeflyCarrierResourceSelectors = map[string]bool{
	"EnvironmentPrefix":                  true,
	"FixturePrefix":                      true,
	"ServiceConfigurationPrefix":         true,
	"ServiceSecretConfigurationPrefix":   true,
	"WorkspaceConfigurationPrefix":       true,
	"WorkspaceSecretConfigurationPrefix": true,
}

func reportSDKBoundaryViolation(t *testing.T, fset *token.FileSet, root, path string, position token.Pos, format string, args ...any) {
	t.Helper()
	location := fset.Position(position)
	relative, _ := filepath.Rel(root, path)
	t.Errorf("%s:%d %s; add/use an sdk-go API instead", relative, location.Line, fmt.Sprintf(format, args...))
}

func findProductionScanRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := ""
	for {
		if _, statErr := os.Stat(filepath.Join(directory, "workspace.codefly.yaml")); statErr == nil {
			return filepath.Join(directory, "modules")
		}
		if moduleRoot == "" {
			if _, statErr := os.Stat(filepath.Join(directory, "module.codefly.yaml")); statErr == nil {
				moduleRoot = directory
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			if moduleRoot != "" {
				return moduleRoot
			}
			t.Fatal("Codefly workspace or module root not found")
		}
		directory = parent
	}
}
