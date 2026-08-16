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
	"log"
	"os"
	"strings"
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
		if route.Owner == nil || route.Owner.Service == "" || route.UpstreamEndpoint == "" {
			return nil, fmt.Errorf("routing: artifact %s Connect route %q is missing owner or upstream endpoint", path, route.Path)
		}
		entries = append(entries, &RouteEntry{
			Service:      route.Owner.Service + "_" + route.UpstreamEndpoint,
			Method:       route.Method,
			Path:         route.Path,
			UpstreamPath: route.RewritePath,
			Procedure:    route.Procedure,
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
		if route.Owner == nil || route.Owner.Service == "" {
			return nil, fmt.Errorf("routing: artifact %s REST route %q is missing owner", path, route.Path)
		}
		entries = append(entries, &RouteEntry{
			Service:   route.Owner.Service,
			Method:    route.Method,
			Path:      route.Path,
			Procedure: route.Procedure,
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
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("routing: cannot read route artifact %s: %w", path, err)
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
	seen := make(map[string]struct{}, len(doc.Routes))
	for index := range doc.Routes {
		route := &doc.Routes[index]
		if route.Method == "" || route.Path == "" || route.Procedure == "" {
			return nil, fmt.Errorf("routing: artifact %s has a route with an incomplete identity", path)
		}
		if route.Method != strings.ToUpper(route.Method) {
			return nil, fmt.Errorf("routing: artifact %s route %q method is not uppercase", path, route.Path)
		}
		if route.Exposure != exposurePublic && route.Exposure != exposureAuthenticated {
			return nil, fmt.Errorf("routing: artifact %s route %q has non-edge exposure %q", path, route.Path, route.Exposure)
		}
		matchKey := route.Method + " " + route.Path
		if _, exists := seen[matchKey]; exists {
			return nil, fmt.Errorf("routing: artifact %s has duplicate match %q", path, matchKey)
		}
		seen[matchKey] = struct{}{}
	}
	return &doc, nil
}
