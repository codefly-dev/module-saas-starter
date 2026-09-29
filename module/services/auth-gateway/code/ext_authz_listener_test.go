package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	apigen "auth-gateway/external/saas-starter/accounts"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// validAPIKeyClient admits every key, standing in for accounts' ValidateAPIKey.
type validAPIKeyClient struct {
	apigen.APIKeyServiceClient
}

func (validAPIKeyClient) ValidateAPIKey(context.Context, *apigen.ValidateAPIKeyRequest, ...grpc.CallOption) (*apigen.ValidateAPIKeyResponse, error) {
	return &apigen.ValidateAPIKeyResponse{Valid: true, UserId: "user-1", OrganizationId: "org-1"}, nil
}

// The gRPC ext_authz listener answers anyone who can reach its port. The
// gateway credential is what makes accounts believe forwarded identity headers,
// so a Check answer that carried it would hand any holder of an ordinary login
// or API key the means to tell accounts it is any user. The HTTP gateway stamps
// the credential itself, only on accounts routes; a Check answer never carries
// it, over the wire or in process.
func TestExtAuthzListener_NeverReturnsTheGatewayCredential(t *testing.T) {
	s, priv := newTestExtAuthz(t)
	s.apiKey = validAPIKeyClient{}

	server := grpc.NewServer()
	authv3.RegisterAuthorizationServer(server, s)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := authv3.NewAuthorizationClient(conn)

	for name, bearer := range map[string]string{
		"session token": signClaims(t, priv, validClaims(time.Now())),
		"api key":       "cfly_sk_example",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, err := client.Check(ctx, checkReq("/v1/users", map[string]string{
				"authorization": "Bearer " + bearer,
			}))
			require.NoError(t, err)
			ok := resp.GetOkResponse()
			require.NotNil(t, ok, "a valid credential is admitted")
			require.NotEmpty(t, headerMap(resp)["x-user-id"], "identity is still projected")

			for _, h := range ok.GetHeaders() {
				require.NotEqual(t, "x-codefly-gateway-token", strings.ToLower(h.GetHeader().GetKey()))
				require.NotContains(t, h.GetHeader().GetValue(), "test-gateway-token",
					"header %q carries the gateway credential", h.GetHeader().GetKey())
			}
			require.Contains(t, ok.GetHeadersToRemove(), "x-codefly-gateway-token",
				"a caller-supplied gateway credential is removed, never forwarded")
		})
	}
}
