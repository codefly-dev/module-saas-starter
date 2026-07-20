package adapters

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func TestNewConnectServerRequiresConfiguredBusinessService(t *testing.T) {
	previous := service
	service = nil
	t.Cleanup(func() { service = previous })

	_, err := NewConnectServer(&Configuration{}, &GrpcServer{})
	require.EqualError(t, err, "business service must be configured before the Connect server")

	configured := new(business.Service)
	WithService(configured)

	server, err := NewConnectServer(&Configuration{}, &GrpcServer{})
	require.NoError(t, err)
	require.Same(t, configured, server.service)
}

func TestCatalogConnectRegistrationCoversEveryProcedure(t *testing.T) {
	catalog, err := business.BuildServiceCatalog()
	require.NoError(t, err)

	mux := http.NewServeMux()
	server := &ConnectServer{grpc: &GrpcServer{}, service: new(business.Service)}
	registerCatalogConnectServices(mux, server)

	patterns := make(map[string]struct{})
	for _, method := range catalog.GetMethods() {
		request := httptest.NewRequest(http.MethodPost, "http://accounts.test"+method.GetProcedure(), nil)
		_, pattern := mux.Handler(request)
		require.NotEmpty(t, pattern, "%s is absent from the Connect mux", method.GetProcedure())
		require.Equal(t, "/"+method.GetFullService()+"/", pattern)
		patterns[pattern] = struct{}{}
	}
	require.Len(t, patterns, len(catalog.GetServices()))

	request := httptest.NewRequest(http.MethodPost, "http://accounts.test/saas.accounts.v1.UnknownService/Unknown", nil)
	_, pattern := mux.Handler(request)
	require.Empty(t, pattern)
}
