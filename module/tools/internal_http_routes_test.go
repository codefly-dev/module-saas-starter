package tools

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	// internalHTTPMethodSource holds the method set the topology compiler will
	// accept in internal_http_routes. It is read rather than restated: a second
	// copy here is the same drift this gate exists to catch, one artifact over.
	internalHTTPMethodSource = "services/accounts/code/pkg/cataloggen/deployment_topology.go"
	internalHTTPMethodMap    = "internalHTTPMethods"
	// internalTokenModule defines the gate. It is named by path because the
	// declaration form is not the gate's identity: rewriting it as an exported
	// const would make the defining module fail its own check.
	internalTokenModule = "src/lib/internal-token.ts"
)

// nextRouteHandlerMethods is the set of exported names Next.js serves as route
// handlers from a route module. It is deliberately wider than what
// internal_http_routes can express: Next serves HEAD and OPTIONS, the mesh
// binding cannot name them, and a gated handler on one of those is a conflict
// to report rather than a declaration to demand.
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
	declarable := declarableInternalHTTPMethods(t, moduleDir)

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
		compareInternalHTTPRoutes(t, service.Name, gatedRouteHandlers(t, moduleDir, appDir, declarable), declaredInternalRoutes(service))
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
			if !strings.Contains(code, internalCallGate) {
				return nil
			}
			relative, relErr := filepath.Rel(moduleDir, path)
			if relErr != nil {
				return relErr
			}
			slashed := filepath.ToSlash(relative)
			if strings.HasSuffix(slashed, internalTokenModule) {
				return nil
			}
			t.Errorf("%s: %s is reached outside a route module; internal_http_routes is checked against the exported handler that calls it, so a route gated here would go undeclared", slashed, internalCallGate)
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

// declarableInternalHTTPMethods reads the topology compiler's own accepted
// method set out of its source, so this gate cannot demand a declaration the
// compiler rejects.
func declarableInternalHTTPMethods(t *testing.T, moduleDir string) map[string]bool {
	t.Helper()
	path := filepath.Join(moduleDir, filepath.FromSlash(internalHTTPMethodSource))
	literal := findMapLiteral(parseInventorySource(t, path), internalHTTPMethodMap)
	if literal == nil {
		t.Fatalf("%s does not declare %s as a map literal", internalHTTPMethodSource, internalHTTPMethodMap)
	}
	methods := make(map[string]bool, len(literal.Elts))
	for _, element := range literal.Elts {
		entry, ok := element.(*ast.KeyValueExpr)
		if !ok {
			t.Fatalf("%s: unexpected entry in %s", internalHTTPMethodSource, internalHTTPMethodMap)
		}
		key, ok := entry.Key.(*ast.BasicLit)
		if !ok || key.Kind != token.STRING {
			t.Fatalf("%s: %s is keyed by something other than a method literal", internalHTTPMethodSource, internalHTTPMethodMap)
		}
		method, err := strconv.Unquote(key.Value)
		if err != nil {
			t.Fatalf("%s: %s holds an unparsable method %s", internalHTTPMethodSource, internalHTTPMethodMap, key.Value)
		}
		methods[method] = true
	}
	if len(methods) == 0 {
		t.Fatalf("%s: %s is empty", internalHTTPMethodSource, internalHTTPMethodMap)
	}
	return methods
}

func sortedSet(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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

func gatedRouteHandlers(t *testing.T, moduleDir, appDir string, declarable map[string]bool) []gatedRouteHandler {
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
		routePath, dynamic, pathErr := routeModulePath(appDir, path)
		if pathErr != nil {
			return pathErr
		}
		if dynamic {
			t.Errorf("%s: a handler here checks the cluster-internal token, but the mesh matches literal paths and %s carries a dynamic segment", relative, routePath)
			return nil
		}
		gated, undeclarable := partitionGatedMethods(scan.gated, declarable)
		for _, method := range undeclarable {
			t.Errorf("%s: %s checks the cluster-internal token, but internal_http_routes admits only %s, so the route it serves cannot be denied in the mesh", relative, method, strings.Join(sortedSet(declarable), ", "))
		}
		for _, method := range gated {
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

// partitionGatedMethods splits the methods a module gates into those
// internal_http_routes can name and those it cannot. Next.js serves HEAD and
// OPTIONS; the topology compiler rejects them. Demanding a declaration for one
// would leave the author with a gate they cannot satisfy and an exemption
// marker that would then misstate a route they do want denied.
func partitionGatedMethods(gated, declarable map[string]bool) ([]string, []string) {
	var declarableMethods, undeclarable []string
	for _, method := range sortedKeys(gated) {
		if declarable[method] {
			declarableMethods = append(declarableMethods, method)
			continue
		}
		undeclarable = append(undeclarable, method)
	}
	return declarableMethods, undeclarable
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
	// A module-scope declaration starts at column zero. Allowing leading
	// whitespace let a function nested inside a handler shadow the module-scope
	// binding of the same name, which is the only thing an aliased export can
	// refer to.
	exportedFunction   = regexp.MustCompile(`(?m)^export[\t ]+(?:async[\t ]+)?function[\t ]+([A-Za-z_$][\w$]*)[\t ]*\(`)
	functionDefinition = regexp.MustCompile(`(?m)^(?:export[\t ]+)?(?:async[\t ]+)?function[\t ]+([A-Za-z_$][\w$]*)[\t ]*\(`)
	exportList         = regexp.MustCompile(`\bexport[\t\n ]*\{([^}]*)\}`)
	exportAlias        = regexp.MustCompile(`^(?:([A-Za-z_$][\w$]*)[\t\n ]+as[\t\n ]+)?([A-Za-z_$][\w$]*)$`)
	importClause       = regexp.MustCompile(`\bimport[\t\n ]*\{([^}]*)\}[\t\n ]*from`)
	importBinding      = regexp.MustCompile(`^(?:type[\t\n ]+)?([A-Za-z_$][\w$]*)(?:[\t\n ]+as[\t\n ]+([A-Za-z_$][\w$]*))?$`)
	exemptionMarker    = regexp.MustCompile(internalRouteExemption + `[\t ]+([A-Za-z]+)[\t ]*:[\t ]*(.*)`)
)

// gateLocalNames is every name the gate is bound to in this module. An import
// clause may rename it (`import { isTrustedInternalCall as gate }`), which is
// ordinary TypeScript, so the name called in the handler is not necessarily the
// exported one. Resolving the binding is what makes the call attributable;
// matching the exported name alone read an aliased gate as absent, and an
// absent gate is a route nothing requires to be declared.
func gateLocalNames(code string) ([]string, [][]int) {
	names := []string{internalCallGate}
	clauses := importClause.FindAllStringSubmatchIndex(code, -1)
	spans := make([][]int, 0, len(clauses))
	for _, clause := range clauses {
		spans = append(spans, []int{clause[0], clause[1]})
		for _, entry := range strings.Split(code[clause[2]:clause[3]], ",") {
			binding := importBinding.FindStringSubmatch(strings.TrimSpace(entry))
			if binding == nil || binding[1] != internalCallGate || binding[2] == "" {
				continue
			}
			names = append(names, binding[2])
		}
	}
	return names, spans
}

func gateReferencePattern(names []string) *regexp.Regexp {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, regexp.QuoteMeta(name))
	}
	return regexp.MustCompile(`\b(?:` + strings.Join(quoted, "|") + `)\b`)
}

// scanRouteModule attributes the token check to the exported handlers that
// perform it. A reference it cannot place inside one is reported rather than
// dropped: an unattributed gate is a route that would silently stay out of
// internal_http_routes, which is the failure this gate exists to catch.
func scanRouteModule(file, source string) routeModuleScan {
	code, comments := blankNonCode(source)
	scan := routeModuleScan{gated: map[string]bool{}, exemptions: map[string]string{}}
	if !balancedDelimiters(code) {
		scan.problems = append(scan.problems, fmt.Sprintf("%s: delimiters do not balance after comments and literals are removed, so this module cannot be read reliably; the gate refuses to report it as ungated", file))
		return scan
	}
	regions := routeHandlerRegions(code)
	names, importSpans := gateLocalNames(code)

	for _, reference := range gateReferencePattern(names).FindAllStringIndex(code, -1) {
		if withinAny(importSpans, reference[0]) {
			continue
		}
		methods := handlerMethodsAt(regions, reference[0])
		called := nextSymbol(code, reference[1]) == '('
		switch {
		case len(methods) == 0 && !called:
			scan.problems = append(scan.problems, fmt.Sprintf("%s:%d: %s is bound to another name outside a route handler; the route it gates is derived from the call in the handler, so the binding hides it", file, lineAt(source, reference[0]), code[reference[0]:reference[1]]))
		case len(methods) == 0:
			scan.problems = append(scan.problems, fmt.Sprintf("%s:%d: %s is called outside an exported route handler; move it into the handler so the route it gates can be matched against internal_http_routes", file, lineAt(source, reference[0]), code[reference[0]:reference[1]]))
		case !called:
			scan.problems = append(scan.problems, fmt.Sprintf("%s:%d: %s is referenced without being called; the route it gates is derived from the direct call in the handler", file, lineAt(source, reference[0]), code[reference[0]:reference[1]]))
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
	previousWord := ""
	word := make([]byte, 0, 16)
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
		case current == '\'' || current == '"' || current == '`' || (current == '/' && opensRegularExpression(previous, previousWord)):
			end := closingDelimiter(buffer, index)
			blank(index, end)
			index = end
			// A literal is a value, so a division that follows it is not a
			// regular expression. The word is cleared with it: `return "a" / 2`
			// divides, and leaving "return" standing would read it as a regular
			// expression and blank through the rest of the statement.
			previous = 'x'
			previousWord = ""
			word = word[:0]
		default:
			if identifierByte(current) {
				word = append(word, current)
			} else if len(word) > 0 {
				previousWord = string(word)
				word = word[:0]
			}
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

// regexPrecedingKeywords are the keywords after which a slash can only open a
// regular expression. Reading the preceding *punctuation* alone was not enough:
// `return /["']/.test(id)` ends in a letter, so the slash read as a division and
// the quote inside the character class opened a literal that blanked through the
// rest of the module — including the token check it was meant to find.
var regexPrecedingKeywords = map[string]bool{
	"await":      true,
	"case":       true,
	"delete":     true,
	"do":         true,
	"else":       true,
	"in":         true,
	"instanceof": true,
	"new":        true,
	"of":         true,
	"return":     true,
	"throw":      true,
	"typeof":     true,
	"void":       true,
	"yield":      true,
}

// opensRegularExpression distinguishes a regular-expression literal from a
// division by the token before the slash: an operator, an opening delimiter or
// a keyword can only be followed by a value, while an identifier, a literal or
// a closing delimiter is one.
func opensRegularExpression(previous byte, previousWord string) bool {
	if previous == 0 {
		return true
	}
	if identifierByte(previous) {
		return regexPrecedingKeywords[previousWord]
	}
	return strings.IndexByte("(,=:[!&|?{};+-*%^~<>", previous) >= 0
}

func identifierByte(current byte) bool {
	return current == '_' || current == '$' ||
		(current >= 'a' && current <= 'z') ||
		(current >= 'A' && current <= 'Z') ||
		(current >= '0' && current <= '9')
}

// routeModulePath derives the served path from a route module's directory, the
// way Next.js does: a route group contributes no segment, and a dynamic segment
// makes the path unmatchable by a mesh policy, which matches literals.
func routeModulePath(appDir, routeFile string) (string, bool, error) {
	relative, err := filepath.Rel(appDir, filepath.Dir(routeFile))
	if err != nil {
		return "", false, fmt.Errorf("resolve %s under %s: %w", routeFile, appDir, err)
	}
	if relative == "." {
		return "/", false, nil
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
	return "/" + strings.Join(segments, "/"), dynamic, nil
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

func withinAny(spans [][]int, offset int) bool {
	for _, span := range spans {
		if offset >= span[0] && offset < span[1] {
			return true
		}
	}
	return false
}

// balancedDelimiters is the backstop for the lexer being wrong in a way nobody
// anticipated. Every brace, parenthesis and bracket in real TypeScript that is
// not inside a comment or a literal is matched, so a blanking mistake that
// swallows code shows up here as an imbalance. Without it, a mis-lexed module
// reports no gated handler at all, which is indistinguishable from a module
// that gates nothing.
func balancedDelimiters(code string) bool {
	var stack []byte
	pairs := map[byte]byte{')': '(', ']': '[', '}': '{'}
	for index := 0; index < len(code); index++ {
		switch code[index] {
		case '(', '[', '{':
			stack = append(stack, code[index])
		case ')', ']', '}':
			if len(stack) == 0 || stack[len(stack)-1] != pairs[code[index]] {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0
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
