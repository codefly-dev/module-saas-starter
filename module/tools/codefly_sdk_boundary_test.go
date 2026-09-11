package tools

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestShellToolsDoNotReadCodeflyCarriers extends the SDK boundary to operational
// scripts. A shell tool may invoke the Codefly CLI or accept an explicit,
// purpose-named input; it must never couple itself to the runtime's generated
// environment representation. It shares the production walkers' composed scope:
// one tree must not be in scope for one walker and out of scope for another.
func TestShellToolsDoNotReadCodeflyCarriers(t *testing.T) {
	root, _, nonComposed := findProductionScanRoots(t)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if nonComposed[path] {
				return filepath.SkipDir
			}
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
	repositoryRoot, roots, nonComposed := findProductionScanRoots(t)
	fset := token.NewFileSet()

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if nonComposed[path] {
					return filepath.SkipDir
				}
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
	repositoryRoot, roots, nonComposed := findProductionScanRoots(t)
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
				if nonComposed[path] {
					return filepath.SkipDir
				}
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
	repositoryRoot, roots, nonComposed := findProductionScanRoots(t)
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
				if nonComposed[path] {
					return filepath.SkipDir
				}
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

func findProductionScanRoots(t *testing.T) (string, []string, map[string]bool) {
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
	return root, roots, nonComposedServiceDirectories(t, roots)
}

// TestComposedSubsetIsNarrowedAndDisclosed pins the contract a consuming
// workspace depends on: `codefly sync module` lands every service of a base
// module on disk, only the composed ones are in this gate's reach, and the
// narrowing is announced rather than silent.
func TestComposedSubsetIsNarrowedAndDisclosed(t *testing.T) {
	workspace := t.TempDir()
	writeComposedModule(t, filepath.Join(workspace, "module"),
		"services:\n    - name: accounts\n    - name: store\n",
		"accounts", "store")
	writeComposedModule(t, filepath.Join(workspace, "modules", "documents"),
		"services:\n    - name: documents\n    - name: store\n",
		"documents", "store", "runtime-worker")
	writeComposedModule(t, filepath.Join(workspace, "modules", "inventoryless"),
		"", "anything")

	scan := scanWorkspace(t, workspace)

	expected := map[string]bool{
		filepath.Join(workspace, "modules", "documents", "services", "runtime-worker"): true,
	}
	if !maps.Equal(scan.skipped, expected) {
		t.Fatalf("skipped = %v, want %v", scan.skipped, expected)
	}
	if len(scan.violations) != 0 {
		t.Fatalf("unexpected violations: %v", scan.violations)
	}
	if len(scan.notices) != 1 || !strings.Contains(scan.notices[0], "runtime-worker") {
		t.Fatalf("narrowing was not disclosed: %v", scan.notices)
	}
}

// TestOwnedModuleCannotShrinkTheGateByOmittingAService covers the seam this gate
// would otherwise hand out for free: module.codefly.yaml carries no integrity
// protection, so dropping a service from the inventory of a module this
// repository owns must fail loudly instead of silently exempting its code.
func TestOwnedModuleCannotShrinkTheGateByOmittingAService(t *testing.T) {
	workspace := t.TempDir()
	writeComposedModule(t, filepath.Join(workspace, "module"),
		"services:\n    - name: accounts\n",
		"accounts", "store")

	scan := scanWorkspace(t, workspace)

	if len(scan.skipped) != 0 {
		t.Fatalf("an owned module exempted %v; it must keep enforcing every service it ships", scan.skipped)
	}
	if len(scan.violations) != 1 || !strings.Contains(scan.violations[0], "store") {
		t.Fatalf("omission was not reported: %v", scan.violations)
	}
}

// TestMisspelledInventoryEnforcesEveryService covers the other half: an
// inventory naming a service that is not on disk has misspelled or renamed one
// that is, and the service it misspells must not fall out of the gate.
func TestMisspelledInventoryEnforcesEveryService(t *testing.T) {
	workspace := t.TempDir()
	writeComposedModule(t, filepath.Join(workspace, "modules", "documents"),
		"services:\n    - name: acounts\n    - name: store\n",
		"accounts", "store")

	scan := scanWorkspace(t, workspace)

	if len(scan.skipped) != 0 {
		t.Fatalf("a typo exempted %v; a malformed inventory must enforce everything", scan.skipped)
	}
	if len(scan.notices) != 1 || !strings.Contains(scan.notices[0], "acounts") {
		t.Fatalf("malformed inventory was not disclosed: %v", scan.notices)
	}
}

// scanWorkspace scans a fixture workspace through the same root filtering
// findProductionScanRoots applies, so the fixtures exercise the production path
// rather than a laxer one.
func scanWorkspace(t *testing.T, workspace string) compositionScan {
	t.Helper()
	var roots []string
	for _, candidate := range []string{filepath.Join(workspace, "module"), filepath.Join(workspace, "modules")} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			roots = append(roots, candidate)
		}
	}
	scan, err := scanModuleComposition(roots)
	if err != nil {
		t.Fatal(err)
	}
	return scan
}

func writeComposedModule(t *testing.T, moduleRoot, servicesBlock string, serviceDirectories ...string) {
	t.Helper()
	for _, service := range serviceDirectories {
		if err := os.MkdirAll(filepath.Join(moduleRoot, "services", service), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest := "kind: module\nname: " + filepath.Base(moduleRoot) + "\n" + servicesBlock
	if err := os.WriteFile(filepath.Join(moduleRoot, "module.codefly.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// composedModuleDocument is the minimal view of module.codefly.yaml this gate
// needs: the service inventory a workspace actually composes.
type composedModuleDocument struct {
	Services []struct {
		Name string `yaml:"name"`
	} `yaml:"services"`
}

// moduleComposition is a module root plus whether this repository owns it. An
// owned module — the canonical `module/`, or a module repository's own root —
// publishes its inventory; a module found under a workspace's `modules/` is a
// synced copy whose inventory is the consumer's composition choice.
type moduleComposition struct {
	root  string
	owned bool
}

// compositionScan is the decided scope of the production walkers: the subtrees
// to skip, the disclosure owed for skipping them, and the inventories that are
// themselves wrong. It is a value rather than inline *testing.T reporting so the
// disclosure can be asserted.
type compositionScan struct {
	skipped    map[string]bool
	notices    []string
	violations []string
}

// scanModuleComposition decides which `services/<name>/` subtrees sit outside
// this gate. A consumer may compose a subset of a base module's services, but
// `codefly sync module` copies the module whole, so the omitted services' code
// is never built, run, or given a runtime endpoint to resolve.
//
// module.codefly.yaml carries no integrity protection — base-integrity.mjs
// excludes it as consumer identity — so reading it unchecked would turn this
// gate into a silent, self-service waiver. The narrowing is therefore bounded:
//
//   - every skip is disclosed, the way base-integrity.mjs announces its own;
//   - an owned module must compose exactly what it ships, so canonical cannot
//     shrink the gate by editing generated YAML;
//   - an inventory naming a service with no directory does not describe this
//     tree — a typo silently exempts the service it misspells — so it is refused
//     whole and every service stays enforced.
func scanModuleComposition(roots []string) (compositionScan, error) {
	scan := compositionScan{skipped: map[string]bool{}}
	modules, err := moduleCompositions(roots)
	if err != nil {
		return scan, err
	}
	for _, module := range modules {
		composed, err := composedServices(module.root)
		if err != nil {
			return scan, err
		}
		if composed == nil {
			continue
		}
		onDisk, err := serviceDirectories(module.root)
		if err != nil {
			return scan, err
		}
		if phantom := namesMissingFrom(composed, onDisk); len(phantom) > 0 {
			scan.record(module, fmt.Sprintf("%s composes %s, which have no services/<name> directory; its inventory does not describe this tree, so every service stays enforced",
				module.root, strings.Join(phantom, ", ")))
			continue
		}
		skipped := namesMissingFrom(onDisk, composed)
		if len(skipped) == 0 {
			continue
		}
		if module.owned {
			scan.violations = append(scan.violations, fmt.Sprintf("%s ships %s without composing them; a module must compose every service it publishes, or this gate stops covering them",
				module.root, strings.Join(skipped, ", ")))
			continue
		}
		scan.notices = append(scan.notices, fmt.Sprintf("composed subset: %s skipped %d non-composed service(s): %s",
			module.root, len(skipped), strings.Join(skipped, ", ")))
		for _, service := range skipped {
			scan.skipped[filepath.Join(module.root, "services", service)] = true
		}
	}
	return scan, nil
}

// record routes a malformed inventory: the owning repository must fix it, while
// a consumer's copy falls back to enforcing everything and says so.
func (scan *compositionScan) record(module moduleComposition, message string) {
	if module.owned {
		scan.violations = append(scan.violations, message)
		return
	}
	scan.notices = append(scan.notices, message)
}

func nonComposedServiceDirectories(t *testing.T, roots []string) map[string]bool {
	t.Helper()
	scan, err := scanModuleComposition(roots)
	if err != nil {
		t.Fatal(err)
	}
	for _, notice := range scan.notices {
		t.Log(notice)
	}
	for _, violation := range scan.violations {
		t.Error(violation)
	}
	return scan.skipped
}

// moduleCompositions resolves scan roots to module roots: a root either holds a
// module.codefly.yaml itself or contains one module per child directory.
func moduleCompositions(roots []string) ([]moduleComposition, error) {
	var modules []moduleComposition
	for _, root := range roots {
		if _, err := os.Stat(filepath.Join(root, "module.codefly.yaml")); err == nil {
			modules = append(modules, moduleComposition{root: root, owned: true})
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, fmt.Errorf("read module roots under %s: %w", root, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			candidate := filepath.Join(root, entry.Name())
			if _, err := os.Stat(filepath.Join(candidate, "module.codefly.yaml")); err == nil {
				modules = append(modules, moduleComposition{root: candidate})
			}
		}
	}
	return modules, nil
}

// composedServices reads a module's declared service inventory, or nil when it
// declares none — which enforces every service on disk.
func composedServices(moduleRoot string) (map[string]bool, error) {
	manifest := filepath.Join(moduleRoot, "module.codefly.yaml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		return nil, err
	}
	var document composedModuleDocument
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode %s: %w", manifest, err)
	}
	if len(document.Services) == 0 {
		return nil, nil
	}
	composed := make(map[string]bool, len(document.Services))
	for _, service := range document.Services {
		composed[service.Name] = true
	}
	return composed, nil
}

// serviceDirectories lists the service subtrees a module ships. A module without
// a services/ directory ships none; any other read failure is reported, never
// mistaken for an empty tree.
func serviceDirectories(moduleRoot string) (map[string]bool, error) {
	servicesRoot := filepath.Join(moduleRoot, "services")
	entries, err := os.ReadDir(servicesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", servicesRoot, err)
	}
	directories := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() {
			directories[entry.Name()] = true
		}
	}
	return directories, nil
}

func namesMissingFrom(subject, reference map[string]bool) []string {
	var missing []string
	for name := range subject {
		if !reference[name] {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	return missing
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
