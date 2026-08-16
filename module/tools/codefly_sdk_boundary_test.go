package tools

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
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
		if err := scanner.Err(); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	})
	if err != nil {
		t.Fatalf("scan shell tools: %v", err)
	}
}

// TestProductionGoUsesCodeflySDK is a portable architecture gate for both the
// canonical module and composed workspaces. Runtime carriers belong to sdk-go;
// production services consume typed SDK methods.
func TestProductionGoUsesCodeflySDK(t *testing.T) {
	repositoryRoot, roots := findProductionScanRoots(t)
	fset := token.NewFileSet()

	for _, root := range roots {
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
						reportSDKBoundaryViolation(t, fset, repositoryRoot, path, value.Pos(), "hard-codes Codefly runtime carrier %q", literal)
					}
				case *ast.SelectorExpr:
					qualifier, ok := value.X.(*ast.Ident)
					if !ok {
						return true
					}
					if _, ok := resourceAliases[qualifier.Name]; ok && codeflyCarrierResourceSelectors[value.Sel.Name] {
						reportSDKBoundaryViolation(t, fset, repositoryRoot, path, value.Pos(), "accesses Codefly carrier constant %s.%s", qualifier.Name, value.Sel.Name)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("scan production Go code under %s: %v", root, err)
		}
	}
}

var loopbackPortLiteral = regexp.MustCompile(`(?i)(?:https?://)?(?:localhost|127(?:\.[0-9]{1,3}){3}|\[::1\]):[0-9]{1,5}`)

// TestProductionCodeDoesNotPinCodeflyPorts prevents the other half of the
// runtime-carrier bug: knowing a service's local port out of band. Codefly
// allocates and injects runtime endpoints; production code consumes the typed
// SDK, while shell setup tools use `codefly endpoint`.
func TestProductionCodeDoesNotPinCodeflyPorts(t *testing.T) {
	repositoryRoot, roots := findProductionScanRoots(t)
	fset := token.NewFileSet()
	scriptExtensions := map[string]bool{
		".cjs": true, ".js": true, ".jsx": true, ".mjs": true,
		".rs": true, ".sh": true, ".ts": true, ".tsx": true,
	}

	for _, root := range roots {
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
			if isTestSource(path) {
				return nil
			}
			if filepath.Ext(path) == ".go" {
				file, parseErr := parser.ParseFile(fset, path, nil, 0)
				if parseErr != nil {
					return parseErr
				}
				ast.Inspect(file, func(node ast.Node) bool {
					literal, ok := node.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						return true
					}
					value, unquoteErr := strconv.Unquote(literal.Value)
					if unquoteErr == nil && loopbackPortLiteral.MatchString(value) {
						reportPinnedPort(t, fset, repositoryRoot, path, literal.Pos(), value)
					}
					return true
				})
				return nil
			}
			if !scriptExtensions[filepath.Ext(path)] {
				return nil
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for index, line := range strings.Split(stripSourceComments(string(contents)), "\n") {
				if loopbackPortLiteral.MatchString(line) {
					relative, _ := filepath.Rel(repositoryRoot, path)
					t.Errorf("%s:%d pins a loopback port; resolve it through the Codefly SDK/CLI", relative, index+1)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan production port literals under %s: %v", root, err)
		}
	}

	scanRuntimeConfigurationsForPinnedPorts(t, repositoryRoot)
}

// TestProductionRustAndTypeScriptUseCodeflySDK closes the same architecture
// boundary for the other languages used by composed products. The scanner
// examines string contents while removing comments: documentation may explain
// a private carrier, but executable product code may only reach it through the
// language SDK.
func TestProductionRustAndTypeScriptUseCodeflySDK(t *testing.T) {
	repositoryRoot, roots := findProductionScanRoots(t)
	extensions := map[string]bool{
		".rs":  true,
		".ts":  true,
		".tsx": true,
		".js":  true,
		".jsx": true,
		".mjs": true,
		".cjs": true,
	}

	for _, root := range roots {
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
			if !extensions[filepath.Ext(path)] || isTestSource(path) {
				return nil
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for index, line := range strings.Split(stripSourceComments(string(contents)), "\n") {
				if !containsCodeflyCarrier(line) {
					continue
				}
				relative, _ := filepath.Rel(repositoryRoot, path)
				t.Errorf("%s:%d hard-codes a Codefly runtime carrier; add/use the language SDK instead", relative, index+1)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan production Rust/TypeScript code under %s: %v", root, err)
		}
	}
}

func containsCodeflyCarrier(text string) bool {
	return strings.Contains(text, "CODEFLY__") ||
		strings.Contains(text, "CODEFLY_SCOPED_AUTH_SECRET")
}

func isTestSource(path string) bool {
	slashPath := filepath.ToSlash(path)
	for _, segment := range []string{"/test/", "/tests/", "/__tests__/"} {
		if strings.Contains(slashPath, segment) {
			return true
		}
	}
	name := filepath.Base(path)
	for _, marker := range []string{"_test.go", ".test.", ".spec.", "playwright.config."} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	if strings.Contains(slashPath, "/scripts/smoke.") ||
		strings.Contains(slashPath, "/tools/marketing-extraction.") {
		return true
	}
	return false
}

// stripSourceComments is a deliberately small lexer, not a source parser. It
// preserves quoted strings (the evidence this gate cares about) and newlines
// (for useful locations), while removing line and block comments.
func stripSourceComments(source string) string {
	var result strings.Builder
	result.Grow(len(source))
	var quote byte
	escaped := false
	lineComment := false
	blockComment := false

	for index := 0; index < len(source); index++ {
		current := source[index]
		next := byte(0)
		if index+1 < len(source) {
			next = source[index+1]
		}

		if lineComment {
			if current == '\n' {
				lineComment = false
				result.WriteByte(current)
			}
			continue
		}
		if blockComment {
			if current == '*' && next == '/' {
				blockComment = false
				index++
			} else if current == '\n' {
				result.WriteByte(current)
			}
			continue
		}
		if quote != 0 {
			result.WriteByte(current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == quote {
				quote = 0
			}
			continue
		}
		if current == '/' && next == '/' {
			lineComment = true
			index++
			continue
		}
		if current == '/' && next == '*' {
			blockComment = true
			index++
			continue
		}
		if current == '"' || current == '\'' || current == '`' {
			quote = current
		}
		result.WriteByte(current)
	}
	return result.String()
}

func TestSourceCommentStrippingPreservesOnlyExecutableCarrierText(t *testing.T) {
	source := `// CODEFLY__COMMENT
const actual = "CODEFLY__EXECUTABLE";
/* CODEFLY__BLOCK */
const url = "https://example.test/CODEFLY__INSIDE_STRING";
`
	stripped := stripSourceComments(source)
	if strings.Contains(stripped, "CODEFLY__COMMENT") ||
		strings.Contains(stripped, "CODEFLY__BLOCK") {
		t.Fatalf("comment text survived: %q", stripped)
	}
	for _, wanted := range []string{"CODEFLY__EXECUTABLE", "CODEFLY__INSIDE_STRING"} {
		if !strings.Contains(stripped, wanted) {
			t.Fatalf("executable text %q was removed: %q", wanted, stripped)
		}
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

func reportPinnedPort(t *testing.T, fset *token.FileSet, root, path string, position token.Pos, literal string) {
	t.Helper()
	location := fset.Position(position)
	relative, _ := filepath.Rel(root, path)
	t.Errorf("%s:%d pins Codefly runtime address %q; resolve it through the language SDK", relative, location.Line, literal)
}

func scanRuntimeConfigurationsForPinnedPorts(t *testing.T, repositoryRoot string) {
	t.Helper()
	configurationRoots := []string{filepath.Join(repositoryRoot, "configurations")}
	modulesRoot := filepath.Join(repositoryRoot, "modules")
	if entries, err := os.ReadDir(modulesRoot); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				configurationRoots = append(configurationRoots, filepath.Join(modulesRoot, entry.Name(), "configurations"))
			}
		}
	}
	for _, root := range configurationRoots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.Contains(filepath.Base(path), ".env") ||
				strings.Contains(filepath.Base(path), ".secret.") {
				return nil
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			scanner := bufio.NewScanner(file)
			line := 0
			for scanner.Scan() {
				line++
				text := strings.TrimSpace(scanner.Text())
				if text == "" || strings.HasPrefix(text, "#") {
					continue
				}
				if loopbackPortLiteral.MatchString(text) {
					relative, _ := filepath.Rel(repositoryRoot, path)
					t.Errorf("%s:%d pins a loopback port; Codefly must inject the endpoint", relative, line)
				}
			}
			if err := scanner.Err(); err != nil {
				_ = file.Close()
				return err
			}
			return file.Close()
		})
		if err != nil {
			t.Fatalf("scan runtime configurations under %s: %v", root, err)
		}
	}
}

func findProductionScanRoots(t *testing.T) (string, []string) {
	t.Helper()
	root := findRepositoryRoot(t)
	candidates := []string{
		filepath.Join(root, "module"),
		filepath.Join(root, "modules"),
	}
	if _, err := os.Stat(filepath.Join(root, "module.codefly.yaml")); err == nil {
		candidates = append(candidates, root)
	}
	var roots []string
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			roots = append(roots, candidate)
		}
	}
	if len(roots) == 0 {
		t.Fatal("no Codefly production module roots found")
	}
	return root, roots
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
