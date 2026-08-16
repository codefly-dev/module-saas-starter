package adapters

import (
	"context"
	"net"
	"net/http"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // SA1019: h2c drives cleartext HTTP/2 for this multiplexing test; migration tracked with the generated listeners
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type internalAPIKeyFixture struct {
	gen.UnimplementedAPIKeyServiceServer
}

func (internalAPIKeyFixture) ValidateAPIKey(context.Context, *gen.ValidateAPIKeyRequest) (*gen.ValidateAPIKeyResponse, error) {
	return &gen.ValidateAPIKeyResponse{Valid: true}, nil
}

func TestPrivateRESTListenerMultiplexesInternalGRPC(t *testing.T) {
	previousToken := internalToken
	SetInternalToken("test-internal-token")
	t.Cleanup(func() { SetInternalToken(previousToken) })

	internal := grpc.NewServer(grpc.UnaryInterceptor(
		grpcAuthInterceptor(nil, rpcExposureInternal),
	))
	gen.RegisterAPIKeyServiceServer(internal, internalAPIKeyFixture{})

	fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &http.Server{Handler: h2c.NewHandler(multiplexInternalGRPC(internal, fallback), &http2.Server{})} //nolint:staticcheck // SA1019: cleartext HTTP/2 test server
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
		internal.Stop()
	})

	httpResp, err := http.Get("http://" + lis.Addr().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, httpResp.StatusCode)
	require.NoError(t, httpResp.Body.Close())

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := gen.NewAPIKeyServiceClient(conn)

	_, err = client.ValidateAPIKey(context.Background(), &gen.ValidateAPIKeyRequest{Key: "cfly_sk_test"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-codefly-internal-token", "test-internal-token")
	resp, err := client.ValidateAPIKey(ctx, &gen.ValidateAPIKeyRequest{Key: "cfly_sk_test"})
	require.NoError(t, err)
	require.True(t, resp.Valid)
}

func TestRESTAnnotatorCarriesOnlyGatewayVerifiablePublicOrigin(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://accounts/v1/auth/oauth/begin", nil)
	require.NoError(t, err)
	req.Header.Set("X-Codefly-Gateway-Token", "test-gateway-token")
	req.Header.Set(publicOriginHeader, "http://localhost:54321")

	md := CustomHeaderToGRPCMetadataAnnotator(context.Background(), req)
	require.Equal(t, []string{"test-gateway-token"}, md.Get("x-codefly-gateway-token"))
	require.Equal(t, []string{"http://localhost:54321"}, md.Get("x-codefly-public-origin"))
}
