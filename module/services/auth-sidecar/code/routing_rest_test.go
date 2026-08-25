package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratedRESTSurfaceAndExtensions(t *testing.T) {
	generated, err := LoadRESTRoutesFromCatalog()
	require.NoError(t, err)

	paths := make(map[string]*RouteEntry, len(generated))
	publicCount := 0
	for _, entry := range generated {
		paths[entry.Method+" "+entry.Path] = entry
		require.NotEmpty(t, entry.Procedure)
		require.NotEmpty(t, entry.PolicySHA256)
		if !entry.Protected {
			publicCount++
		}
	}
	require.Equal(t, 16, publicCount)
	require.NotNil(t, paths["GET /v1/.well-known/service-info"])
	require.NotNil(t, paths["GET /v1/public/plans"])
	require.False(t, paths["GET /v1/public/plans"].Protected)
	require.NotNil(t, paths["GET /v1/acquisition"])
	require.False(t, paths["GET /v1/acquisition"].Protected)
	require.NotNil(t, paths["POST /v1/waitlist"])
	require.False(t, paths["POST /v1/waitlist"].Protected)
	require.False(t, paths["POST /v1/invitations:inspect"].Protected)
	require.True(t, paths["POST /v1/invitations:inspect-id"].Protected)
	require.NotNil(t, paths["GET /v1/platform/jobs/operations"])
	require.True(t, paths["GET /v1/platform/jobs/operations"].Protected)
	require.NotNil(t, paths["GET /v1/platform/jobs/{job_id}"])
	require.True(t, paths["GET /v1/platform/jobs/{job_id}"].Protected)
	require.NotNil(t, paths["POST /v1/platform/jobs/{source_job_id}:replay"])
	require.True(t, paths["POST /v1/platform/jobs/{source_job_id}:replay"].Protected)
	require.NotNil(t, paths["POST /v1/work-contexts:exchange-audience"])
	require.True(t, paths["POST /v1/work-contexts:exchange-audience"].Protected)
	require.NotNil(t, paths["GET /v1/organizations/{organization_id}/usage"])
	require.True(t, paths["GET /v1/organizations/{organization_id}/usage"].Protected)
	require.NotNil(t, paths["GET /v1/organizations/{organization_id}/usage/{meter}/history"])
	require.True(t, paths["GET /v1/organizations/{organization_id}/usage/{meter}/history"].Protected)
	require.Nil(t, paths["POST /v1/permissions:check"])
	require.False(t, paths["POST /v1/users"].Protected)

	extensions, err := LoadRESTExtensionsFromDir(context.Background(), DefaultRoutingDir())
	require.NoError(t, err)
	require.Len(t, extensions, 7)
	wantExtensions := map[string]bool{
		"POST /v1/auth/magic-link":        false,
		"POST /v1/auth/magic-link/verify": false,
		"POST /v1/billing/webhook":        false,
		"POST /v1/email/webhook/resend":   false,
		"POST /v1/billing/checkout":       true,
		"POST /v1/billing/free-plan":      true,
		"POST /v1/billing/portal":         true,
	}
	for _, entry := range extensions {
		require.Empty(t, entry.Procedure)
		protected, exists := wantExtensions[entry.Method+" "+entry.Path]
		require.True(t, exists, "unexpected extension %s %s", entry.Method, entry.Path)
		require.Equal(t, protected, entry.Protected)
		delete(wantExtensions, entry.Method+" "+entry.Path)
	}
	require.Empty(t, wantExtensions)

	all, err := LoadAllRESTRoutes(context.Background(), DefaultRoutingDir())
	require.NoError(t, err)
	matcher := NewRouteMatcher(all, nil)
	require.NotNil(t, matcher.MatchREST(http.MethodPost, "/v1/platform/jobs/job-123:replay"))
	require.Nil(t, matcher.MatchREST(http.MethodPost, "/v1/platform/jobs/:replay"))
	require.Nil(t, matcher.MatchREST(http.MethodPost, "/v1/platform/jobs/job-123:other"))
	require.NotNil(t, matcher.MatchREST(http.MethodPost, "/v1/auth/magic-link"))
	require.Nil(t, matcher.MatchREST(http.MethodPost, "/v1/permissions:check"))
}

func TestRESTExtensionsRejectDescriptorRoutesAndDisabledEntries(t *testing.T) {
	tests := []struct {
		name    string
		route   string
		wantErr string
	}{
		{
			name: "descriptor collision",
			route: `path: /v1
routes:
  - path: /v1/users
    method: POST
    extension:
      exposed: true
      protected: false
`,
			wantErr: "collides with generated descriptor procedure",
		},
		{
			name: "disabled extension",
			route: `path: /v1
routes:
  - path: /v1/disabled
    method: POST
    extension:
      exposed: false
      protected: true
`,
			wantErr: "must set exposed=true",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "saas-starter", "api")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "test.rest.codefly.yaml"), []byte(test.route), 0o600))

			_, err := LoadRESTExtensionsFromDir(context.Background(), filepath.Dir(filepath.Dir(dir)))
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}
