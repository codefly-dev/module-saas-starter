package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	gatewayRouteArtifact = "../../accounts/generated/gateway-routes.json"
	restSurfaceArtifact  = "../../accounts/generated/rest-surface.json"
)

// The runtime artifact path must produce the same inventory the compiled
// catalog does, so shipping routes with a deploy is faithful to a rebuild.
func TestConnectArtifactMatchesCompiledCatalog(t *testing.T) {
	compiled, err := LoadConnectRoutesFromCatalog()
	require.NoError(t, err)

	t.Setenv(gatewayRouteCatalogEnv, gatewayRouteArtifact)
	fromArtifact, err := LoadConnectRoutesFromCatalog()
	require.NoError(t, err)

	require.Equal(t, compiled, fromArtifact)
}

func TestRESTArtifactMatchesCompiledCatalog(t *testing.T) {
	compiled, err := LoadRESTRoutesFromCatalog()
	require.NoError(t, err)

	t.Setenv(restSurfaceCatalogEnv, restSurfaceArtifact)
	fromArtifact, err := LoadRESTRoutesFromCatalog()
	require.NoError(t, err)

	require.Equal(t, compiled, fromArtifact)
}

// A new REST binding for an already-authorized procedure loads from the
// artifact without any change to the compiled catalog.
func TestRESTArtifactAddsRouteWithoutRebuild(t *testing.T) {
	doc := routeCatalogDocument{
		SchemaVersion: restSurfaceSchemaVersion,
		Owner:         &routeCatalogOwner{Module: "saas-starter", Service: "accounts"},
		Routes: []routeCatalogItem{{
			Protocol:         gatewayProtocolREST,
			Method:           "GET",
			Path:             "/v2/public/plans",
			Match:            "GATEWAY_MATCH_EXACT",
			Procedure:        "/saas.accounts.v1.BillingService/ListPublicPlans",
			Owner:            &routeCatalogOwner{Module: "saas-starter", Service: "accounts"},
			UpstreamEndpoint: "rest",
			Exposure:         exposurePublic,
			Source:           "GATEWAY_ROUTE_SOURCE_DESCRIPTOR",
		}},
	}
	path := writeArtifact(t, doc)

	t.Setenv(restSurfaceCatalogEnv, path)
	entries, err := LoadRESTRoutesFromCatalog()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "/v2/public/plans", entries[0].Path)
	require.False(t, entries[0].Protected)
	require.NotEmpty(t, entries[0].PolicySHA256)

	matcher := NewRouteMatcher(entries, nil)
	require.NotNil(t, matcher.MatchREST(http.MethodGet, "/v2/public/plans"))
}

func TestRouteArtifactFailsClosed(t *testing.T) {
	valid := routeCatalogItem{
		Protocol:         gatewayProtocolREST,
		Method:           "GET",
		Path:             "/v1/public/plans",
		Procedure:        "/saas.accounts.v1.BillingService/ListPublicPlans",
		Owner:            &routeCatalogOwner{Module: "saas-starter", Service: "accounts"},
		UpstreamEndpoint: "rest",
		Exposure:         exposurePublic,
	}

	tests := []struct {
		name    string
		mutate  func(doc *routeCatalogDocument)
		wantErr string
	}{
		{
			name:    "wrong schema version",
			mutate:  func(doc *routeCatalogDocument) { doc.SchemaVersion = "saas.rest.surface.v2" },
			wantErr: "schema version",
		},
		{
			name:    "no routes",
			mutate:  func(doc *routeCatalogDocument) { doc.Routes = nil },
			wantErr: "contains no routes",
		},
		{
			name:    "incomplete identity",
			mutate:  func(doc *routeCatalogDocument) { doc.Routes[0].Procedure = "" },
			wantErr: "incomplete identity",
		},
		{
			name:    "non-uppercase method",
			mutate:  func(doc *routeCatalogDocument) { doc.Routes[0].Method = "get" },
			wantErr: "not uppercase",
		},
		{
			name:    "internal exposure",
			mutate:  func(doc *routeCatalogDocument) { doc.Routes[0].Exposure = "EXPOSURE_INTERNAL" },
			wantErr: "non-edge exposure",
		},
		{
			name: "duplicate match",
			mutate: func(doc *routeCatalogDocument) {
				doc.Routes = append(doc.Routes, doc.Routes[0])
			},
			wantErr: "duplicate match",
		},
		{
			name:    "missing owner",
			mutate:  func(doc *routeCatalogDocument) { doc.Routes[0].Owner = nil },
			wantErr: "missing owner",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := routeCatalogDocument{
				SchemaVersion: restSurfaceSchemaVersion,
				Owner:         &routeCatalogOwner{Module: "saas-starter", Service: "accounts"},
				Routes:        []routeCatalogItem{valid},
			}
			test.mutate(&doc)
			_, err := loadRESTRoutesFromArtifact(writeArtifact(t, doc))
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestRouteArtifactRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rest-surface.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schema_version":"saas.rest.surface.v1","routes":[],"unexpected":true}`), 0o600))
	_, err := loadRESTRoutesFromArtifact(path)
	require.ErrorContains(t, err, "cannot decode route artifact")
}

func TestRouteArtifactMissingFile(t *testing.T) {
	_, err := loadConnectRoutesFromArtifact(filepath.Join(t.TempDir(), "absent.json"))
	require.ErrorContains(t, err, "cannot read route artifact")
}

func writeArtifact(t *testing.T, doc routeCatalogDocument) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "route-catalog.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}
