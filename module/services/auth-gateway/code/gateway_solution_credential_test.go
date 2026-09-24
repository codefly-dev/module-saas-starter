package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeSolutionMint serves accounts' solution-credential exchange with a fixed
// outcome, so the gateway's mapping of each outcome can be asserted alone.
type fakeSolutionMint struct {
	code         codes.Code
	requestCount int
}

func newSolutionExchangeHarness(t *testing.T) (*Gateway, *fakeSolutionMint) {
	t.Helper()
	gw, _, _, _ := newGatewayHarness(t)
	mint := &fakeSolutionMint{}

	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "saas.accounts.v1.ModuleCapabilitiesService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "MintSolutionRegistration",
			Handler: func(_ any, _ context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				req := &accountsv1.SolutionMintRegistrationRequest{}
				if err := dec(req); err != nil {
					return nil, err
				}
				mint.requestCount++
				if mint.code != codes.OK {
					return nil, status.Error(mint.code, "not issued")
				}
				return &accountsv1.SolutionMintRegistrationResponse{
					Token:     "minted",
					ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)),
				}, nil
			},
		}},
		Metadata: "saas/accounts/v1/module_registration.proto",
	}, mint)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	gw.authz.backendConn = conn
	return gw, mint
}

func solutionTokenRequest(id string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/solutions/"+solutionRegistrationTokenSegment,
		strings.NewReader(fmt.Sprintf(`{"id":%q}`, id)))
	req.Header.Set(solutionSecretHeader, "example-secret")
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	return req
}

// A registrant acts differently on each outcome of the exchange: a refusal means
// its provisioning is wrong, an unreachable issuer means try again, and an issuer
// that answered with an error means the host is broken. Answering the second
// like either of the others sends an operator after a secret that was correct —
// which is what a restarting accounts looked like before.
func TestGateway_SolutionRegistrationToken_KeepsOutcomesApart(t *testing.T) {
	for name, test := range map[string]struct {
		code       codes.Code
		want       int
		retryAfter bool
	}{
		"issued":             {codes.OK, http.StatusOK, false},
		"refused":            {codes.PermissionDenied, http.StatusUnauthorized, false},
		"issuer unreachable": {codes.Unavailable, http.StatusServiceUnavailable, true},
		"issuer timed out":   {codes.DeadlineExceeded, http.StatusServiceUnavailable, true},
		"issuer failed":      {codes.Internal, http.StatusBadGateway, false},
	} {
		t.Run(name, func(t *testing.T) {
			gw, mint := newSolutionExchangeHarness(t)
			mint.code = test.code

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, solutionTokenRequest("example"))

			require.Equal(t, test.want, w.Code, w.Body.String())
			require.Equal(t, 1, mint.requestCount)
			require.Equal(t, test.retryAfter, w.Header().Get("Retry-After") != "")
		})
	}
}
