package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

func TestGateway_DeletedRegistrationPathsReturn404(t *testing.T) {
	gateway := NewGateway(&ExtAuthz{}, NewRouteMatcher(nil, nil), nil, nil, nil, nil)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		for _, path := range []string{"/modules/_register", "/modules/_registration-token", "/solutions/_register", "/solutions/_frontend", "/solutions/_registration-token", "/solutions/_register/extra", "/solutions/_frontend/extra", "/solutions/_registration-token/extra", "/v1/example", "/v1/example/records", "/v1/example:query"} {
			t.Run(method+" "+path, func(t *testing.T) {
				req := httptest.NewRequest(method, path, strings.NewReader(`{"prefix":"example","upstream":"http://example.svc"}`))
				req.Header.Set("X-Codefly-Internal-Token", "example-internal-token")
				req.Header.Set("X-Codefly-Module-Registration", "example-retired-credential")
				response := httptest.NewRecorder()
				gateway.ServeHTTP(response, req)
				require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
			})
		}
	}
}

func TestDeclaredSolutionUpstreamReadPreservesURLAdmission(t *testing.T) {
	for _, upstream := range []string{
		"http://169.254.169.254", "http://metadata.google.internal", "https://api.example.com",
		"file://example.svc", "http://user:password@example.svc", "http://example.svc/path", "http://example.svc?secret=true",
	} {
		t.Run(upstream, func(t *testing.T) {
			registry := newFakeSolutionRegistry()
			registry.records["example"] = &accountsv1.SolutionRegistration{
				SolutionId: "example", Frontend: &accountsv1.SolutionFrontendBinding{},
				Backend:  &accountsv1.SolutionBackendBinding{Upstream: upstream},
				Declared: &accountsv1.SolutionDeclaredBinding{TargetId: "example-target"},
			}
			cache := newSolutionRegistryCache(registry)
			require.NoError(t, cache.refresh(context.Background()))
			routing, resolution := cache.resolveRouting(context.Background(), "example")
			require.Nil(t, routing)
			require.Equal(t, solutionNotActive, resolution)
		})
	}
}
