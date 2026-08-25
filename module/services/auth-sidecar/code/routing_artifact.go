package main

// Runtime-loadable route catalogs for the gateway RouteMatcher.
//
// The compiled-in generatedCatalog*Routes() slices force an auth-sidecar
// rebuild whenever a route changes. When GATEWAY_ROUTE_CATALOG_PATH or
// REST_SURFACE_CATALOG_PATH names a deployed artifact, the matching inventory
// is read and validated from that JSON at startup instead, so routes can ship
// with a deploy. The artifacts are the same saas.gateway.routes.v1 /
// saas.rest.surface.v1 documents the catalog generator already emits, so a
// runtime load produces byte-identical RouteEntry values to the compiled path.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	gatewayRouteCatalogEnv = "GATEWAY_ROUTE_CATALOG_PATH"
	restSurfaceCatalogEnv  = "REST_SURFACE_CATALOG_PATH"

	gatewayRouteSchemaVersion = "saas.gateway.routes.v1"
	restSurfaceSchemaVersion  = "saas.rest.surface.v1"

	gatewayProtocolConnect = "GATEWAY_PROTOCOL_CONNECT"
	gatewayProtocolREST    = "GATEWAY_PROTOCOL_REST"

	exposurePublic        = "EXPOSURE_PUBLIC"
	exposureAuthenticated = "EXPOSURE_AUTHENTICATED"

	matchExact        = "GATEWAY_MATCH_EXACT"
	matchPathTemplate = "GATEWAY_MATCH_PATH_TEMPLATE"

	routeSourceDescriptor         = "GATEWAY_ROUTE_SOURCE_DESCRIPTOR"
	routeSourceCompatibilityAlias = "GATEWAY_ROUTE_SOURCE_COMPATIBILITY_ALIAS"

	maxRuntimeCatalogBytes = 16 << 20
)

var (
	catalogIdentityPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	templateSegmentPattern = regexp.MustCompile(`^\{[A-Za-z_][A-Za-z0-9_]*\}(:[A-Za-z][A-Za-z0-9]*)?$`)
	procedurePattern       = regexp.MustCompile(`^/[A-Za-z_][A-Za-z0-9_.]*/[A-Za-z_][A-Za-z0-9_]*$`)
)

type routeCatalogDocument struct {
	SchemaVersion string             `json:"schema_version"`
	Owner         *routeCatalogOwner `json:"owner,omitempty"`
	Routes        []routeCatalogItem `json:"routes"`
}

type routeCatalogItem struct {
	Protocol         string             `json:"protocol"`
	Method           string             `json:"method"`
	Path             string             `json:"path"`
	Match            string             `json:"match"`
	Procedure        string             `json:"procedure"`
	Owner            *routeCatalogOwner `json:"owner"`
	UpstreamEndpoint string             `json:"upstream_endpoint"`
	Exposure         string             `json:"exposure"`
	Source           string             `json:"source"`
	RewritePath      string             `json:"rewrite_path"`
	RemoveAfter      string             `json:"remove_after"`
}

type routeCatalogOwner struct {
	Module  string `json:"module"`
	Service string `json:"service"`
}

type routeArtifactUpstream struct {
	Key      string
	Module   string
	Service  string
	Endpoint string
	API      string
}

// connectCatalogRoutes returns the Connect route inventory, preferring the
// deployed artifact when GATEWAY_ROUTE_CATALOG_PATH names one.
func connectCatalogRoutes() ([]*RouteEntry, error) {
	path := strings.TrimSpace(os.Getenv(gatewayRouteCatalogEnv))
	if path == "" {
		return generatedCatalogConnectRoutes(), nil
	}
	entries, err := loadConnectRoutesFromArtifact(path)
	if err != nil {
		return nil, err
	}
	log.Printf("routing: loaded Connect routes from artifact %s", path)
	return entries, nil
}

// restCatalogRoutes returns the descriptor REST inventory, preferring the
// deployed artifact when REST_SURFACE_CATALOG_PATH names one.
func restCatalogRoutes() ([]*RouteEntry, error) {
	path := strings.TrimSpace(os.Getenv(restSurfaceCatalogEnv))
	if path == "" {
		return generatedCatalogRESTRoutes(), nil
	}
	entries, err := loadRESTRoutesFromArtifact(path)
	if err != nil {
		return nil, err
	}
	log.Printf("routing: loaded REST routes from artifact %s", path)
	return entries, nil
}

func loadConnectRoutesFromArtifact(path string) ([]*RouteEntry, error) {
	doc, err := decodeRouteCatalog(path, gatewayRouteSchemaVersion)
	if err != nil {
		return nil, err
	}
	var entries []*RouteEntry
	for index := range doc.Routes {
		route := &doc.Routes[index]
		if route.Protocol != gatewayProtocolConnect {
			continue
		}
		upstream := newRouteArtifactUpstream(route.Owner, route.UpstreamEndpoint, "connect")
		entries = append(entries, &RouteEntry{
			Service:             upstream.Key,
			Method:              route.Method,
			Path:                route.Path,
			UpstreamPath:        route.RewritePath,
			Procedure:           route.Procedure,
			artifactUpstream:    &upstream,
			artifactExposure:    exposureFromRoute(route.Exposure),
			hasArtifactExposure: true,
		})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("routing: artifact %s contains no Connect routes", path)
	}
	return entries, nil
}

func loadRESTRoutesFromArtifact(path string) ([]*RouteEntry, error) {
	doc, err := decodeRouteCatalog(path, restSurfaceSchemaVersion)
	if err != nil {
		return nil, err
	}
	var entries []*RouteEntry
	for index := range doc.Routes {
		route := &doc.Routes[index]
		if route.Protocol != gatewayProtocolREST {
			return nil, fmt.Errorf("routing: artifact %s REST route %q has non-REST protocol %q", path, route.Path, route.Protocol)
		}
		upstream := newRouteArtifactUpstream(route.Owner, route.UpstreamEndpoint, "rest")
		entries = append(entries, &RouteEntry{
			Service:             upstream.Key,
			Method:              route.Method,
			Path:                route.Path,
			Procedure:           route.Procedure,
			artifactUpstream:    &upstream,
			artifactExposure:    exposureFromRoute(route.Exposure),
			hasArtifactExposure: true,
		})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("routing: artifact %s contains no REST routes", path)
	}
	return entries, nil
}

// decodeRouteCatalog reads and validates a route catalog artifact, rejecting
// schema drift, incomplete identities, non-edge exposure, and duplicate
// matches so an invalid artifact fails the gateway closed at startup.
func decodeRouteCatalog(path, wantSchemaVersion string) (*routeCatalogDocument, error) {
	raw, err := readRuntimeCatalog(path, "route")
	if err != nil {
		return nil, err
	}
	// Additive proto fields must not fail an older sidecar: a runtime artifact
	// is generated by whatever platform version shipped the plugin, so unknown
	// fields are tolerated per the protobuf forward-compatibility contract. The
	// required-field checks below still fail closed on a missing or misnamed
	// identity field.
	var doc routeCatalogDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("routing: cannot decode route artifact %s: %w", path, err)
	}
	if doc.SchemaVersion != wantSchemaVersion {
		return nil, fmt.Errorf("routing: artifact %s has schema version %q, want %q", path, doc.SchemaVersion, wantSchemaVersion)
	}
	if len(doc.Routes) == 0 {
		return nil, fmt.Errorf("routing: artifact %s contains no routes", path)
	}
	if doc.Owner != nil && (!validCatalogIdentity(doc.Owner.Module) || !validCatalogIdentity(doc.Owner.Service)) {
		return nil, fmt.Errorf("routing: artifact %s owner is incomplete", path)
	}
	seen := make(map[string]struct{}, len(doc.Routes))
	for index := range doc.Routes {
		route := &doc.Routes[index]
		if route.Owner == nil {
			return nil, fmt.Errorf("routing: artifact %s has a route with a missing owner", path)
		}
		if route.Method == "" || route.Path == "" || route.Procedure == "" ||
			!validCatalogIdentity(route.Owner.Module) || !validCatalogIdentity(route.Owner.Service) ||
			!validCatalogIdentity(route.UpstreamEndpoint) {
			return nil, fmt.Errorf("routing: artifact %s has a route with an incomplete identity", path)
		}
		if doc.Owner != nil && (route.Owner.Module != doc.Owner.Module || route.Owner.Service != doc.Owner.Service) {
			return nil, fmt.Errorf("routing: artifact %s route %q owner disagrees with catalog owner", path, route.Path)
		}
		if !procedurePattern.MatchString(route.Procedure) {
			return nil, fmt.Errorf("routing: artifact %s route %q procedure %q is not canonical", path, route.Path, route.Procedure)
		}
		if err := validateCatalogRouteShape(route); err != nil {
			return nil, fmt.Errorf("routing: artifact %s route %q: %w", path, route.Path, err)
		}
		if route.Exposure != exposurePublic && route.Exposure != exposureAuthenticated {
			return nil, fmt.Errorf("routing: artifact %s route %q has non-edge exposure %q", path, route.Path, route.Exposure)
		}
		matchKey := route.Method + " " + normalizedRoutePath(route.Path)
		if _, exists := seen[matchKey]; exists {
			return nil, fmt.Errorf("routing: artifact %s has duplicate match %q", path, matchKey)
		}
		seen[matchKey] = struct{}{}
	}
	return &doc, nil
}

func readRuntimeCatalog(path, kind string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("routing: cannot read %s artifact %s: %w", kind, path, err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxRuntimeCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("routing: cannot read %s artifact %s: %w", kind, path, err)
	}
	if len(raw) > maxRuntimeCatalogBytes {
		return nil, fmt.Errorf("routing: %s artifact %s exceeds %d bytes", kind, path, maxRuntimeCatalogBytes)
	}
	return raw, nil
}

func validateCatalogRouteShape(route *routeCatalogItem) error {
	if route.Method != strings.ToUpper(route.Method) {
		return fmt.Errorf("method is not uppercase")
	}
	switch route.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return fmt.Errorf("method %q is unsupported", route.Method)
	}
	if !strings.HasPrefix(route.Path, "/") || strings.ContainsAny(route.Path, "?#\r\n\t") || strings.Contains(route.Path, "//") {
		return fmt.Errorf("path is not an absolute clean HTTP path")
	}
	if route.Source != routeSourceDescriptor && route.Source != routeSourceCompatibilityAlias {
		return fmt.Errorf("source %q is unsupported", route.Source)
	}
	switch route.Protocol {
	case gatewayProtocolConnect:
		if route.Method != "POST" || route.Match != matchExact {
			return fmt.Errorf("Connect routes must use POST with exact matching")
		}
		if !procedurePattern.MatchString(route.Path) {
			return fmt.Errorf("Connect path is not canonical")
		}
	case gatewayProtocolREST:
		hasTemplate := false
		for _, segment := range strings.Split(route.Path, "/") {
			if strings.ContainsAny(segment, "{}") {
				if !templateSegmentPattern.MatchString(segment) {
					return fmt.Errorf("path contains a malformed template segment")
				}
				hasTemplate = true
			}
		}
		wantMatch := matchExact
		if hasTemplate {
			wantMatch = matchPathTemplate
		}
		if route.Match != wantMatch {
			return fmt.Errorf("match %q disagrees with path shape; want %q", route.Match, wantMatch)
		}
	default:
		return fmt.Errorf("protocol %q is unsupported", route.Protocol)
	}
	switch route.Source {
	case routeSourceDescriptor:
		if route.RewritePath != "" || route.RemoveAfter != "" {
			return fmt.Errorf("descriptor routes cannot carry compatibility metadata")
		}
	case routeSourceCompatibilityAlias:
		if route.Protocol != gatewayProtocolConnect {
			return fmt.Errorf("compatibility aliases are supported only for Connect routes")
		}
		if route.RewritePath != route.Procedure {
			return fmt.Errorf("compatibility alias rewrite must equal its canonical procedure")
		}
		if _, err := time.Parse("2006-01-02", route.RemoveAfter); err != nil {
			return fmt.Errorf("compatibility alias remove_after must be an ISO date")
		}
	}
	return nil
}

func normalizedRoutePath(value string) string {
	segments := strings.Split(value, "/")
	for index, segment := range segments {
		if matches := templateSegmentPattern.FindStringSubmatch(segment); matches != nil {
			segments[index] = "{}" + matches[1]
		}
	}
	return strings.Join(segments, "/")
}

func validCatalogIdentity(value string) bool {
	return len(value) <= 63 && catalogIdentityPattern.MatchString(value)
}

func exposureFromRoute(value string) edgeExposure {
	if value == exposurePublic {
		return edgeExposurePublic
	}
	return edgeExposureAuthenticated
}

func newRouteArtifactUpstream(owner *routeCatalogOwner, endpoint, api string) routeArtifactUpstream {
	key := owner.Module + "/" + owner.Service + "/" + endpoint + "/" + api
	if owner.Module == "saas-starter" && owner.Service == "accounts" && endpoint == api {
		key = owner.Service
		if endpoint != "rest" {
			key += "_" + endpoint
		}
	}
	return routeArtifactUpstream{
		Key: key, Module: owner.Module, Service: owner.Service, Endpoint: endpoint, API: api,
	}
}
