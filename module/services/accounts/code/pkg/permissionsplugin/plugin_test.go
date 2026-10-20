package permissionsplugin

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestInstallationRESTReachesSelectedRPC(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { _ = listener.Close() })
	server := grpc.NewServer(grpc.UnaryInterceptor(func(_ context.Context, _ any, info *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		// A refusal from the selected RPC proves transcoding reached it. The
		// registration must not turn an authorization refusal into a route 404.
		return nil, status.Error(codes.PermissionDenied, info.FullMethod)
	}))
	gen.RegisterInstallationServiceServer(server, &gen.UnimplementedInstallationServiceServer{})
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()

	mux := runtime.NewServeMux()
	require.NoError(t, New().RegisterREST(ctx, mux, "passthrough:///installations", []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	}))
	for _, tc := range []struct{ method, path, rpc string }{
		{http.MethodGet, "/v1/installations:listAvailable?org_id=org", "ListAvailableSolutions"},
		{http.MethodGet, "/v1/installations?org_id=org", "ListOrganizationInstallations"},
		{http.MethodGet, "/v1/installations/installation", "GetInstallation"},
		{http.MethodPost, "/v1/installations", "InstallSolution"},
		{http.MethodPost, "/v1/installations/installation:uninstall", "UninstallSolution"},
		{http.MethodPost, "/v1/installations/installation:narrow", "NarrowInstallation"},
		{http.MethodPost, "/v1/installations/installation:retry", "RetryInstallation"},
		{http.MethodPost, "/v1/installations/installation:transferOwnership", "TransferInstallationOwnership"},
		{http.MethodPost, "/v1/installations/installation/teams", "GrantInstallationToTeam"},
		{http.MethodPost, "/v1/installations/installation/teams/team:revoke", "RevokeInstallationFromTeam"},
	} {
		t.Run(tc.rpc, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			request.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
			require.Contains(t, recorder.Body.String(), "/saas.accounts.v1.InstallationService/"+tc.rpc)
		})
	}
}
