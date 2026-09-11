package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A cluster-internal HTTP route is authored rather than derived: no catalog
// describes an HTTP route carrying cluster-internal authority, so
// deployment/topology.bindings.codefly.yaml names the method/path pairs the
// baseline renders `deny-<service>-internal-http` from. Nothing tied that list
// to the handlers that actually check the cluster-internal token, and the drift
// fails open in both directions — a newly gated route is simply ungated in the
// mesh, and a declared path that no longer exists renders a policy matching
// nothing while reading as protection. The correspondence spans a Go-authored
// binding and TypeScript route modules, so it is checked at the text level.
const (
	topologyBindingFile = "deployment/topology.bindings.codefly.yaml"
	moduleManifestFile  = "module.codefly.yaml"
	internalCallGate    = "isTrustedInternalCall"
	// internalRouteExemption is how an author says a gated route is
	// deliberately not mesh-denied. Absence from internal_http_routes otherwise
	// means both "not internal" and "forgotten", which is the silence this gate
	// exists to remove.
	internalRouteExemption = "codefly:internal-http-route-exempt"
)

// nextRouteHandlerMethods is the set of exported names Next.js serves as route
// handlers from a route module.
var nextRouteHandlerMethods = map[string]bool{
	"DELETE":  true,
	"GET":     true,
	"HEAD":    true,
	"OPTIONS": true,
	"PATCH":   true,
	"POST":    true,
	"PUT":     true,
}

type internalHTTPRouteBinding struct {
	Path    string   `yaml:"path"`
	Methods []string `yaml:"methods"`
}

type topologyServiceBinding struct {
	Name               string                     `yaml:"name"`
	InternalHTTPRoutes []internalHTTPRouteBinding `yaml:"internal_http_routes"`
}

type topologyBindings struct {
	Services []topologyServiceBinding `yaml:"services"`
}

// internalRouteKey is one method/path pair, the unit both the binding and the
// mesh policy work in.
type internalRouteKey struct {
	method string
	path   string
}

func (key internalRouteKey) String() string { return key.method + " " + key.path }

// gatedRouteHandler is one exported HTTP method of one route module whose body
// checks the cluster-internal token.
type gatedRouteHandler struct {
	key          internalRouteKey
	file         string
	exemptReason string
}

func TestInternalHTTPRoutesMatchTokenGatedHandlers(t *testing.T) {
	moduleDir := findModuleDir(t)
	composed := composedServiceNames(t, moduleDir)

	for _, service := range loadTopologyBindings(t, moduleDir).Services {
		if composed != nil && !composed[service.Name] {
			continue
		}
		appDir := filepath.Join(moduleDir, "services", service.Name, "code", "src", "app")
		if info, err := os.Stat(appDir); err != nil || !info.IsDir() {
			if len(service.InternalHTTPRoutes) > 0 {
				t.Errorf("service %q declares internal_http_routes but ships no route modules under services/%s/code/src/app to resolve them against", service.Name, service.Name)
			}
			continue
		}
		compareInternalHTTPRoutes(t, service.Name, gatedRouteHandlers(t, moduleDir, appDir), declaredInternalRoutes(service))
	}
}

// TestInternalCallGateIsReachedOnlyFromRouteModules keeps the correspondence
// above attributable. It reads the token check as belonging to the exported
// handler whose body calls it, so a gate reached through a shared helper would
// leave its route invisible to that check and silently undeclared.
func TestInternalCallGateIsReachedOnlyFromRouteModules(t *testing.T) {
	moduleDir := findModuleDir(t)
	composed := composedServiceNames(t, moduleDir)

	for _, service := range loadTopologyBindings(t, moduleDir).Services {
		if composed != nil && !composed[service.Name] {
			continue
		}
		sourceDir := filepath.Join(moduleDir, "services", service.Name, "code", "src")
		if info, err := os.Stat(sourceDir); err != nil || !info.IsDir() {
			continue
		}
		err := filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return skipNonSourceDirectory(entry.Name())
			}
			if !typeScriptSource(entry.Name()) || isTestSource(path) || isRouteModule(entry.Name()) {
				return nil
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			code, _ := blankNonCode(string(contents))
			if !strings.Contains(code, internalCallGate) || gateDefinition.MatchString(code) {
				return nil
			}
			relative, _ := filepath.Rel(moduleDir, path)
			t.Errorf("%s: %s is reached outside a route module; internal_http_routes is checked against the exported handler that calls it, so a route gated here would go undeclared", filepath.ToSlash(relative), internalCallGate)
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", sourceDir, err)
		}
	}
}

func compareInternalHTTPRoutes(t *testing.T, service string, gated []gatedRouteHandler, declared map[internalRouteKey]bool) {
	t.Helper()
	resolved := make(map[internalRouteKey]bool, len(gated))
	for _, handler := range gated {
		resolved[handler.key] = true
		switch {
		case handler.exemptReason != "" && declared[handler.key]:
			t.Errorf("%s: %s is declared in internal_http_routes and marked %s; it is one or the other", handler.file, handler.key, internalRouteExemption)
		case handler.exemptReason != "" || declared[handler.key]:
		default:
			t.Errorf("%s: %s checks the cluster-internal token, but service %q does not declare it in internal_http_routes; declare it there, or mark the handler `%s %s: <why not>`", handler.file, handler.key, service, internalRouteExemption, handler.key.method)
		}
	}
	undeclared := make([]internalRouteKey, 0, len(declared))
	for key := range declared {
		if !resolved[key] {
			undeclared = append(undeclared, key)
		}
	}
	sort.Slice(undeclared, func(i, j int) bool { return undeclared[i].String() < undeclared[j].String() })
	for _, key := range undeclared {
		t.Errorf("service %q declares internal_http_routes %s, which no route module gates with %s; the rendered deny matches nothing", service, key, internalCallGate)
	}
}

func declaredInternalRoutes(service topologyServiceBinding) map[internalRouteKey]bool {
	declared := make(map[internalRouteKey]bool)
	for _, route := range service.InternalHTTPRoutes {
		for _, method := range route.Methods {
			declared[internalRouteKey{method: method, path: route.Path}] = true
		}
	}
	return declared
}

func gatedRouteHandlers(t *testing.T, moduleDir, appDir string) []gatedRouteHandler {
	t.Helper()
	var handlers []gatedRouteHandler
	err := filepath.WalkDir(appDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return skipNonSourceDirectory(entry.Name())
		}
		if !isRouteModule(entry.Name()) {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relative, _ := filepath.Rel(moduleDir, path)
		relative = filepath.ToSlash(relative)
		scan := scanRouteModule(relative, string(contents))
		for _, problem := range scan.problems {
			t.Error(problem)
		}
		if len(scan.gated) == 0 {
			return nil
		}
		routePath, dynamic := routeModulePath(appDir, path)
		if dynamic {
			t.Errorf("%s: a handler here checks the cluster-internal token, but the mesh matches literal paths and %s carries a dynamic segment", relative, routePath)
			return nil
		}
		for _, method := range sortedKeys(scan.gated) {
			handlers = append(handlers, gatedRouteHandler{
				key:          internalRouteKey{method: method, path: routePath},
				file:         relative,
				exemptReason: scan.exemptions[method],
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", appDir, err)
	}
	return handlers
}

// routeModuleScan is what one route module says about the cluster-internal
// gate: the methods whose handler checks it, the methods an author marked
// deliberately un-denied, and whatever made the module unreadable to this gate.
type routeModuleScan struct {
	gated      map[string]bool
	exemptions map[string]string
	problems   []string
}

var (
	gateReference      = regexp.MustCompile(`\b` + internalCallGate + `\b`)
	gateDefinition     = regexp.MustCompile(`export[\t ]+function[\t ]+` + internalCallGate + `\b`)
	exportedFunction   = regexp.MustCompile(`(?m)^[\t ]*export[\t ]+(?:async[\t ]+)?function[\t ]+([A-Za-z_$][\w$]*)[\t ]*\(`)
	functionDefinition = regexp.MustCompile(`(?m)^[\t ]*(?:export[\t ]+)?(?:async[\t ]+)?function[\t ]+([A-Za-z_$][\w$]*)[\t ]*\(`)
	exportList         = regexp.MustCompile(`\bexport[\t\n ]*\{([^}]*)\}`)
	exportAlias        = regexp.MustCompile(`^(?:([A-Za-z_$][\w$]*)[\t\n ]+as[\t\n ]+)?([A-Za-z_$][\w$]*)$`)
	exemptionMarker    = regexp.MustCompile(internalRouteExemption + `[\t ]+([A-Za-z]+)[\t ]*:[\t ]*(.*)`)
)

// scanRouteModule attributes the token check to the exported handlers that
// perform it. A reference it cannot place inside one is reported rather than
// dropped: an unattributed gate is a route that would silently stay out of
// internal_http_routes, which is the failure this gate exists to catch.
func scanRouteModule(file, source string) routeModuleScan {
	code, comments := blankNonCode(source)
	scan := routeModuleScan{gated: map[string]bool{}, exemptions: map[string]string{}}
	regions := routeHandlerRegions(code)

	for _, reference := range gateReference.FindAllStringIndex(code, -1) {
		methods := handlerMethodsAt(regions, reference[0])
		called := nextSymbol(code, reference[1]) == '('
		switch {
		case len(methods) == 0 && !called:
			// The import, or a type position. Only a call gates a route.
		case len(methods) == 0:
			scan.problems = append(scan.problems, fmt.Sprintf("%s:%d: %s is called outside an exported route handler; move it into the handler so the route it gates can be matched against internal_http_routes", file, lineAt(source, reference[0]), internalCallGate))
		case !called:
			scan.problems = append(scan.problems, fmt.Sprintf("%s:%d: %s is referenced without being called; the route it gates is derived from the direct call in the handler", file, lineAt(source, reference[0]), internalCallGate))
		default:
			for _, method := range methods {
				scan.gated[method] = true
			}
		}
	}

	for _, comment := range comments {
		for _, marker := range exemptionMarker.FindAllStringSubmatch(comment, -1) {
			method := marker[1]
			reason := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(marker[2]), "*/"))
			switch {
			case !nextRouteHandlerMethods[method]:
				scan.problems = append(scan.problems, fmt.Sprintf("%s: %s names %q, which is not an HTTP method a route module serves", file, internalRouteExemption, method))
			case reason == "":
				scan.problems = append(scan.problems, fmt.Sprintf("%s: %s %s states no reason; an exemption without one is the silence it replaces", file, internalRouteExemption, method))
			default:
				scan.exemptions[method] = reason
			}
		}
	}
	for _, method := range sortedKeys(scan.exemptions) {
		if !scan.gated[method] {
			scan.problems = append(scan.problems, fmt.Sprintf("%s: %s %s exempts a handler that does not check the cluster-internal token", file, internalRouteExemption, method))
			delete(scan.exemptions, method)
		}
	}
	return scan
}

// handlerRegion is the body of one function, and the HTTP methods a route
// module serves from it. An aliased export (`export { handler as GET }`) makes
// that more than one method.
type handlerRegion struct {
	methods    []string
	start, end int
}

func routeHandlerRegions(code string) []handlerRegion {
	bodies := make(map[string][2]int)
	for _, match := range functionDefinition.FindAllStringSubmatchIndex(code, -1) {
		start, end := functionBody(code, match[1]-1)
		if start >= 0 {
			bodies[code[match[2]:match[3]]] = [2]int{start, end}
		}
	}
	var regions []handlerRegion
	for _, match := range exportedFunction.FindAllStringSubmatchIndex(code, -1) {
		name := code[match[2]:match[3]]
		body, defined := bodies[name]
		if !nextRouteHandlerMethods[name] || !defined {
			continue
		}
		regions = append(regions, handlerRegion{methods: []string{name}, start: body[0], end: body[1]})
	}
	aliased := make(map[string][]string)
	for _, list := range exportList.FindAllStringSubmatch(code, -1) {
		for _, entry := range strings.Split(list[1], ",") {
			alias := exportAlias.FindStringSubmatch(strings.TrimSpace(entry))
			if alias == nil || !nextRouteHandlerMethods[alias[2]] {
				continue
			}
			local := alias[1]
			if local == "" {
				local = alias[2]
			}
			aliased[local] = append(aliased[local], alias[2])
		}
	}
	for _, local := range sortedKeys(aliased) {
		body, defined := bodies[local]
		if !defined {
			continue
		}
		sort.Strings(aliased[local])
		regions = append(regions, handlerRegion{methods: aliased[local], start: body[0], end: body[1]})
	}
	return regions
}

func handlerMethodsAt(regions []handlerRegion, offset int) []string {
	var methods []string
	for _, region := range regions {
		if offset >= region.start && offset < region.end {
			methods = append(methods, region.methods...)
		}
	}
	return methods
}

// functionBody returns the span of the body following the parameter list that
// opens at parenthesis. The parameter list is matched first because a
// destructured parameter opens a brace before the body does.
func functionBody(code string, parenthesis int) (int, int) {
	if parenthesis < 0 || parenthesis >= len(code) || code[parenthesis] != '(' {
		return -1, -1
	}
	closing := matchDelimiter(code, parenthesis, '(', ')')
	if closing < 0 {
		return -1, -1
	}
	brace := strings.IndexByte(code[closing:], '{')
	if brace < 0 {
		return -1, -1
	}
	brace += closing
	end := matchDelimiter(code, brace, '{', '}')
	if end < 0 {
		return -1, -1
	}
	return brace, end + 1
}

func matchDelimiter(code string, start int, open, closed byte) int {
	depth := 0
	for index := start; index < len(code); index++ {
		switch code[index] {
		case open:
			depth++
		case closed:
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

// blankNonCode replaces comments, string and template literals, and regular
// expressions with spaces of the same length, so brace matching and identifier
// lookup see executable code at unchanged offsets. The comment texts are
// returned rather than discarded: an exemption marker lives in one.
func blankNonCode(source string) (string, []string) {
	buffer := []byte(source)
	var comments []string
	blank := func(from, to int) {
		for index := from; index < to; index++ {
			if buffer[index] != '\n' {
				buffer[index] = ' '
			}
		}
	}
	previous := byte(0)
	for index := 0; index < len(buffer); {
		current := buffer[index]
		next := byte(0)
		if index+1 < len(buffer) {
			next = buffer[index+1]
		}
		switch {
		case current == '/' && next == '/':
			end := index
			for end < len(buffer) && buffer[end] != '\n' {
				end++
			}
			comments = append(comments, source[index:end])
			blank(index, end)
			index = end
		case current == '/' && next == '*':
			end := index + 2
			for end+1 < len(buffer) && !(buffer[end] == '*' && buffer[end+1] == '/') {
				end++
			}
			end = min(end+2, len(buffer))
			comments = append(comments, source[index:end])
			blank(index, end)
			index = end
		case current == '\'' || current == '"' || current == '`' || (current == '/' && opensRegularExpression(previous)):
			end := closingDelimiter(buffer, index)
			blank(index, end)
			index = end
			// A literal is a value, so a division that follows it is not a
			// regular expression.
			previous = 'x'
		default:
			if current != ' ' && current != '\t' && current != '\n' && current != '\r' {
				previous = current
			}
			index++
		}
	}
	return string(buffer), comments
}

func closingDelimiter(buffer []byte, start int) int {
	delimiter := buffer[start]
	for index := start + 1; index < len(buffer); index++ {
		switch buffer[index] {
		case '\\':
			index++
		case delimiter:
			return index + 1
		}
	}
	return len(buffer)
}

// opensRegularExpression distinguishes a regular-expression literal from a
// division by what precedes the slash: an operator or an opening delimiter can
// only be followed by a value.
func opensRegularExpression(previous byte) bool {
	return previous == 0 || strings.IndexByte("(,=:[!&|?{};+-*%^~<>", previous) >= 0
}

// routeModulePath derives the served path from a route module's directory, the
// way Next.js does: a route group contributes no segment, and a dynamic segment
// makes the path unmatchable by a mesh policy, which matches literals.
func routeModulePath(appDir, routeFile string) (string, bool) {
	relative, err := filepath.Rel(appDir, filepath.Dir(routeFile))
	if err != nil || relative == "." {
		return "/", false
	}
	dynamic := false
	segments := make([]string, 0, 8)
	for _, segment := range strings.Split(filepath.ToSlash(relative), "/") {
		if strings.HasPrefix(segment, "(") && strings.HasSuffix(segment, ")") {
			continue
		}
		if strings.HasPrefix(segment, "[") {
			dynamic = true
		}
		segments = append(segments, segment)
	}
	return "/" + strings.Join(segments, "/"), dynamic
}

func loadTopologyBindings(t *testing.T, moduleDir string) topologyBindings {
	t.Helper()
	path := filepath.Join(moduleDir, filepath.FromSlash(topologyBindingFile))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", topologyBindingFile, err)
	}
	var bindings topologyBindings
	if err := yaml.Unmarshal(body, &bindings); err != nil {
		t.Fatalf("parse %s: %v", topologyBindingFile, err)
	}
	if len(bindings.Services) == 0 {
		t.Fatalf("%s declares no services", topologyBindingFile)
	}
	return bindings
}

// composedServiceNames is the services a consumer actually took. A composition
// omitting a service legitimately omits its source tree, so a route declared
// for it is not drift; nil means enforce every service, which is the canonical
// module and any consumer without an explicit list.
func composedServiceNames(t *testing.T, moduleDir string) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(moduleDir, moduleManifestFile))
	if err != nil {
		t.Fatalf("read %s: %v", moduleManifestFile, err)
	}
	var manifest struct {
		Services []struct {
			Name string `yaml:"name"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("parse %s: %v", moduleManifestFile, err)
	}
	if len(manifest.Services) == 0 {
		return nil
	}
	names := make(map[string]bool, len(manifest.Services))
	for _, service := range manifest.Services {
		names[service.Name] = true
	}
	return names
}

func skipNonSourceDirectory(name string) error {
	switch name {
	case ".next", "__mocks__", "__tests__", "node_modules":
		return filepath.SkipDir
	}
	return nil
}

func isRouteModule(name string) bool {
	switch name {
	case "route.ts", "route.tsx", "route.js", "route.jsx", "route.mjs":
		return true
	}
	return false
}

func typeScriptSource(name string) bool {
	switch filepath.Ext(name) {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return true
	}
	return false
}

// nextSymbol is the first byte after offset that is not whitespace, or 0.
func nextSymbol(code string, offset int) byte {
	for index := offset; index < len(code); index++ {
		switch code[index] {
		case ' ', '\t', '\n', '\r':
		default:
			return code[index]
		}
	}
	return 0
}

func lineAt(source string, offset int) int {
	return 1 + strings.Count(source[:offset], "\n")
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
