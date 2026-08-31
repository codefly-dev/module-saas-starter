package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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

	require.Equal(t, compiled, routeBehaviorEntries(fromArtifact))
}

func TestRESTArtifactMatchesCompiledCatalog(t *testing.T) {
	compiled, err := LoadRESTRoutesFromCatalog()
	require.NoError(t, err)

	t.Setenv(restSurfaceCatalogEnv, restSurfaceArtifact)
	fromArtifact, err := LoadRESTRoutesFromCatalog()
	require.NoError(t, err)

	require.Equal(t, compiled, routeBehaviorEntries(fromArtifact))
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
		Match:            matchExact,
		Procedure:        "/saas.accounts.v1.BillingService/ListPublicPlans",
		Owner:            &routeCatalogOwner{Module: "saas-starter", Service: "accounts"},
		UpstreamEndpoint: "rest",
		Exposure:         exposurePublic,
		Source:           routeSourceDescriptor,
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
			name:    "unknown protocol",
			mutate:  func(doc *routeCatalogDocument) { doc.Routes[0].Protocol = "GATEWAY_PROTOCOL_SMUGGLED" },
			wantErr: "protocol",
		},
		{
			name:    "path match drift",
			mutate:  func(doc *routeCatalogDocument) { doc.Routes[0].Match = matchPathTemplate },
			wantErr: "disagrees with path shape",
		},
		{
			name:    "unsupported source",
			mutate:  func(doc *routeCatalogDocument) { doc.Routes[0].Source = "GATEWAY_ROUTE_SOURCE_UNTRUSTED" },
			wantErr: "source",
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
		{
			name: "route owner disagrees with catalog",
			mutate: func(doc *routeCatalogDocument) {
				doc.Routes[0].Owner = &routeCatalogOwner{Module: "other", Service: "accounts"}
			},
			wantErr: "owner disagrees with catalog owner",
		},
		{
			name:    "descriptor compatibility metadata",
			mutate:  func(doc *routeCatalogDocument) { doc.Routes[0].RewritePath = doc.Routes[0].Procedure },
			wantErr: "cannot carry compatibility metadata",
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

func TestConnectCompatibilityAliasFailsClosed(t *testing.T) {
	valid := routeCatalogItem{
		Protocol: gatewayProtocolConnect, Method: "POST", Path: "/customers.WidgetService/ListWidgets", Match: matchExact,
		Procedure: "/saas.plugin.v1.WidgetService/ListWidgets", Owner: &routeCatalogOwner{Module: "widgets", Service: "widgets"},
		UpstreamEndpoint: "connect", Exposure: exposureAuthenticated, Source: routeSourceCompatibilityAlias,
		RewritePath: "/saas.plugin.v1.WidgetService/ListWidgets", RemoveAfter: "2026-10-11",
	}

	tests := []struct {
		name    string
		mutate  func(*routeCatalogItem)
		wantErr string
	}{
		{name: "missing rewrite", mutate: func(route *routeCatalogItem) { route.RewritePath = "" }, wantErr: "rewrite"},
		{name: "wrong rewrite", mutate: func(route *routeCatalogItem) { route.RewritePath = "/saas.plugin.v1.WidgetService/GetWidget" }, wantErr: "rewrite"},
		{name: "missing removal date", mutate: func(route *routeCatalogItem) { route.RemoveAfter = "" }, wantErr: "ISO date"},
		{name: "invalid removal date", mutate: func(route *routeCatalogItem) { route.RemoveAfter = "2026-02-30" }, wantErr: "ISO date"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			route := valid
			test.mutate(&route)
			doc := routeCatalogDocument{SchemaVersion: gatewayRouteSchemaVersion, Routes: []routeCatalogItem{route}}
			_, err := loadConnectRoutesFromArtifact(writeArtifact(t, doc))
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestRouteArtifactRejectsAmbiguousTemplateMatches(t *testing.T) {
	doc := routeCatalogDocument{
		SchemaVersion: restSurfaceSchemaVersion,
		Routes: []routeCatalogItem{
			{
				Protocol: gatewayProtocolREST, Method: "GET", Path: "/v1/widgets/{id}", Match: matchPathTemplate,
				Procedure: "/saas.plugin.v1.WidgetService/GetWidget", Owner: &routeCatalogOwner{Module: "widgets", Service: "widgets"},
				UpstreamEndpoint: "rest", Exposure: exposureAuthenticated, Source: routeSourceDescriptor,
			},
			{
				Protocol: gatewayProtocolREST, Method: "GET", Path: "/v1/widgets/{name}", Match: matchPathTemplate,
				Procedure: "/saas.plugin.v1.WidgetService/FindWidget", Owner: &routeCatalogOwner{Module: "widgets", Service: "widgets"},
				UpstreamEndpoint: "rest", Exposure: exposureAuthenticated, Source: routeSourceDescriptor,
			},
		},
	}
	_, err := loadRESTRoutesFromArtifact(writeArtifact(t, doc))
	require.ErrorContains(t, err, "duplicate match")
}

func TestRuntimeArtifactAddsItsTypedCodeflyUpstream(t *testing.T) {
	doc := routeCatalogDocument{
		SchemaVersion: restSurfaceSchemaVersion,
		Routes: []routeCatalogItem{{
			Protocol: gatewayProtocolREST, Method: "GET", Path: "/v1/widgets", Match: matchExact,
			Procedure: "/saas.plugin.v1.WidgetService/ListWidgets", Owner: &routeCatalogOwner{Module: "widgets", Service: "backend"},
			UpstreamEndpoint: "public-rest", Exposure: exposureAuthenticated, Source: routeSourceDescriptor,
		}},
	}
	entries, err := loadRESTRoutesFromArtifact(writeArtifact(t, doc))
	require.NoError(t, err)
	require.Len(t, entries, 1)

	matcher := NewRouteMatcher(entries, nil)
	upstreams := map[string]*url.URL{"accounts": mustParseURL(t, "http://accounts:8080")}
	var resolved routeArtifactUpstream
	require.NoError(t, addArtifactUpstreams(t.Context(), matcher, upstreams, func(_ context.Context, requirement routeArtifactUpstream) (*url.URL, error) {
		resolved = requirement
		return mustParseURL(t, "http://widgets:9090"), nil
	}))

	require.Equal(t, routeArtifactUpstream{
		Key: "widgets/backend/public-rest/rest", Module: "widgets", Service: "backend", Endpoint: "public-rest", API: "rest",
	}, resolved)
	require.Equal(t, "http://widgets:9090", upstreams[resolved.Key].String())
	require.Equal(t, resolved.Key, entries[0].Service)
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	require.NoError(t, err)
	return parsed
}

func routeBehaviorEntries(entries []*RouteEntry) []*RouteEntry {
	result := make([]*RouteEntry, 0, len(entries))
	for _, entry := range entries {
		copy := *entry
		copy.artifactUpstream = nil
		copy.artifactExposure = 0
		copy.hasArtifactExposure = false
		result = append(result, &copy)
	}
	return result
}

// An additive proto field must not fail an older sidecar: unknown fields are
// tolerated so a newer platform's artifact still loads.
func TestRouteArtifactToleratesUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rest-surface.json")
	route := `{"protocol":"GATEWAY_PROTOCOL_REST","method":"GET","path":"/v1/public/plans","match":"GATEWAY_MATCH_EXACT",` +
		`"procedure":"/saas.accounts.v1.BillingService/ListPublicPlans","owner":{"module":"saas-starter","service":"accounts"},` +
		`"upstream_endpoint":"rest","exposure":"EXPOSURE_PUBLIC","source":"GATEWAY_ROUTE_SOURCE_DESCRIPTOR","future_field":42}`
	body := `{"schema_version":"saas.rest.surface.v1","owner":{"module":"saas-starter","service":"accounts"},"routes":[` + route + `]}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	entries, err := loadRESTRoutesFromArtifact(path)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "/v1/public/plans", entries[0].Path)
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
