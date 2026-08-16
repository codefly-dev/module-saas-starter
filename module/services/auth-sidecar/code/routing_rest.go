package main

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// LoadRESTRoutesFromCatalog returns every public-edge descriptor REST binding.
// Authorization metadata is joined by canonical procedure and internal methods
// are absent from the generated source inventory.
func LoadRESTRoutesFromCatalog() ([]*RouteEntry, error) {
	source, err := restCatalogRoutes()
	if err != nil {
		return nil, err
	}
	authz, err := authorizationByProcedure()
	if err != nil {
		return nil, err
	}
	entries := make([]*RouteEntry, 0, len(source))
	for _, entry := range source {
		copy := *entry
		if err := applyGeneratedAuthorizationMetadata(&copy, authz); err != nil {
			return nil, fmt.Errorf("REST route %s %q: %w", copy.Method, copy.Path, err)
		}
		entries = append(entries, &copy)
	}
	log.Printf("routing: loaded %d REST routes from saas.rest.surface.v1", len(entries))
	return entries, nil
}

// LoadAllRESTRoutes combines the generated protobuf surface with explicit
// non-protobuf extensions still owned by the folder configuration.
func LoadAllRESTRoutes(ctx context.Context, extensionDir string) ([]*RouteEntry, error) {
	generated, err := LoadRESTRoutesFromCatalog()
	if err != nil {
		return nil, err
	}
	extensions, err := LoadRESTExtensionsFromDir(ctx, extensionDir)
	if err != nil {
		return nil, err
	}
	entries := append(generated, extensions...)
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		key := strings.ToUpper(entry.Method) + " " + entry.Path
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate REST route %q", key)
		}
		seen[key] = struct{}{}
	}
	return entries, nil
}
