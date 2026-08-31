package cataloggen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"go/token"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"accounts/pkg/business"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

const (
	restSurfaceSchemaVersion = "saas.rest.surface.v1"
	restBindingsVersion      = "v1"
)

type restBindings struct {
	Version  string                        `yaml:"version"`
	Services map[string]restServiceBinding `yaml:"services"`
}

type restServiceBinding struct {
	Kind   string `yaml:"kind"`
	Plugin string `yaml:"plugin,omitempty"`
}

// BuildRESTSurfaceCatalog selects only descriptor REST routes intentionally
// available at the public edge. Internal procedures and compatibility aliases
// are excluded by the validated gateway catalog before this projection.
func BuildRESTSurfaceCatalog(gatewayDocument []byte) (*catalogv1.RESTSurfaceCatalog, error) {
	gateway := &catalogv1.GatewayRouteCatalog{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(gatewayDocument, gateway); err != nil {
		return nil, fmt.Errorf("decode gateway route catalog: %w", err)
	}
	if err := ValidateGatewayRouteCatalog(gateway); err != nil {
		return nil, fmt.Errorf("validate gateway route catalog: %w", err)
	}

	surface := &catalogv1.RESTSurfaceCatalog{SchemaVersion: restSurfaceSchemaVersion}
	for _, route := range gateway.GetRoutes() {
		if route.GetProtocol() != catalogv1.GatewayProtocol_GATEWAY_PROTOCOL_REST {
			continue
		}
		cloned, _ := proto.Clone(route).(*catalogv1.GatewayRoute)
		surface.Routes = append(surface.Routes, cloned)
		if surface.Owner == nil {
			surface.Owner = cloneOwner(route.GetOwner())
		}
	}
	if err := ValidateRESTSurfaceCatalog(surface); err != nil {
		return nil, err
	}
	return surface, nil
}

// ValidateRESTSurfaceCatalog enforces the target-neutral REST contract without
// repairing ordering, ownership, or unsafe route metadata.
func ValidateRESTSurfaceCatalog(surface *catalogv1.RESTSurfaceCatalog) error {
	if surface == nil {
		return fmt.Errorf("REST surface catalog is nil")
	}
	if surface.GetSchemaVersion() != restSurfaceSchemaVersion {
		return fmt.Errorf("unsupported REST surface schema version %q", surface.GetSchemaVersion())
	}
	if surface.GetOwner().GetModule() == "" || surface.GetOwner().GetService() == "" {
		return fmt.Errorf("REST surface owner is incomplete")
	}
	if len(surface.GetRoutes()) == 0 {
		return fmt.Errorf("REST surface contains no routes")
	}

	previous := ""
	seen := make(map[string]string, len(surface.GetRoutes()))
	for _, route := range surface.GetRoutes() {
		if route == nil || route.GetProtocol() != catalogv1.GatewayProtocol_GATEWAY_PROTOCOL_REST ||
			route.GetSource() != catalogv1.GatewayRouteSource_GATEWAY_ROUTE_SOURCE_DESCRIPTOR {
			return fmt.Errorf("REST surface contains a non-descriptor REST route")
		}
		if route.GetOwner().GetModule() != surface.GetOwner().GetModule() ||
			route.GetOwner().GetService() != surface.GetOwner().GetService() {
			return fmt.Errorf("REST route %q owner does not match surface owner", route.GetPath())
		}
		if route.GetExposure() != policyv1.Exposure_EXPOSURE_PUBLIC &&
			route.GetExposure() != policyv1.Exposure_EXPOSURE_AUTHENTICATED {
			return fmt.Errorf("REST route %q has invalid public-edge exposure", route.GetPath())
		}
		if route.GetRewritePath() != "" || route.GetRemoveAfter() != "" || route.GetProcedure() == "" ||
			route.GetMethod() == "" || route.GetPath() == "" || route.GetUpstreamEndpoint() == "" {
			return fmt.Errorf("REST route %q identity or compatibility metadata is invalid", route.GetPath())
		}
		if route.GetMatch() == catalogv1.GatewayMatch_GATEWAY_MATCH_PATH_TEMPLATE {
			if err := validateSimplePathTemplate(route.GetPath()); err != nil {
				return err
			}
		} else if route.GetMatch() != catalogv1.GatewayMatch_GATEWAY_MATCH_EXACT {
			return fmt.Errorf("REST route %q has invalid match mode", route.GetPath())
		}
		key := route.GetMethod() + " " + route.GetPath()
		matchKey := openAPIRouteKey(route.GetMethod(), route.GetPath())
		if prior, exists := seen[matchKey]; exists {
			return fmt.Errorf("duplicate REST match %q for %q and %q", matchKey, prior, route.GetProcedure())
		}
		seen[matchKey] = route.GetProcedure()
		sortKey := key + "\x00" + route.GetProcedure()
		if previous != "" && sortKey <= previous {
			return fmt.Errorf("REST routes are not strictly sorted at %q", key)
		}
		previous = sortKey
	}
	return nil
}

// RenderRESTSurfaceCatalogJSON emits stable proto-name JSON.
func RenderRESTSurfaceCatalogJSON(surface *catalogv1.RESTSurfaceCatalog) ([]byte, error) {
	if err := ValidateRESTSurfaceCatalog(surface); err != nil {
		return nil, err
	}
	document, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(surface)
	if err != nil {
		return nil, fmt.Errorf("marshal REST surface catalog: %w", err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, document, "", "  "); err != nil {
		return nil, fmt.Errorf("format REST surface catalog: %w", err)
	}
	formatted.WriteByte('\n')
	return formatted.Bytes(), nil
}

func decodeRESTBindings(document []byte) (restBindings, error) {
	var bindings restBindings
	decoder := yaml.NewDecoder(bytes.NewReader(document))
	decoder.KnownFields(true)
	if err := decoder.Decode(&bindings); err != nil {
		return restBindings{}, fmt.Errorf("decode REST bindings: %w", err)
	}
	if bindings.Version != restBindingsVersion {
		return restBindings{}, fmt.Errorf("unsupported REST bindings version %q", bindings.Version)
	}
	if len(bindings.Services) == 0 {
		return restBindings{}, fmt.Errorf("REST bindings contain no services")
	}
	return bindings, nil
}

func restSurfaceServices(surface *catalogv1.RESTSurfaceCatalog, service *catalogv1.ServiceCatalog) (map[string]struct{}, error) {
	methods := make(map[string]*catalogv1.Method, len(service.GetMethods()))
	for _, method := range service.GetMethods() {
		methods[method.GetProcedure()] = method
	}
	services := make(map[string]struct{})
	for _, route := range surface.GetRoutes() {
		method := methods[route.GetProcedure()]
		if method == nil {
			return nil, fmt.Errorf("REST route %q references unknown procedure %q", route.GetPath(), route.GetProcedure())
		}
		services[method.GetService()] = struct{}{}
	}
	return services, nil
}

func validateRESTBindingCoverage(surface *catalogv1.RESTSurfaceCatalog, service *catalogv1.ServiceCatalog, bindings restBindings) error {
	wanted, err := restSurfaceServices(surface, service)
	if err != nil {
		return err
	}
	for name := range wanted {
		binding, exists := bindings.Services[name]
		if !exists {
			return fmt.Errorf("REST binding is missing surface service %q", name)
		}
		switch binding.Kind {
		case "generated":
			if binding.Plugin != "" {
				return fmt.Errorf("generated REST service %q declares plugin %q", name, binding.Plugin)
			}
		case "plugin":
			if !token.IsIdentifier(binding.Plugin) {
				return fmt.Errorf("plugin REST service %q has invalid plugin %q", name, binding.Plugin)
			}
		default:
			return fmt.Errorf("REST service %q has unsupported kind %q", name, binding.Kind)
		}
	}
	for name := range bindings.Services {
		if _, exists := wanted[name]; !exists {
			return fmt.Errorf("REST binding declares service %q absent from the public REST surface", name)
		}
	}
	return nil
}

// RenderAccountsRESTRuntime emits complete grpc-gateway registration and an
// exact method/path allowlist. Internal RPCs have no HTTP annotations; the
// allowlist remains a fail-closed runtime check against generator drift.
func RenderAccountsRESTRuntime(surface *catalogv1.RESTSurfaceCatalog, service *catalogv1.ServiceCatalog, bindingDocument []byte) ([]byte, error) {
	if err := ValidateRESTSurfaceCatalog(surface); err != nil {
		return nil, err
	}
	if err := business.ValidateServiceCatalog(service); err != nil {
		return nil, err
	}
	bindings, err := decodeRESTBindings(bindingDocument)
	if err != nil {
		return nil, err
	}
	if err := validateRESTBindingCoverage(surface, service, bindings); err != nil {
		return nil, err
	}

	wanted, _ := restSurfaceServices(surface, service)
	serviceNames := make([]string, 0, len(wanted))
	plugins := make(map[string]struct{})
	for name := range wanted {
		serviceNames = append(serviceNames, name)
		if binding := bindings.Services[name]; binding.Kind == "plugin" {
			plugins[binding.Plugin] = struct{}{}
		}
	}
	sort.Strings(serviceNames)
	pluginNames := make([]string, 0, len(plugins))
	for name := range plugins {
		pluginNames = append(pluginNames, name)
	}
	sort.Strings(pluginNames)

	var source strings.Builder
	source.WriteString(`// Code generated by cmd/rest-surface from generated/rest-surface.json and rest_bindings.yaml. DO NOT EDIT.

package adapters

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"google.golang.org/grpc"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"

	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/plugins"
)

func registerCatalogRESTHandlers(ctx context.Context, mux *runtime.ServeMux, endpoint string, options []grpc.DialOption) error {
`)
	for _, name := range serviceNames {
		if bindings.Services[name].Kind != "generated" {
			continue
		}
		fmt.Fprintf(&source, "\tif err := gen.Register%sHandlerFromEndpoint(ctx, mux, endpoint, options); err != nil {\n\t\treturn fmt.Errorf(%q, err)\n\t}\n",
			name, "register generated REST service "+name+": %w")
	}
	for _, plugin := range pluginNames {
		fmt.Fprintf(&source, "\tseenPlugin%s := false\n", exportedIdentifier(plugin))
	}
	source.WriteString("\tfor _, plugin := range plugins.All() {\n\t\tswitch plugin.Name() {\n")
	for _, plugin := range pluginNames {
		identifier := exportedIdentifier(plugin)
		fmt.Fprintf(&source, "\t\tcase %q:\n\t\t\tif seenPlugin%s {\n\t\t\t\treturn fmt.Errorf(%q)\n\t\t\t}\n\t\t\tif err := plugin.RegisterREST(ctx, mux, endpoint, options); err != nil {\n\t\t\t\treturn fmt.Errorf(%q, err)\n\t\t\t}\n\t\t\tseenPlugin%s = true\n",
			plugin, identifier, "REST plugin "+plugin+" is registered more than once", "register REST plugin "+plugin+": %w", identifier)
	}
	source.WriteString("\t\t}\n\t}\n")
	for _, plugin := range pluginNames {
		fmt.Fprintf(&source, "\tif !seenPlugin%s {\n\t\treturn fmt.Errorf(%q)\n\t}\n", exportedIdentifier(plugin), "required REST plugin "+plugin+" is not registered")
	}
	source.WriteString("\treturn nil\n}\n\n")

	source.WriteString("var catalogRESTExactRoutes = map[string]struct{}{\n")
	for _, route := range surface.GetRoutes() {
		if route.GetMatch() == catalogv1.GatewayMatch_GATEWAY_MATCH_EXACT {
			fmt.Fprintf(&source, "\t%q: {},\n", route.GetMethod()+" "+route.GetPath())
		}
	}
	source.WriteString("}\n\ntype catalogRESTTemplateRoute struct {\n\tmethod string\n\tpath *regexp.Regexp\n}\n\n")
	source.WriteString("var catalogRESTTemplateRoutes = []catalogRESTTemplateRoute{\n")
	for _, route := range surface.GetRoutes() {
		if route.GetMatch() == catalogv1.GatewayMatch_GATEWAY_MATCH_PATH_TEMPLATE {
			fmt.Fprintf(&source, "\t{method: %q, path: regexp.MustCompile(%q)},\n", route.GetMethod(), pathTemplateToRegex(route.GetPath()))
		}
	}
	source.WriteString(`}

func catalogRESTRouteAllowed(method, path string) bool {
	if _, ok := catalogRESTExactRoutes[method+" "+path]; ok {
		return true
	}
	for _, route := range catalogRESTTemplateRoutes {
		if route.method == method && route.path.MatchString(path) {
			return true
		}
	}
	return false
}

func catalogRESTHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if !catalogRESTRouteAllowed(request.Method, request.URL.Path) {
			http.NotFound(w, request)
			return
		}
		next.ServeHTTP(w, request)
	})
}
`)
	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		return nil, fmt.Errorf("format accounts REST runtime: %w", err)
	}
	return formatted, nil
}

func exportedIdentifier(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

// RenderAuthGatewayRESTRoutes emits the descriptor-owned edge whitelist. The
// separate YAML source contributes only non-protobuf extension routes.
func RenderAuthGatewayRESTRoutes(surface *catalogv1.RESTSurfaceCatalog) ([]byte, error) {
	if err := ValidateRESTSurfaceCatalog(surface); err != nil {
		return nil, err
	}
	var source strings.Builder
	source.WriteString(`// Code generated by cmd/rest-surface from generated/rest-surface.json. DO NOT EDIT.

package main

func generatedCatalogRESTRoutes() []*RouteEntry {
	return []*RouteEntry{
`)
	for _, route := range surface.GetRoutes() {
		fmt.Fprintf(&source, "\t\t{Service: %q, Method: %q, Path: %q, Procedure: %q},\n",
			route.GetOwner().GetService(), route.GetMethod(), route.GetPath(), route.GetProcedure())
	}
	source.WriteString("\t}\n}\n")
	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		return nil, fmt.Errorf("format auth-gateway REST routes: %w", err)
	}
	return formatted, nil
}

// RenderPublicOpenAPI removes internal-only operations from protoc-gen-
// openapiv2 output, verifies complete public REST parity, and prunes schemas
// that are no longer reachable from the public document.
func RenderPublicOpenAPI(rawDocument []byte, surface *catalogv1.RESTSurfaceCatalog, service *catalogv1.ServiceCatalog) ([]byte, error) {
	if err := ValidateRESTSurfaceCatalog(surface); err != nil {
		return nil, err
	}
	if err := business.ValidateServiceCatalog(service); err != nil {
		return nil, err
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(rawDocument))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode OpenAPI document: %w", err)
	}
	if document["swagger"] != "2.0" {
		return nil, fmt.Errorf("unsupported OpenAPI version %v", document["swagger"])
	}
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("OpenAPI paths object is missing")
	}

	allBindings := make(map[string]*catalogv1.Method)
	for _, method := range service.GetMethods() {
		for _, binding := range method.GetHttpBindings() {
			key := openAPIRouteKey(binding.GetMethod(), binding.GetPath())
			if previous := allBindings[key]; previous != nil {
				return nil, fmt.Errorf("normalized HTTP binding %q is ambiguous", key)
			}
			allBindings[key] = method
		}
	}
	allowed := make(map[string]*catalogv1.GatewayRoute, len(surface.GetRoutes()))
	for _, route := range surface.GetRoutes() {
		allowed[openAPIRouteKey(route.GetMethod(), route.GetPath())] = route
	}

	filteredPaths := make(map[string]any)
	seen := make(map[string]struct{}, len(allowed))
	for path, rawPathItem := range paths {
		pathItem, ok := rawPathItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("OpenAPI path %q is not an object", path)
		}
		filteredItem := make(map[string]any)
		for method, operation := range pathItem {
			upperMethod := strings.ToUpper(method)
			if !isOpenAPIOperationMethod(upperMethod) {
				filteredItem[method] = operation
				continue
			}
			key := openAPIRouteKey(upperMethod, path)
			catalogMethod := allBindings[key]
			if catalogMethod == nil {
				return nil, fmt.Errorf("OpenAPI operation %q %q is absent from the service catalog", upperMethod, path)
			}
			if _, public := allowed[key]; public {
				filteredItem[method] = operation
				seen[key] = struct{}{}
				continue
			}
			return nil, fmt.Errorf("OpenAPI operation %q is absent from the public REST surface", key)
		}
		operationCount := 0
		for method := range filteredItem {
			if isOpenAPIOperationMethod(strings.ToUpper(method)) {
				operationCount++
			}
		}
		if operationCount > 0 {
			filteredPaths[path] = filteredItem
		}
	}
	if len(seen) != len(allowed) {
		missing := make([]string, 0)
		for key := range allowed {
			if _, exists := seen[key]; !exists {
				missing = append(missing, key)
			}
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("OpenAPI is missing %d public REST operations: %s", len(missing), strings.Join(missing, ", "))
	}
	document["paths"] = filteredPaths
	document["x-codefly-rest-schema"] = surface.GetSchemaVersion()
	document["x-codefly-owner"] = map[string]any{
		"module":  surface.GetOwner().GetModule(),
		"service": surface.GetOwner().GetService(),
	}
	if err := pruneOpenAPIDefinitions(document); err != nil {
		return nil, err
	}

	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode public OpenAPI: %w", err)
	}
	return output.Bytes(), nil
}

func openAPIRouteKey(method, path string) string {
	normalized := pathParameterPattern.ReplaceAllString(path, "{}")
	return strings.ToUpper(method) + " " + normalized
}

func isOpenAPIOperationMethod(method string) bool {
	switch method {
	case "GET", "PUT", "POST", "DELETE", "PATCH", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

func pruneOpenAPIDefinitions(document map[string]any) error {
	rawDefinitions, _ := document["definitions"].(map[string]any)
	delete(document, "definitions")
	needed := collectOpenAPIRefs(document)
	selected := make(map[string]any)
	for len(needed) > 0 {
		name := needed[0]
		needed = needed[1:]
		if _, exists := selected[name]; exists {
			continue
		}
		definition, exists := rawDefinitions[name]
		if !exists {
			return fmt.Errorf("OpenAPI references missing definition %q", name)
		}
		selected[name] = definition
		needed = append(needed, collectOpenAPIRefs(definition)...)
	}
	document["definitions"] = selected
	return nil
}

func collectOpenAPIRefs(value any) []string {
	var refs []string
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				if reference, ok := child.(string); ok && strings.HasPrefix(reference, "#/definitions/") {
					refs = append(refs, strings.TrimPrefix(reference, "#/definitions/"))
				}
				continue
			}
			refs = append(refs, collectOpenAPIRefs(child)...)
		}
	case []any:
		for _, child := range typed {
			refs = append(refs, collectOpenAPIRefs(child)...)
		}
	}
	return refs
}
