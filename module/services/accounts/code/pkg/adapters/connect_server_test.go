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

// The Connect listener — the endpoint the composition names as host.delivery —
// serves the delivery documents the composition interface has no procedure
// for: the module envelope, which the CLI's envelope Job POSTs to
// /platform/_delivery/envelope.
func TestConnectListenerServesTheDeliveryRoutes(t *testing.T) {
	mux := http.NewServeMux()
	mountDeliveryRoutes(mux, nil)
	for _, path := range []string{SolutionDeliveryEnvelopePath, SolutionDeliveryManifestPath} {
		request := httptest.NewRequest(http.MethodPost, "http://accounts.test"+path, nil)
		_, pattern := mux.Handler(request)
		if pattern != SolutionDeliveryPrefix {
			t.Fatalf("%s is served by %q on the Connect listener, want %q", path, pattern, SolutionDeliveryPrefix)
		}
	}
}
