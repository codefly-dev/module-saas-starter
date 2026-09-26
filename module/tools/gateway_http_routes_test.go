package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A non-protobuf HTTP route is registered in the service and routed at the
// gateway, and the two halves have no generator between them: the service calls
// RegisterHTTPRoute, and someone has to remember to add the same path to
// auth-gateway's non-protobuf extensions. Nothing tied them together, and the
// drift fails silently in the direction that matters — the handler exists,
// verifies its signature, answers correctly, and the gateway answers 404 before
// any of it runs, so the feature is simply dead with every one of its own tests
// green.
//
// It shipped twice. The GitHub webhook receivers (per-source and App-level) were
// registered with no gateway route, so no delivery could ever reach the code that
// verifies it. /v1/status was registered as the public status surface the
// product's own /status page fetches, with no gateway route, so that page showed
// a fetch error.
//
// This gate closes the loop. Every path the accounts service registers is either
// reachable through the gateway — a generated descriptor route or an explicit
// extension — or carries the marker below saying, in the source, why it is not.
const (
	// registerHTTPRouteFunc is the registration seam. A path passed to it is a
	// public-edge path unless the author says otherwise.
	registerHTTPRouteFunc = "RegisterHTTPRoute"
	// gatewayRouteExemption is how an author says a registered route is
	// deliberately NOT reachable through the public gateway — a mesh-only
	// internal route, or a scrape endpoint. Absence from the gateway catalog
	// otherwise means both "not public" and "forgotten", which is the silence
	// this gate exists to remove. The reason follows the marker on the same
	// comment line.
	gatewayRouteExemption = "codefly:gateway-route-exempt"
)

// registeredHTTPRoute is one RegisterHTTPRoute call site.
type registeredHTTPRoute struct {
	path     string
	position string
	exempt   bool
}

func TestRegisteredHTTPRoutesAreRoutedAtTheGateway(t *testing.T) {
	moduleDir := findModuleDir(t)
	accountsCode := filepath.Join(moduleDir, "services", "accounts", "code")

	consts, ambiguous := stringConstantsIn(t, accountsCode)
	registered := registeredHTTPRoutes(t, accountsCode, consts, ambiguous)
	if len(registered) == 0 {
		t.Fatalf("found no %s call sites under %s — the gate cannot pass by finding nothing", registerHTTPRouteFunc, accountsCode)
	}

	routable := gatewayRoutablePaths(t, moduleDir)

	var missing []string
	for _, route := range registered {
		if route.exempt {
			continue
		}
		if gatewayRoutes(routable).covers(route.path) {
			continue
		}
		missing = append(missing, route.position+": "+route.path)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf(
			"accounts registers %d HTTP route(s) the gateway does not route, so each 404s at the public edge "+
				"however correct its handler is:\n    %s\n\nAdd the path to "+
				"services/auth-gateway/routing/rest/saas-starter/accounts/non-protobuf-extensions.rest.codefly.yaml, "+
				"or mark the call site `// %s <reason>` if it is deliberately not public.",
			len(missing), strings.Join(missing, "\n    "), gatewayRouteExemption,
		)
	}
}

// An exemption must carry a reason. "Exempt" with nothing after it is the same
// silence as absence, one marker further on.
func TestGatewayRouteExemptionsStateAReason(t *testing.T) {
	moduleDir := findModuleDir(t)
	accountsCode := filepath.Join(moduleDir, "services", "accounts", "code")
	fileSet := token.NewFileSet()

	walkGoFiles(t, accountsCode, func(path string) {
		file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, group := range file.Comments {
			for _, comment := range group.List {
				text := comment.Text
				index := strings.Index(text, gatewayRouteExemption)
				if index < 0 {
					continue
				}
				reason := strings.TrimSpace(text[index+len(gatewayRouteExemption):])
				if reason == "" {
					t.Errorf("%s: %s carries no reason", fileSet.Position(comment.Pos()), gatewayRouteExemption)
				}
			}
		}
	})
}

// gatewayRoutes is the set of public-edge path templates, matched the way the
// gateway matches: segment count must agree and a {param} segment stands for
// exactly one non-empty segment. A registered prefix route ("/v1/x/") is covered
// by a template that matches one path below it.
type gatewayRoutes []string

func (routes gatewayRoutes) covers(registered string) bool {
	probe := strings.TrimSuffix(registered, "/")
	if strings.HasSuffix(registered, "/") {
		// RegisterHTTPRoute matches by prefix, so a trailing slash means "and
		// everything below". The gateway must route at least one segment below it.
		probe = probe + "/probe"
	}
	probeSegments := strings.Split(probe, "/")
	for _, route := range routes {
		if templateMatches(strings.Split(route, "/"), probeSegments) {
			return true
		}
	}
	return false
}

func templateMatches(template, request []string) bool {
	if len(template) != len(request) {
		return false
	}
	for i := range template {
		if strings.HasPrefix(template[i], "{") && strings.HasSuffix(template[i], "}") {
			if request[i] == "" {
				return false
			}
			continue
		}
		if template[i] != request[i] {
			return false
		}
	}
	return true
}

// gatewayRoutablePaths is every path the gateway can match for accounts: the
// generated descriptor REST surface plus the explicit non-protobuf extensions.
// Both are read from the artifacts the gateway itself loads, so a path that
// stops being routed there stops being routed here.
func gatewayRoutablePaths(t *testing.T, moduleDir string) []string {
	t.Helper()
	var paths []string

	surface := filepath.Join(moduleDir, "services", "accounts", "generated", "rest-surface.json")
	raw, err := os.ReadFile(surface)
	if err != nil {
		t.Fatalf("read %s: %v", surface, err)
	}
	// Read the paths without binding to the whole schema: the gate cares only
	// that a path is present, and a schema change should not silently empty it.
	var document struct {
		Routes []struct {
			Path string `json:"path"`
		} `json:"routes"`
		Services []struct {
			Methods []struct {
				Rest *struct {
					Path string `json:"path"`
				} `json:"rest"`
			} `json:"methods"`
		} `json:"services"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse %s: %v", surface, err)
	}
	for _, route := range document.Routes {
		if route.Path != "" {
			paths = append(paths, route.Path)
		}
	}
	for _, service := range document.Services {
		for _, method := range service.Methods {
			if method.Rest != nil && method.Rest.Path != "" {
				paths = append(paths, method.Rest.Path)
			}
		}
	}
	if len(paths) == 0 {
		t.Fatalf("%s yielded no descriptor REST paths; the gate would pass by reading nothing", surface)
	}

	extensionsDir := filepath.Join(moduleDir, "services", "auth-gateway", "routing", "rest")
	extensionCount := 0
	walkFiles(t, extensionsDir, ".codefly.yaml", func(path string) {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		var group struct {
			Routes []struct {
				Path string `yaml:"path"`
			} `yaml:"routes"`
		}
		if err := yaml.Unmarshal(body, &group); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, route := range group.Routes {
			if route.Path != "" {
				paths = append(paths, route.Path)
				extensionCount++
			}
		}
	})
	if extensionCount == 0 {
		t.Fatalf("%s yielded no explicit extension paths; the gate would pass by reading nothing", extensionsDir)
	}
	return paths
}

// registeredHTTPRoutes finds every RegisterHTTPRoute call under dir and resolves
// its path argument. A literal is used as written; an identifier is resolved
// through the package-level string constants of the whole service. Anything else
// fails the gate rather than being skipped: an unresolvable path is a route no
// gate can check, and the point is that none goes unchecked.
func registeredHTTPRoutes(t *testing.T, dir string, consts map[string]string, ambiguous map[string]bool) []registeredHTTPRoute {
	t.Helper()
	fileSet := token.NewFileSet()
	var found []registeredHTTPRoute

	walkGoFiles(t, dir, func(path string) {
		file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		exemptLines := exemptionLines(fileSet, file)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isRegisterHTTPRouteCall(call) || len(call.Args) == 0 {
				return true
			}
			position := fileSet.Position(call.Pos())
			value, resolved := resolveStringArg(call.Args[0], consts, ambiguous)
			if !resolved {
				t.Errorf(
					"%s: cannot resolve the path passed to %s statically. Pass a string literal or a "+
						"package-level string constant so the gateway correspondence can be checked.",
					position, registerHTTPRouteFunc,
				)
				return true
			}
			found = append(found, registeredHTTPRoute{
				path:     value,
				position: strings.TrimPrefix(position.String(), dir+string(os.PathSeparator)),
				exempt:   exemptLines[position.Line] || exemptLines[position.Line-1] || exemptLines[position.Line-2],
			})
			return true
		})
	})
	return found
}

func isRegisterHTTPRouteCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name == registerHTTPRouteFunc
	case *ast.Ident:
		return fn.Name == registerHTTPRouteFunc
	}
	return false
}

// exemptionLines maps each line carrying the exemption marker, so a marker on
// the call line or on either of the two lines above it applies to the call.
func exemptionLines(fileSet *token.FileSet, file *ast.File) map[int]bool {
	lines := map[int]bool{}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.Contains(comment.Text, gatewayRouteExemption) {
				lines[fileSet.Position(comment.Pos()).Line] = true
			}
		}
	}
	return lines
}

func resolveStringArg(arg ast.Expr, consts map[string]string, ambiguous map[string]bool) (string, bool) {
	switch expr := arg.(type) {
	case *ast.BasicLit:
		if expr.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(expr.Value)
		if err != nil {
			return "", false
		}
		return value, true
	case *ast.Ident:
		if ambiguous[expr.Name] {
			return "", false
		}
		value, ok := consts[expr.Name]
		return value, ok
	case *ast.SelectorExpr:
		if ambiguous[expr.Sel.Name] {
			return "", false
		}
		value, ok := consts[expr.Sel.Name]
		return value, ok
	}
	return "", false
}

// stringConstantsIn collects every package-level string constant under dir by
// name, and separately the names declared more than once with different values.
// The service has such collisions among unrelated internals, so they are
// reported rather than fatal — resolving a call site THROUGH an ambiguous name
// is what must fail, because picking one silently is how a gate passes on the
// wrong path.
func stringConstantsIn(t *testing.T, dir string) (map[string]string, map[string]bool) {
	t.Helper()
	fileSet := token.NewFileSet()
	values := map[string]string{}
	ambiguous := map[string]bool{}

	walkGoFiles(t, dir, func(path string) {
		file, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			generic, ok := decl.(*ast.GenDecl)
			if !ok || generic.Tok != token.CONST {
				continue
			}
			for _, spec := range generic.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range valueSpec.Names {
					if i >= len(valueSpec.Values) {
						continue
					}
					literal, ok := valueSpec.Values[i].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					unquoted, err := strconv.Unquote(literal.Value)
					if err != nil {
						continue
					}
					if previous, seen := values[name.Name]; seen && previous != unquoted {
						ambiguous[name.Name] = true
						continue
					}
					values[name.Name] = unquoted
				}
			}
		}
	})
	return values, ambiguous
}

func walkGoFiles(t *testing.T, dir string, visit func(path string)) {
	t.Helper()
	walkFiles(t, dir, ".go", func(path string) {
		if strings.HasSuffix(path, "_test.go") {
			return
		}
		visit(path)
	})
}

func walkFiles(t *testing.T, dir, suffix string, visit func(path string)) {
	t.Helper()
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", "gen", "vendor", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, suffix) {
			visit(path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}
