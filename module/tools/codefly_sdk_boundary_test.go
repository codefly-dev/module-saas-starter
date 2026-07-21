package tools

import (
	"bufio"
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

// TestShellToolsDoNotReadCodeflyCarriers extends the SDK boundary to operational
// scripts. A shell tool may invoke the Codefly CLI or accept an explicit,
// purpose-named input; it must never couple itself to the runtime's generated
// environment representation.
func TestShellToolsDoNotReadCodeflyCarriers(t *testing.T) {
	root := findRepositoryRoot(t)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".next", "node_modules", "target", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".sh" {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		line := 0
		for scanner.Scan() {
			line++
			text := strings.TrimSpace(scanner.Text())
			if text == "" || strings.HasPrefix(text, "#") {
				continue
			}
			if strings.Contains(text, "CODEFLY__") || strings.Contains(text, "CODEFLY_SCOPED_AUTH_SECRET") {
				relative, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d hard-codes a Codefly runtime carrier; use the SDK/CLI boundary instead", relative, line)
			}
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatalf("scan shell tools: %v", err)
	}
}

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
	root := findRepositoryRoot(t)
	if _, err := os.Stat(filepath.Join(root, "workspace.codefly.yaml")); err == nil {
		return filepath.Join(root, "modules")
	}
	return root
}

func findRepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := ""
	for {
		if _, statErr := os.Stat(filepath.Join(directory, "workspace.codefly.yaml")); statErr == nil {
			return directory
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
