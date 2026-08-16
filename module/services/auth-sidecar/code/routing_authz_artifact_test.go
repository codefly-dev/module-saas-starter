package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const authzMethodsArtifact = "../../accounts/generated/authz-methods.json"

// The runtime artifact must project the same policy the compiled map holds, so
// shipping authorization with a deploy is faithful to a rebuild.
func TestAuthzArtifactMatchesCompiledCatalog(t *testing.T) {
	fromArtifact, err := loadAuthorizationMetadataFromArtifact(authzMethodsArtifact)
	require.NoError(t, err)
	require.Equal(t, generatedAuthorizationByProcedure, fromArtifact)
}

func TestConnectRoutesUseAuthzArtifact(t *testing.T) {
	compiled, err := LoadConnectRoutesFromCatalog()
	require.NoError(t, err)

	t.Setenv(authzMetadataCatalogEnv, authzMethodsArtifact)
	fromArtifact, err := LoadConnectRoutesFromCatalog()
	require.NoError(t, err)

	require.Equal(t, compiled, fromArtifact)
}

// A route whose backend lives in another module loads once its procedure's
// policy arrives in the authorization artifact — no compiled-in metadata.
func TestRouteAuthorizedByArtifactProcedureWithoutRebuild(t *testing.T) {
	const procedure = "/saas.plugin.v1.WidgetService/ListWidgets"
	_, known := generatedAuthorizationByProcedure[procedure]
	require.False(t, known, "procedure must be absent from the compiled catalog for this test")

	authz := authzMethodsDocument{
		SchemaVersion: authzMethodsSchemaVersion,
		Owner:         &routeCatalogOwner{Module: "widgets", Service: "widgets"},
		Methods: []authzMethodItem{{
			Procedure:                   procedure,
			PolicySHA256:                "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			RateLimitBackendFailureMode: "RATE_LIMIT_BACKEND_FAILURE_MODE_FAIL_OPEN",
			Policy:                      &authzEdgePolicy{Exposure: exposureAuthenticated, RateLimit: "RATE_LIMIT_CLASS_STANDARD_READ"},
		}},
	}
	routes := routeCatalogDocument{
		SchemaVersion: restSurfaceSchemaVersion,
		Owner:         &routeCatalogOwner{Module: "widgets", Service: "widgets"},
		Routes: []routeCatalogItem{{
			Protocol:         gatewayProtocolREST,
			Method:           "GET",
			Path:             "/v1/widgets",
			Match:            "GATEWAY_MATCH_EXACT",
			Procedure:        procedure,
			Owner:            &routeCatalogOwner{Module: "widgets", Service: "widgets"},
			UpstreamEndpoint: "rest",
			Exposure:         exposureAuthenticated,
			Source:           "GATEWAY_ROUTE_SOURCE_DESCRIPTOR",
		}},
	}

	t.Setenv(authzMetadataCatalogEnv, writeAuthzArtifact(t, authz))
	t.Setenv(restSurfaceCatalogEnv, writeArtifact(t, routes))

	entries, err := LoadRESTRoutesFromCatalog()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, procedure, entries[0].Procedure)
	require.True(t, entries[0].Protected)
	require.Equal(t, edgeRateLimitClassStandardRead, entries[0].RateLimitClass)
	require.NotEmpty(t, entries[0].PolicySHA256)

	matcher := NewRouteMatcher(entries, nil)
	require.NotNil(t, matcher.MatchREST(http.MethodGet, "/v1/widgets"))
}

// A route with no matching policy still fails closed under the artifact path.
func TestRouteWithoutArtifactPolicyFailsClosed(t *testing.T) {
	authz := authzMethodsDocument{
		SchemaVersion: authzMethodsSchemaVersion,
		Owner:         &routeCatalogOwner{Module: "widgets", Service: "widgets"},
		Methods: []authzMethodItem{{
			Procedure:    "/saas.plugin.v1.WidgetService/ListWidgets",
			PolicySHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Policy:       &authzEdgePolicy{Exposure: exposureAuthenticated, RateLimit: "RATE_LIMIT_CLASS_STANDARD_READ"},
		}},
	}
	routes := routeCatalogDocument{
		SchemaVersion: restSurfaceSchemaVersion,
		Owner:         &routeCatalogOwner{Module: "widgets", Service: "widgets"},
		Routes: []routeCatalogItem{{
			Protocol:         gatewayProtocolREST,
			Method:           "GET",
			Path:             "/v1/orphan",
			Match:            "GATEWAY_MATCH_EXACT",
			Procedure:        "/saas.plugin.v1.WidgetService/Orphan",
			Owner:            &routeCatalogOwner{Module: "widgets", Service: "widgets"},
			UpstreamEndpoint: "rest",
			Exposure:         exposureAuthenticated,
			Source:           "GATEWAY_ROUTE_SOURCE_DESCRIPTOR",
		}},
	}

	t.Setenv(authzMetadataCatalogEnv, writeAuthzArtifact(t, authz))
	t.Setenv(restSurfaceCatalogEnv, writeArtifact(t, routes))

	_, err := LoadRESTRoutesFromCatalog()
	require.ErrorContains(t, err, "metadata is missing")
}

func TestAuthzArtifactFailsClosed(t *testing.T) {
	validMethod := func() authzMethodItem {
		return authzMethodItem{
			Procedure:                   "/saas.plugin.v1.WidgetService/ListWidgets",
			PolicySHA256:                "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			RateLimitBackendFailureMode: "RATE_LIMIT_BACKEND_FAILURE_MODE_FAIL_OPEN",
			Policy:                      &authzEdgePolicy{Exposure: exposureAuthenticated, RateLimit: "RATE_LIMIT_CLASS_STANDARD_READ"},
		}
	}

	tests := []struct {
		name    string
		mutate  func(doc *authzMethodsDocument)
		wantErr string
	}{
		{
			name:    "wrong schema version",
			mutate:  func(doc *authzMethodsDocument) { doc.SchemaVersion = "saas.authz.methods.v2" },
			wantErr: "schema version",
		},
		{
			name:    "incomplete owner",
			mutate:  func(doc *authzMethodsDocument) { doc.Owner = nil },
			wantErr: "owner is incomplete",
		},
		{
			name:    "no methods",
			mutate:  func(doc *authzMethodsDocument) { doc.Methods = nil },
			wantErr: "contains no methods",
		},
		{
			name:    "missing policy",
			mutate:  func(doc *authzMethodsDocument) { doc.Methods[0].Policy = nil },
			wantErr: "incomplete identity",
		},
		{
			name:    "duplicate procedure",
			mutate:  func(doc *authzMethodsDocument) { doc.Methods = append(doc.Methods, doc.Methods[0]) },
			wantErr: "duplicate procedure",
		},
		{
			name:    "invalid fingerprint",
			mutate:  func(doc *authzMethodsDocument) { doc.Methods[0].PolicySHA256 = "nope" },
			wantErr: "invalid policy fingerprint",
		},
		{
			name:    "unsupported exposure",
			mutate:  func(doc *authzMethodsDocument) { doc.Methods[0].Policy.Exposure = "EXPOSURE_UNSPECIFIED" },
			wantErr: "unsupported exposure",
		},
		{
			name:    "unsupported rate-limit class",
			mutate:  func(doc *authzMethodsDocument) { doc.Methods[0].Policy.RateLimit = "RATE_LIMIT_CLASS_UNSPECIFIED" },
			wantErr: "unsupported rate-limit class",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := authzMethodsDocument{
				SchemaVersion: authzMethodsSchemaVersion,
				Owner:         &routeCatalogOwner{Module: "widgets", Service: "widgets"},
				Methods:       []authzMethodItem{validMethod()},
			}
			test.mutate(&doc)
			_, err := loadAuthorizationMetadataFromArtifact(writeAuthzArtifact(t, doc))
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestAuthzArtifactMissingFile(t *testing.T) {
	_, err := loadAuthorizationMetadataFromArtifact(filepath.Join(t.TempDir(), "absent.json"))
	require.ErrorContains(t, err, "cannot read authorization artifact")
}

func writeAuthzArtifact(t *testing.T, doc authzMethodsDocument) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "authz-methods.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}
