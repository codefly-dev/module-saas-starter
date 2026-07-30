package cataloggen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"regexp"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"accounts/pkg/business"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

const gatewayRouteSchemaVersion = "saas.gateway.routes.v1"

var (
	endpointNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	protoPackagePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	pathParameterPattern = regexp.MustCompile(`\{[A-Za-z][A-Za-z0-9_]*\}`)
)

type gatewayBindings struct {
	Version              string                      `yaml:"version"`
	CompatibilityAliases []gatewayCompatibilityAlias `yaml:"compatibility_aliases"`
}

type gatewayEndpoints struct {
	Connect string `yaml:"connect"`
	REST    string `yaml:"rest"`
}

type gatewayCompatibilityAlias struct {
	Protocol    string `yaml:"protocol"`
	FromPackage string `yaml:"from_package"`
	ToPackage   string `yaml:"to_package"`
	RemoveAfter string `yaml:"remove_after"`
}

// BuildGatewayRouteCatalog derives the complete public protobuf edge surface.
// Internal procedures are deliberately omitted rather than marked protected.
func BuildGatewayRouteCatalog(serviceDocument, bindingDocument, topologyDocument []byte) (*catalogv1.GatewayRouteCatalog, error) {
	serviceCatalog := &catalogv1.ServiceCatalog{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(serviceDocument, serviceCatalog); err != nil {
		return nil, fmt.Errorf("decode service catalog: %w", err)
	}
	if err := business.ValidateServiceCatalog(serviceCatalog); err != nil {
		return nil, fmt.Errorf("validate service catalog: %w", err)
	}
	bindings, err := decodeGatewayBindings(bindingDocument)
	if err != nil {
		return nil, err
	}
	endpoints, err := gatewayEndpointsFromDeployment(serviceCatalog, topologyDocument)
	if err != nil {
		return nil, err
	}

	routes := &catalogv1.GatewayRouteCatalog{SchemaVersion: gatewayRouteSchemaVersion}
	for _, method := range serviceCatalog.GetMethods() {
		exposure := method.GetPolicy().GetExposure()
		if exposure == policyv1.Exposure_EXPOSURE_INTERNAL {
			continue
		}
		if exposure != policyv1.Exposure_EXPOSURE_PUBLIC && exposure != policyv1.Exposure_EXPOSURE_AUTHENTICATED {
			return nil, fmt.Errorf("procedure %q has unsupported gateway exposure %s", method.GetProcedure(), exposure)
		}

		if methodHasProtocol(method, catalogv1.Protocol_PROTOCOL_CONNECT) {
			canonical := &catalogv1.GatewayRoute{
				Protocol:         catalogv1.GatewayProtocol_GATEWAY_PROTOCOL_CONNECT,
				Method:           "POST",
				Path:             method.GetProcedure(),
				Match:            catalogv1.GatewayMatch_GATEWAY_MATCH_EXACT,
				Procedure:        method.GetProcedure(),
				Owner:            cloneOwner(method.GetOwner()),
				UpstreamEndpoint: endpoints.Connect,
				Exposure:         exposure,
				Source:           catalogv1.GatewayRouteSource_GATEWAY_ROUTE_SOURCE_DESCRIPTOR,
			}
			routes.Routes = append(routes.Routes, canonical)
			for _, alias := range bindings.CompatibilityAliases {
				if alias.Protocol != "connect" || alias.FromPackage != serviceCatalog.GetApiPackage() {
					continue
				}
				prefix := "/" + alias.FromPackage + "."
				if !strings.HasPrefix(method.GetProcedure(), prefix) {
					return nil, fmt.Errorf("procedure %q does not match configured alias package %q", method.GetProcedure(), alias.FromPackage)
				}
				routes.Routes = append(routes.Routes, &catalogv1.GatewayRoute{
					Protocol:         catalogv1.GatewayProtocol_GATEWAY_PROTOCOL_CONNECT,
					Method:           "POST",
					Path:             strings.Replace(method.GetProcedure(), prefix, "/"+alias.ToPackage+".", 1),
					Match:            catalogv1.GatewayMatch_GATEWAY_MATCH_EXACT,
					Procedure:        method.GetProcedure(),
					Owner:            cloneOwner(method.GetOwner()),
					UpstreamEndpoint: endpoints.Connect,
					Exposure:         exposure,
					Source:           catalogv1.GatewayRouteSource_GATEWAY_ROUTE_SOURCE_COMPATIBILITY_ALIAS,
					RewritePath:      method.GetProcedure(),
					RemoveAfter:      alias.RemoveAfter,
				})
			}
		}

		for _, binding := range method.GetHttpBindings() {
			match := catalogv1.GatewayMatch_GATEWAY_MATCH_EXACT
			if strings.Contains(binding.GetPath(), "{") {
				if err := validateSimplePathTemplate(binding.GetPath()); err != nil {
					return nil, fmt.Errorf("procedure %q: %w", method.GetProcedure(), err)
				}
				match = catalogv1.GatewayMatch_GATEWAY_MATCH_PATH_TEMPLATE
			}
			routes.Routes = append(routes.Routes, &catalogv1.GatewayRoute{
				Protocol:         catalogv1.GatewayProtocol_GATEWAY_PROTOCOL_REST,
				Method:           strings.ToUpper(binding.GetMethod()),
				Path:             binding.GetPath(),
				Match:            match,
				Procedure:        method.GetProcedure(),
				Owner:            cloneOwner(method.GetOwner()),
				UpstreamEndpoint: endpoints.REST,
				Exposure:         exposure,
				Source:           catalogv1.GatewayRouteSource_GATEWAY_ROUTE_SOURCE_DESCRIPTOR,
			})
		}
	}

	sort.Slice(routes.Routes, func(i, j int) bool {
		return gatewayRouteSortKey(routes.Routes[i]) < gatewayRouteSortKey(routes.Routes[j])
	})
	if err := ValidateGatewayRouteCatalog(routes); err != nil {
		return nil, err
	}
	return routes, nil
}

func methodHasProtocol(method *catalogv1.Method, wanted catalogv1.Protocol) bool {
	for _, protocol := range method.GetProtocols() {
		if protocol == wanted {
			return true
		}
	}
	return false
}

func cloneOwner(owner *catalogv1.ServiceOwner) *catalogv1.ServiceOwner {
	return &catalogv1.ServiceOwner{Module: owner.GetModule(), Service: owner.GetService()}
}

func decodeGatewayBindings(document []byte) (gatewayBindings, error) {
	var bindings gatewayBindings
	decoder := yaml.NewDecoder(bytes.NewReader(document))
	decoder.KnownFields(true)
	if err := decoder.Decode(&bindings); err != nil {
		return gatewayBindings{}, fmt.Errorf("decode gateway bindings: %w", err)
	}
	if bindings.Version != "v1" {
		return gatewayBindings{}, fmt.Errorf("unsupported gateway bindings version %q", bindings.Version)
	}
	for index, alias := range bindings.CompatibilityAliases {
		if alias.Protocol != "connect" {
			return gatewayBindings{}, fmt.Errorf("compatibility alias %d has unsupported protocol %q", index, alias.Protocol)
		}
		if !protoPackagePattern.MatchString(alias.FromPackage) || !protoPackagePattern.MatchString(alias.ToPackage) || alias.FromPackage == alias.ToPackage {
			return gatewayBindings{}, fmt.Errorf("compatibility alias %d has invalid package mapping", index)
		}
		if _, err := time.Parse("2006-01-02", alias.RemoveAfter); err != nil {
			return gatewayBindings{}, fmt.Errorf("compatibility alias %d has invalid remove_after %q", index, alias.RemoveAfter)
		}
	}
	return bindings, nil
}

func gatewayEndpointsFromDeployment(serviceCatalog *catalogv1.ServiceCatalog, document []byte) (gatewayEndpoints, error) {
	bindings, err := decodeDeploymentBindings(document)
	if err != nil {
		return gatewayEndpoints{}, err
	}
	if err := validateDeploymentBindings(serviceCatalog, bindings); err != nil {
		return gatewayEndpoints{}, err
	}
	var endpoints gatewayEndpoints
	for _, service := range bindings.Services {
		if service.Name != serviceCatalog.GetOwner().GetService() {
			continue
		}
		for _, endpoint := range service.Endpoints {
			switch endpoint.API {
			case "connect":
				if endpoints.Connect != "" {
					return gatewayEndpoints{}, fmt.Errorf("accounts topology declares multiple Connect endpoints")
				}
				endpoints.Connect = endpoint.Name
			case "rest":
				if endpoints.REST != "" {
					return gatewayEndpoints{}, fmt.Errorf("accounts topology declares multiple REST endpoints")
				}
				endpoints.REST = endpoint.Name
			}
		}
	}
	if endpoints.Connect == "" || endpoints.REST == "" {
		return gatewayEndpoints{}, fmt.Errorf("accounts topology is missing Connect or REST gateway endpoint")
	}
	return endpoints, nil
}

func validateSimplePathTemplate(path string) error {
	withoutParameters := pathParameterPattern.ReplaceAllString(path, "")
	if strings.ContainsAny(withoutParameters, "{}") {
		return fmt.Errorf("HTTP path %q uses an unsupported path template", path)
	}
	return nil
}

func gatewayRouteSortKey(route *catalogv1.GatewayRoute) string {
	return fmt.Sprintf("%02d\x00%s\x00%s\x00%s\x00%02d", route.GetProtocol(), route.GetMethod(), route.GetPath(), route.GetProcedure(), route.GetSource())
}

// ValidateGatewayRouteCatalog enforces proxy-safe routing and compatibility
// alias invariants without repairing malformed artifacts.
func ValidateGatewayRouteCatalog(catalog *catalogv1.GatewayRouteCatalog) error {
	if catalog == nil {
		return fmt.Errorf("gateway route catalog is nil")
	}
	if catalog.GetSchemaVersion() != gatewayRouteSchemaVersion {
		return fmt.Errorf("unsupported gateway route schema version %q", catalog.GetSchemaVersion())
	}
	if len(catalog.GetRoutes()) == 0 {
		return fmt.Errorf("gateway route catalog contains no routes")
	}
	seenMatches := make(map[string]string, len(catalog.GetRoutes()))
	previousSortKey := ""
	for _, route := range catalog.GetRoutes() {
		if route == nil || route.GetMethod() == "" || route.GetPath() == "" || route.GetProcedure() == "" ||
			route.GetOwner().GetModule() == "" || route.GetOwner().GetService() == "" || route.GetUpstreamEndpoint() == "" {
			return fmt.Errorf("gateway route identity is incomplete")
		}
		sortKey := gatewayRouteSortKey(route)
		if previousSortKey != "" && sortKey <= previousSortKey {
			return fmt.Errorf("gateway routes are not strictly sorted at %q", route.GetPath())
		}
		previousSortKey = sortKey
		matchKey := route.GetMethod() + " " + route.GetPath()
		if previous, exists := seenMatches[matchKey]; exists {
			return fmt.Errorf("duplicate gateway match %q for %q and %q", matchKey, previous, route.GetProcedure())
		}
		seenMatches[matchKey] = route.GetProcedure()
		if route.GetExposure() != policyv1.Exposure_EXPOSURE_PUBLIC && route.GetExposure() != policyv1.Exposure_EXPOSURE_AUTHENTICATED {
			return fmt.Errorf("gateway route %q has non-public-edge exposure %s", route.GetPath(), route.GetExposure())
		}
		if !endpointNamePattern.MatchString(route.GetUpstreamEndpoint()) {
			return fmt.Errorf("gateway route %q has invalid upstream endpoint", route.GetPath())
		}
		if route.GetMethod() != strings.ToUpper(route.GetMethod()) {
			return fmt.Errorf("gateway route %q method is not uppercase", route.GetPath())
		}
		switch route.GetProtocol() {
		case catalogv1.GatewayProtocol_GATEWAY_PROTOCOL_CONNECT:
			if route.GetMethod() != "POST" || route.GetMatch() != catalogv1.GatewayMatch_GATEWAY_MATCH_EXACT {
				return fmt.Errorf("Connect route %q is not an exact POST match", route.GetPath())
			}
		case catalogv1.GatewayProtocol_GATEWAY_PROTOCOL_REST:
			if route.GetMatch() == catalogv1.GatewayMatch_GATEWAY_MATCH_PATH_TEMPLATE {
				if err := validateSimplePathTemplate(route.GetPath()); err != nil {
					return err
				}
				if !pathParameterPattern.MatchString(route.GetPath()) {
					return fmt.Errorf("REST route %q declares a path template without a parameter", route.GetPath())
				}
			} else if route.GetMatch() != catalogv1.GatewayMatch_GATEWAY_MATCH_EXACT {
				return fmt.Errorf("REST route %q has invalid match kind", route.GetPath())
			} else if strings.ContainsAny(route.GetPath(), "{}") {
				return fmt.Errorf("REST route %q declares an exact match with template braces", route.GetPath())
			}
		default:
			return fmt.Errorf("gateway route %q has unspecified protocol", route.GetPath())
		}
		switch route.GetSource() {
		case catalogv1.GatewayRouteSource_GATEWAY_ROUTE_SOURCE_DESCRIPTOR:
			if route.GetRewritePath() != "" || route.GetRemoveAfter() != "" {
				return fmt.Errorf("canonical route %q declares compatibility metadata", route.GetPath())
			}
		case catalogv1.GatewayRouteSource_GATEWAY_ROUTE_SOURCE_COMPATIBILITY_ALIAS:
			if route.GetProtocol() != catalogv1.GatewayProtocol_GATEWAY_PROTOCOL_CONNECT ||
				route.GetRewritePath() == "" || route.GetRemoveAfter() == "" || route.GetRewritePath() != route.GetProcedure() {
				return fmt.Errorf("compatibility route %q has incomplete rewrite metadata", route.GetPath())
			}
			if _, err := time.Parse("2006-01-02", route.GetRemoveAfter()); err != nil {
				return fmt.Errorf("compatibility route %q has invalid removal date", route.GetPath())
			}
		default:
			return fmt.Errorf("gateway route %q has unspecified source", route.GetPath())
		}
	}
	return nil
}

// RenderGatewayRouteCatalogJSON emits stable proto-name JSON.
func RenderGatewayRouteCatalogJSON(catalog *catalogv1.GatewayRouteCatalog) ([]byte, error) {
	if err := ValidateGatewayRouteCatalog(catalog); err != nil {
		return nil, err
	}
	document, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(catalog)
	if err != nil {
		return nil, fmt.Errorf("marshal gateway route catalog: %w", err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, document, "", "  "); err != nil {
		return nil, fmt.Errorf("format gateway route catalog: %w", err)
	}
	formatted.WriteByte('\n')
	return formatted.Bytes(), nil
}

// RenderAuthSidecarConnectRoutes emits the runtime Connect route inventory.
// REST has a separate generated runtime artifact; custom paths stay explicit.
func RenderAuthSidecarConnectRoutes(catalog *catalogv1.GatewayRouteCatalog) ([]byte, error) {
	if err := ValidateGatewayRouteCatalog(catalog); err != nil {
		return nil, err
	}
	var source strings.Builder
	source.WriteString(`// Code generated by cmd/gateway-routes from generated/gateway-routes.json. DO NOT EDIT.

package main

// generatedCatalogConnectRoutes is the descriptor-authoritative public
// Connect inventory. Internal procedures never enter this slice.
func generatedCatalogConnectRoutes() []*RouteEntry {
	return []*RouteEntry{
`)
	for _, route := range catalog.GetRoutes() {
		if route.GetProtocol() != catalogv1.GatewayProtocol_GATEWAY_PROTOCOL_CONNECT {
			continue
		}
		serviceKey := route.GetOwner().GetService() + "_" + route.GetUpstreamEndpoint()
		fmt.Fprintf(&source, "\t\t{Service: %q, Method: %q, Path: %q, UpstreamPath: %q, Procedure: %q},\n",
			serviceKey,
			route.GetMethod(),
			route.GetPath(),
			route.GetRewritePath(),
			route.GetProcedure(),
		)
	}
	source.WriteString("\t}\n}\n")
	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		return nil, fmt.Errorf("format auth-sidecar gateway routes: %w", err)
	}
	return formatted, nil
}

func pathTemplateToRegex(path string) string {
	var result strings.Builder
	result.WriteByte('^')
	for index := 0; index < len(path); {
		location := pathParameterPattern.FindStringIndex(path[index:])
		if location == nil {
			result.WriteString(regexp.QuoteMeta(path[index:]))
			break
		}
		start := index + location[0]
		end := index + location[1]
		result.WriteString(regexp.QuoteMeta(path[index:start]))
		result.WriteString("[^/]+")
		index = end
	}
	result.WriteByte('$')
	return result.String()
}
