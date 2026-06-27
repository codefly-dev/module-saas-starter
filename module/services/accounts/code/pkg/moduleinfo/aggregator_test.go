package moduleinfo_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/moduleinfo"
)

// stubServer returns an httptest.Server that serves a fixed
// /v1/.well-known/service-info payload.
func stubServer(t *testing.T, caps moduleinfo.ServiceCapabilities) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/.well-known/service-info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": caps})
	})
	return httptest.NewServer(mux)
}

// TestAggregate_MergesAcrossServices — pins the per-service ↔
// module aggregation. Two stub services in the same module: the
// merged view contains every RPC / permission / scope / RLS table
// concatenated, plus the per-service breakdown is preserved.
func TestAggregate_MergesAcrossServices(t *testing.T) {
	svcA := stubServer(t, moduleinfo.ServiceCapabilities{
		Info: moduleinfo.ServiceInfo{Name: "accounts", Module: "saas-starter", Version: "0.1.0"},
		RPCs: []moduleinfo.RPCInfo{
			{Service: "UserService", Method: "GetSelf", HandlerAuthz: "auth"},
		},
		Permissions: []moduleinfo.PermissionInfo{
			{Resource: "users", Action: "read"},
		},
		Scopes: []moduleinfo.ScopeInfo{{Scope: "users:read"}},
	})
	defer svcA.Close()

	svcB := stubServer(t, moduleinfo.ServiceCapabilities{
		Info: moduleinfo.ServiceInfo{Name: "auth-sidecar", Module: "saas-starter", Version: "0.0.5"},
		RPCs: []moduleinfo.RPCInfo{
			{Service: "VerifyService", Method: "VerifyToken", HandlerAuthz: "public"},
		},
		Permissions: []moduleinfo.PermissionInfo{
			{Resource: "tokens", Action: "verify"},
		},
		Scopes: []moduleinfo.ScopeInfo{{Scope: "tokens:verify"}},
	})
	defer svcB.Close()

	view, errs := moduleinfo.Aggregate(context.Background(), []moduleinfo.Endpoint{
		{Name: "accounts", URL: svcA.URL + "/v1/.well-known/service-info"},
		{Name: "auth-sidecar", URL: svcB.URL + "/v1/.well-known/service-info"},
	}, 2*time.Second)
	require.Empty(t, errs)
	require.NotNil(t, view)
	require.Equal(t, "saas-starter", view.ModuleName)
	require.Len(t, view.Services, 2)
	require.Len(t, view.Aggregate.RPCs, 2)
	require.Len(t, view.Aggregate.Permissions, 2)
	require.Len(t, view.Aggregate.Scopes, 2)

	// Stable ordering: aggregator sorts services by name.
	require.Equal(t, "accounts", view.Services[0].Info.Name)
	require.Equal(t, "auth-sidecar", view.Services[1].Info.Name)
}

// TestAggregate_DetectsModuleMismatch — if two services claim
// different module memberships, the aggregator flags it via errs
// (without aborting — partial view still useful).
func TestAggregate_DetectsModuleMismatch(t *testing.T) {
	svcA := stubServer(t, moduleinfo.ServiceCapabilities{
		Info: moduleinfo.ServiceInfo{Name: "accounts", Module: "saas-starter"},
	})
	defer svcA.Close()
	svcB := stubServer(t, moduleinfo.ServiceCapabilities{
		Info: moduleinfo.ServiceInfo{Name: "stranger", Module: "different-module"},
	})
	defer svcB.Close()

	_, errs := moduleinfo.Aggregate(context.Background(), []moduleinfo.Endpoint{
		{Name: "accounts", URL: svcA.URL + "/v1/.well-known/service-info"},
		{Name: "stranger", URL: svcB.URL + "/v1/.well-known/service-info"},
	}, 2*time.Second)
	require.NotEmpty(t, errs)
	require.Contains(t, errs["stranger"].Error(), "different-module")
}

// TestAggregate_PartialFailure — one service down doesn't abort
// the whole aggregation; the failure shows up in errs and the
// healthy services still appear in the view.
func TestAggregate_PartialFailure(t *testing.T) {
	svcA := stubServer(t, moduleinfo.ServiceCapabilities{
		Info: moduleinfo.ServiceInfo{Name: "accounts", Module: "saas-starter"},
	})
	defer svcA.Close()

	view, errs := moduleinfo.Aggregate(context.Background(), []moduleinfo.Endpoint{
		{Name: "accounts", URL: svcA.URL + "/v1/.well-known/service-info"},
		{Name: "down", URL: "http://127.0.0.1:1/missing"},
	}, 500*time.Millisecond)

	require.Len(t, view.Services, 1, "healthy service still merged")
	require.Equal(t, "accounts", view.Services[0].Info.Name)
	require.Contains(t, errs, "down", "down service surfaced in errs")
}
