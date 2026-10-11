package adapters

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Exercise the actual server dispatch without opening a network listener.
func TestDeletedRegistrationRPCsAreUnimplemented(t *testing.T) {
	server, err := NewGrpServer(&Configuration{})
	require.NoError(t, err)
	for name, transport := range map[string]*grpc.Server{
		"tenant": server.gRPC, "internal": server.internalGRPC, "authority": server.authorityGRPC,
	} {
		t.Cleanup(transport.Stop)
		for _, method := range []string{"PutSolutionRegistration", "DeleteSolutionRegistration"} {
			t.Run(name+"/"+method, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, "/saas.accounts.v1.SolutionRegistryService/"+method, bytes.NewReader([]byte{0, 0, 0, 0, 0}))
				req.Proto, req.ProtoMajor, req.ProtoMinor = "HTTP/2.0", 2, 0
				req.Header.Set("Content-Type", "application/grpc")
				req.Header.Set("TE", "trailers")
				response := httptest.NewRecorder()
				transport.ServeHTTP(response, req)
				result := response.Result()
				defer result.Body.Close()
				code := result.Trailer.Get("Grpc-Status")
				if code == "" {
					code = result.Header.Get("Grpc-Status")
				}
				require.Equal(t, "12", code, "deleted RPC must be Unimplemented before authentication")
			})
		}
		require.Contains(t, transport.GetServiceInfo(), "saas.accounts.v1.SolutionRegistryService", "the read service must remain registered")
	}
}

func TestRegistrationDescriptorHasNoRuntimeWritersOrLease(t *testing.T) {
	registry := gen.File_saas_accounts_v1_solution_registry_proto
	for _, name := range []string{"SolutionFrontendBinding", "SolutionBackendBinding"} {
		descriptor := registry.Messages().ByName(protoreflect.Name(name))
		require.NotNil(t, descriptor)
		require.Nil(t, descriptor.Fields().ByName("lease_expires_at"))
	}
	for _, name := range []string{"PutSolutionRegistrationRequest", "DeleteSolutionRegistrationRequest"} {
		require.Nil(t, registry.Messages().ByName(protoreflect.Name(name)))
	}
	require.Nil(t, registry.Enums().ByName("SolutionRegistrationStatus").Values().ByName("SOLUTION_REGISTRATION_STATUS_EXPIRED"))
}
