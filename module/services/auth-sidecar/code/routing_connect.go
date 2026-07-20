package main

// Connect RPC route discovery is compiled from the normalized accounts
// service catalog by cmd/gateway-routes. This runtime adapter returns defensive
// copies so matchers and tests cannot mutate the generated source inventory.

import (
	"fmt"
	"log"
)

// LoadConnectRoutesFromCatalog returns every public-edge Connect procedure and
// dated compatibility alias. Internal descriptor methods are absent by
// construction; public/protected classification comes from MethodPolicy.
func LoadConnectRoutesFromCatalog() ([]*RouteEntry, error) {
	generated := generatedCatalogConnectRoutes()
	entries := make([]*RouteEntry, 0, len(generated))
	for _, entry := range generated {
		copy := *entry
		if err := applyGeneratedAuthorizationMetadata(&copy); err != nil {
			return nil, fmt.Errorf("Connect route %q: %w", copy.Path, err)
		}
		entries = append(entries, &copy)
	}
	log.Printf("routing: loaded %d generated Connect routes from saas.gateway.routes.v1", len(entries))
	return entries, nil
}
