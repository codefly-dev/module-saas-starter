package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestWorkloadMintForwardsOnlyVerifiedPeerAndMapsRefusal(t *testing.T) {
	mint := &fakeAccountsWorkContextMint{}
	backend := grpc.NewServer()
	backend.RegisterService(&grpc.ServiceDesc{ServiceName: "saas.accounts.v1.ModuleCapabilitiesService", HandlerType: (*any)(nil), Methods: []grpc.MethodDesc{{MethodName: "MintModuleWorkContext", Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		return mint.handle(ctx, dec)
	}}}}, mint)
	listener := bufconn.Listen(1 << 20)
	go func() { _ = backend.Serve(listener) }()
	t.Cleanup(backend.Stop)
	conn, err := grpc.NewClient("passthrough:///accounts", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	gateway := NewGateway(&ExtAuthz{internalToken: "internal-perimeter", backendConn: conn}, NewRouteMatcher(nil, nil), nil, nil, newFakeSolutionRegistry(), nil)
	ca := newWorkloadTestCA(t)
	identity, _ := workloadTestIdentity(t, ca, gatewayTestSPIFFE, callerTestSPIFFE+"\n")
	config, err := identity.serverTLSConfig()
	require.NoError(t, err)
	httpListener := workloadMemoryServer(t, config, identity.handler(gateway))
	caller, _ := workloadTestIdentity(t, ca, callerTestSPIFFE, gatewayTestSPIFFE+"\n")
	client := workloadMemoryClient(t, caller, httpListener, gatewayTestSPIFFE)
	for _, code := range []codes.Code{codes.OK, codes.PermissionDenied} {
		mint.code = code
		request, err := http.NewRequest(http.MethodPost, "https://workload/modules/_work-context", strings.NewReader(`{"prefix":"example"}`))
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer projected-token")
		request.Header.Set(workloadSPIFFEHeader, "spiffe://acme.test/ns/acme-prod/sa/spoofed")
		// No shared internal credential: this listener authenticated the caller.
		response, err := client.Do(request)
		require.NoError(t, err)
		_ = response.Body.Close()
		if code == codes.OK {
			require.Equal(t, http.StatusOK, response.StatusCode)
		} else {
			require.Equal(t, http.StatusForbidden, response.StatusCode)
		}
		require.Equal(t, callerTestSPIFFE, mint.lastPeer)
		require.Equal(t, "projected-token", mint.lastToken)
		require.Equal(t, "internal-perimeter", mint.lastInternal)
	}
}
