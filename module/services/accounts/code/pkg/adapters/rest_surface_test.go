package adapters

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalogRESTAllowlistMatchesGeneratedSurface(t *testing.T) {
	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/v1/users"},
		{method: http.MethodGet, path: "/v1/users/4df7cb57-6db2-4332-9f92-c75dbb39f049"},
		{method: http.MethodDelete, path: "/v1/organizations/org-1/members/user-1"},
		{method: http.MethodGet, path: "/v1/delegations/grant-1:wait"},
	} {
		require.True(t, catalogRESTRouteAllowed(route.method, route.path), route.method+" "+route.path)
	}

	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/v1/permissions:check"},
		{method: http.MethodPost, path: "/v1/api-keys:validate"},
		{method: http.MethodPost, path: "/v1/identity:resolve"},
		{method: http.MethodGet, path: "/v1/users/"},
		{method: http.MethodPut, path: "/v1/users"},
		{method: http.MethodGet, path: "/unknown"},
	} {
		require.False(t, catalogRESTRouteAllowed(route.method, route.path), route.method+" "+route.path)
	}
}

func TestCatalogRESTHandlerFailsClosedBeforeGRPCGateway(t *testing.T) {
	nextCalls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	handler := catalogRESTHandler(next)

	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, httptest.NewRequest(http.MethodPost, "/v1/users", nil))
	require.Equal(t, http.StatusNoContent, allowed.Code)
	require.Equal(t, 1, nextCalls)

	internal := httptest.NewRecorder()
	handler.ServeHTTP(internal, httptest.NewRequest(http.MethodPost, "/v1/permissions:check", nil))
	require.Equal(t, http.StatusNotFound, internal.Code)
	require.Equal(t, 1, nextCalls)
}
