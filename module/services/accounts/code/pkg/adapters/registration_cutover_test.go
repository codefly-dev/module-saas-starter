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
	frontend := registry.Messages().ByName("SolutionFrontendBinding")
	require.NotNil(t, frontend)
	require.Nil(t, frontend.Fields().ByName("lease_expires_at"))
	// Nothing a running process reports is on the wire (#1118): the reported
	// backend half is deleted as a message, the record carries no `backend`,
	// and the version two reported halves were compared on is gone with them.
	require.Nil(t, frontend.Fields().ByName("contract_version"))
	require.Nil(t, registry.Messages().ByName("SolutionBackendBinding"))
	require.Nil(t, registry.Messages().ByName("SolutionRegistration").Fields().ByName("backend"))
	for _, name := range []string{"PutSolutionRegistrationRequest", "DeleteSolutionRegistrationRequest"} {
		require.Nil(t, registry.Messages().ByName(protoreflect.Name(name)))
	}
	status := registry.Enums().ByName("SolutionRegistrationStatus").Values()
	require.Nil(t, status.ByName("SOLUTION_REGISTRATION_STATUS_EXPIRED"))
	require.Nil(t, status.ByName("SOLUTION_REGISTRATION_STATUS_INCOMPATIBLE"))
	mint := gen.File_saas_accounts_v1_module_registration_proto.Messages().ByName("ModuleMintWorkContextRequest")
	require.NotNil(t, mint)
	require.Nil(t, mint.Fields().ByName("upstream"), "a mint reports no origin")
	require.Nil(t, mint.Fields().ByName("manifest"), "a mint reports no manifest")
}
