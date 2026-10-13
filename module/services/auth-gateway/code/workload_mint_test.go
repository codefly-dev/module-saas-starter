package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
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

// Exercise each real HTTP route over TLS and the internal gRPC transport. The
// backend here checks the attestation; the accounts adapter tests exercise the
// real TokenReview binding and signer separately.
func TestWorkloadEveryMintForwardsAttestation(t *testing.T) {
	for _, mint := range []struct {
		path, method, body string
		request            proto.Message
		response           proto.Message
	}{
		{
			path: moduleWorkContextPath, method: "MintModuleWorkContext", body: `{"prefix":"example"}`,
			request:  &accountsv1.ModuleMintWorkContextRequest{},
			response: &accountsv1.ModuleMintWorkContextResponse{Token: "signed", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))},
		},
		{
			path: moduleOperationContextPath, method: "MintModuleOperationContext", body: `{"prefix":"example","binding":"sync"}`,
			request:  &accountsv1.ModuleMintOperationContextRequest{},
			response: &accountsv1.ModuleMintOperationContextResponse{Token: "signed", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))},
		},
		{
			path: moduleSourceOperationContextPath, method: "MintSourceOperationContext", body: `{"prefix":"example","source_id":"` + testSourceID + `"}`,
			request:  &accountsv1.ModuleMintSourceOperationContextRequest{},
			response: &accountsv1.ModuleMintSourceOperationContextResponse{Token: "signed", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))},
		},
	} {
		t.Run(mint.method, func(t *testing.T) {
			var mu sync.Mutex
			var peers []string
			calls := 0
			backend := grpc.NewServer()
			backend.RegisterService(&grpc.ServiceDesc{ServiceName: "saas.accounts.v1.ModuleCapabilitiesService", HandlerType: (*any)(nil), Methods: []grpc.MethodDesc{{MethodName: mint.method, Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				req := proto.Clone(mint.request)
				if err := dec(req); err != nil {
					return nil, err
				}
				md, _ := metadata.FromIncomingContext(ctx)
				if token := req.(interface{ GetServiceAccountToken() string }).GetServiceAccountToken(); token != "projected-token" {
					return nil, status.Error(codes.Unauthenticated, "projected token missing")
				}
				if tokens := md.Get("x-codefly-internal-token"); len(tokens) != 1 || tokens[0] != "internal-perimeter" {
					return nil, status.Error(codes.Unauthenticated, "internal credential missing")
				}
				mu.Lock()
				peers = append([]string(nil), md.Get(workloadSPIFFEHeader)...)
				calls++
				mu.Unlock()
				if len(md.Get(workloadSPIFFEHeader)) != 0 && md.Get(workloadSPIFFEHeader)[0] != callerTestSPIFFE {
					return nil, status.Error(codes.PermissionDenied, "workload mismatch")
				}
				return proto.Clone(mint.response), nil
			}}}}, nil)
			listener := bufconn.Listen(1 << 20)
			go func() { _ = backend.Serve(listener) }()
			t.Cleanup(backend.Stop)
			conn, err := grpc.NewClient("passthrough:///accounts", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			gateway := NewGateway(&ExtAuthz{internalToken: "internal-perimeter", backendConn: conn}, NewRouteMatcher(nil, nil), nil, nil, newFakeSolutionRegistry(), nil)
			ca := newWorkloadTestCA(t)
			const otherPeer = "spiffe://acme.test/ns/acme-prod/sa/other"
			identity, _ := workloadTestIdentity(t, ca, gatewayTestSPIFFE, callerTestSPIFFE+"\n"+otherPeer+"\n")
			config, err := identity.serverTLSConfig()
			require.NoError(t, err)
			handler := identity.handler(gateway)
			httpListener := workloadMemoryServer(t, config, handler)
			request := func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "https://workload"+mint.path, strings.NewReader(mint.body))
				req.RequestURI = ""
				req.Header.Set("Authorization", "Bearer projected-token")
				req.Header.Set("X-Codefly-Internal-Token", "internal-perimeter")
				req.Header.Set(workloadSPIFFEHeader, callerTestSPIFFE)
				req.Header.Set("Grpc-Metadata-"+workloadSPIFFEHeader, callerTestSPIFFE)
				return req
			}
			for _, peer := range []string{callerTestSPIFFE, otherPeer} {
				caller, _ := workloadTestIdentity(t, ca, peer, gatewayTestSPIFFE+"\n")
				client := workloadMemoryClient(t, caller, httpListener, gatewayTestSPIFFE)
				response, err := client.Do(request())
				require.NoError(t, err)
				_ = response.Body.Close()
				want := http.StatusOK
				if peer != callerTestSPIFFE {
					want = http.StatusForbidden
				}
				require.Equal(t, want, response.StatusCode)
				mu.Lock()
				got := append([]string(nil), peers...)
				mu.Unlock()
				require.Equal(t, []string{peer}, got)
			}
			// Neither direct nor grpc-gateway-style HTTP headers attest a plain call.
			// Even outgoing metadata already on the context must be replaced.
			plain := request()
			plain.TLS = nil
			plain = plain.WithContext(metadata.NewOutgoingContext(plain.Context(), metadata.Pairs(workloadSPIFFEHeader, callerTestSPIFFE)))
			rec := httptest.NewRecorder()
			gateway.ServeHTTP(rec, plain)
			require.Equal(t, http.StatusOK, rec.Code)
			mu.Lock()
			got := append([]string(nil), peers...)
			before := calls
			mu.Unlock()
			require.Empty(t, got)
			// The workload handler cannot fall back to the plain mint when TLS peer
			// verification is absent, even with both bearer and internal credentials.
			rec = httptest.NewRecorder()
			handler.ServeHTTP(rec, plain)
			require.Equal(t, http.StatusForbidden, rec.Code)
			mu.Lock()
			after := calls
			mu.Unlock()
			require.Equal(t, before, after)
		})
	}
}
