package main

import (
	"context"
	"encoding/json"
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
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeAccountsOperationContextMint stands in for accounts' headless operation
// mint on the internal listener, recording what the gateway forwarded.
type fakeAccountsOperationContextMint struct {
	code         codes.Code
	requestCount int
	lastPrefix   string
	lastSecret   string
	lastBinding  string
	lastInternal string
	expiresAt    time.Time
}

func (f *fakeAccountsOperationContextMint) handle(ctx context.Context, dec func(any) error) (any, error) {
	req := &accountsv1.ModuleMintOperationContextRequest{}
	if err := dec(req); err != nil {
		return nil, err
	}
	f.requestCount++
	f.lastPrefix = req.GetPrefix()
	f.lastSecret = req.GetSecret()
	f.lastBinding = req.GetBinding()
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("x-codefly-internal-token"); len(values) > 0 {
			f.lastInternal = values[0]
		}
	}
	if f.code != codes.OK {
		return nil, status.Error(f.code, "denied")
	}
	return &accountsv1.ModuleMintOperationContextResponse{
		Token:       "operation-context-for-" + req.GetPrefix() + "-" + req.GetBinding(),
		ExpiresAt:   timestamppb.New(f.expiresAt),
		PrincipalId: "00000000-0000-4000-8000-00000000beef",
		Tenant:      "11111111-1111-4111-8111-111111111111",
		Audience:    "modelservice",
		Binding:     req.GetBinding(),
	}, nil
}

func newOperationContextExchangeHarness(t *testing.T) (*Gateway, *fakeAccountsOperationContextMint) {
	t.Helper()
	gw, _, _, _ := newGatewayHarness(t)
	mint := &fakeAccountsOperationContextMint{expiresAt: time.Now().Add(time.Minute).UTC().Truncate(time.Second)}

	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "saas.accounts.v1.ModuleCapabilitiesService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "MintModuleOperationContext",
			Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				return mint.handle(ctx, dec)
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

func operationContextRequest(body, secret, internal string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, moduleOperationContextPath, strings.NewReader(body))
	if secret != "" {
		req.Header.Set(moduleSecretHeader, secret)
	}
	if internal != "" {
		req.Header.Set("X-Codefly-Internal-Token", internal)
	}
	return req
}

func operationContextBody(prefix, binding string) string {
	return fmt.Sprintf(`{"prefix":%q,"binding":%q}`, prefix, binding)
}

func TestGateway_ModuleOperationContext_BrokersToAccounts(t *testing.T) {
	gw, mint := newOperationContextExchangeHarness(t)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, operationContextRequest(operationContextBody("documents", "model"), "documents-secret", "test-internal-token"))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "no-store", w.Header().Get("cache-control"))
	require.Equal(t, "application/json", w.Header().Get("content-type"))
	// The gateway presents its OWN cluster credential and forwards the module's
	// secret and chosen binding; it names no audience or scope of its own.
	require.Equal(t, "test-internal-token", mint.lastInternal)
	require.Equal(t, "documents", mint.lastPrefix)
	require.Equal(t, "documents-secret", mint.lastSecret)
	require.Equal(t, "model", mint.lastBinding)

	var payload map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Equal(t, map[string]string{
		"work_context": "operation-context-for-documents-model",
		"expires_at":   mint.expiresAt.Format(time.RFC3339),
		"principal_id": "00000000-0000-4000-8000-00000000beef",
		"tenant":       "11111111-1111-4111-8111-111111111111",
		"audience":     "modelservice",
		"binding":      "model",
	}, payload)
}

func TestGateway_ModuleOperationContext_FailsClosed(t *testing.T) {
	tests := map[string]struct {
		body     string
		secret   string
		internal string
		want     int
	}{
		"no internal token":  {operationContextBody("documents", "model"), "documents-secret", "", http.StatusUnauthorized},
		"bad internal token": {operationContextBody("documents", "model"), "documents-secret", "wrong", http.StatusUnauthorized},
		"no module secret":   {operationContextBody("documents", "model"), "", "test-internal-token", http.StatusUnauthorized},
		"path prefix":        {operationContextBody("documents/nested", "model"), "documents-secret", "test-internal-token", http.StatusBadRequest},
		"empty prefix":       {operationContextBody("", "model"), "documents-secret", "test-internal-token", http.StatusBadRequest},
		"missing prefix":     {`{"binding":"model"}`, "documents-secret", "test-internal-token", http.StatusBadRequest},
		"empty binding":      {operationContextBody("documents", ""), "documents-secret", "test-internal-token", http.StatusBadRequest},
		"oversized binding":  {operationContextBody("documents", strings.Repeat("b", 129)), "documents-secret", "test-internal-token", http.StatusBadRequest},
		"not json":           {`binding=model`, "documents-secret", "test-internal-token", http.StatusBadRequest},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			gw, mint := newOperationContextExchangeHarness(t)

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, operationContextRequest(test.body, test.secret, test.internal))

			require.Equal(t, test.want, w.Code)
			require.Zero(t, mint.requestCount, "the perimeter rejects it before accounts is asked")
		})
	}
}

// accounts owns the decision. An unproven module, a proven module asking for a
// binding it may not mint headless, a rejected request and an outage all stay
// distinguishable to the caller.
func TestGateway_ModuleOperationContext_RelaysAccountsOutcome(t *testing.T) {
	for name, test := range map[string]struct {
		code codes.Code
		want int
	}{
		"unproven module":      {codes.Unauthenticated, http.StatusUnauthorized},
		"binding not headless": {codes.PermissionDenied, http.StatusForbidden},
		"rejected request":     {codes.InvalidArgument, http.StatusBadRequest},
		"unknown tenant":       {codes.FailedPrecondition, http.StatusBadGateway},
		"outage":               {codes.Internal, http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			gw, mint := newOperationContextExchangeHarness(t)
			mint.code = test.code

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, operationContextRequest(operationContextBody("documents", "model"), "documents-secret", "test-internal-token"))

			require.Equal(t, test.want, w.Code)
			require.Equal(t, 1, mint.requestCount)
		})
	}
}

func TestGateway_ModuleOperationContext_MethodNotAllowed(t *testing.T) {
	gw, mint := newOperationContextExchangeHarness(t)

	req := httptest.NewRequest(http.MethodGet, moduleOperationContextPath, nil)
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Zero(t, mint.requestCount)
}
