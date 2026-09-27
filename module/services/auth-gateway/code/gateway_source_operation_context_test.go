package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	testDelegationID = "22222222-2222-4222-8222-222222222222"
	testSourceID     = "33333333-3333-4333-8333-333333333333"
)

// fakeAccountsSourceOperationContextMint stands in for accounts' source
// delegation mint on the internal listener, recording what the gateway
// forwarded.
type fakeAccountsSourceOperationContextMint struct {
	err          error
	requestCount int
	last         *accountsv1.ModuleMintSourceOperationContextRequest
	lastInternal string
	expiresAt    time.Time
}

func (f *fakeAccountsSourceOperationContextMint) handle(ctx context.Context, dec func(any) error) (any, error) {
	req := &accountsv1.ModuleMintSourceOperationContextRequest{}
	if err := dec(req); err != nil {
		return nil, err
	}
	f.requestCount++
	f.last = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("x-codefly-internal-token"); len(values) > 0 {
			f.lastInternal = values[0]
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return &accountsv1.ModuleMintSourceOperationContextResponse{
		Token:            "source-operation-context-for-" + req.GetPrefix(),
		ExpiresAt:        timestamppb.New(f.expiresAt),
		PrincipalId:      "00000000-0000-4000-8000-00000000beef",
		Tenant:           "11111111-1111-4111-8111-111111111111",
		Audience:         "ingestservice",
		Binding:          "source-sync",
		DelegationId:     testDelegationID,
		SourceId:         testSourceID,
		OwnerPrincipalId: "44444444-4444-4444-8444-444444444444",
	}, nil
}

func newSourceOperationContextHarness(t *testing.T) (*Gateway, *fakeAccountsSourceOperationContextMint) {
	t.Helper()
	gw, _, _, _ := newGatewayHarness(t)
	mint := &fakeAccountsSourceOperationContextMint{expiresAt: time.Now().Add(time.Minute).UTC().Truncate(time.Second)}

	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "saas.accounts.v1.ModuleCapabilitiesService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "MintSourceOperationContext",
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

func sourceOperationContextRequest(body, secret, internal string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, moduleSourceOperationContextPath, strings.NewReader(body))
	if secret != "" {
		req.Header.Set(moduleSecretHeader, secret)
	}
	if internal != "" {
		req.Header.Set("X-Codefly-Internal-Token", internal)
	}
	return req
}

func TestGateway_ModuleSourceOperationContext_BrokersToAccounts(t *testing.T) {
	for name, test := range map[string]struct {
		body  string
		check func(t *testing.T, req *accountsv1.ModuleMintSourceOperationContextRequest)
	}{
		"by delegation": {`{"prefix":"docstore","delegation_id":"` + testDelegationID + `"}`,
			func(t *testing.T, req *accountsv1.ModuleMintSourceOperationContextRequest) {
				require.Equal(t, testDelegationID, req.GetDelegationId())
				require.Empty(t, req.GetSourceId())
			}},
		"by source": {`{"prefix":"docstore","source_id":"` + testSourceID + `"}`,
			func(t *testing.T, req *accountsv1.ModuleMintSourceOperationContextRequest) {
				require.Equal(t, testSourceID, req.GetSourceId())
				require.Empty(t, req.GetDelegationId())
			}},
	} {
		t.Run(name, func(t *testing.T) {
			gw, mint := newSourceOperationContextHarness(t)

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, sourceOperationContextRequest(test.body, "docstore-secret", "test-internal-token"))

			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, "no-store", w.Header().Get("cache-control"))
			require.Equal(t, "application/json", w.Header().Get("content-type"))
			// The gateway presents its OWN cluster credential and forwards the
			// module's secret and the delegation it named; it decides nothing.
			require.Equal(t, "test-internal-token", mint.lastInternal)
			require.Equal(t, "docstore", mint.last.GetPrefix())
			require.Equal(t, "docstore-secret", mint.last.GetSecret())
			test.check(t, mint.last)

			var payload map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
			require.Equal(t, map[string]string{
				"work_context":       "source-operation-context-for-docstore",
				"expires_at":         mint.expiresAt.Format(time.RFC3339),
				"principal_id":       "00000000-0000-4000-8000-00000000beef",
				"owner_principal_id": "44444444-4444-4444-8444-444444444444",
				"tenant":             "11111111-1111-4111-8111-111111111111",
				"audience":           "ingestservice",
				"binding":            "source-sync",
				"delegation_id":      testDelegationID,
				"source_id":          testSourceID,
			}, payload)
		})
	}
}

func TestGateway_ModuleSourceOperationContext_FailsClosed(t *testing.T) {
	byDelegation := `{"prefix":"docstore","delegation_id":"` + testDelegationID + `"}`
	tests := map[string]struct {
		body     string
		secret   string
		internal string
		want     int
	}{
		"no internal token":  {byDelegation, "docstore-secret", "", http.StatusUnauthorized},
		"bad internal token": {byDelegation, "docstore-secret", "wrong", http.StatusUnauthorized},
		"no module secret":   {byDelegation, "", "test-internal-token", http.StatusUnauthorized},
		"path prefix":        {`{"prefix":"docstore/nested","source_id":"` + testSourceID + `"}`, "docstore-secret", "test-internal-token", http.StatusBadRequest},
		"neither":            {`{"prefix":"docstore"}`, "docstore-secret", "test-internal-token", http.StatusBadRequest},
		"both": {`{"prefix":"docstore","delegation_id":"` + testDelegationID + `","source_id":"` + testSourceID + `"}`,
			"docstore-secret", "test-internal-token", http.StatusBadRequest},
		"oversized id": {`{"prefix":"docstore","source_id":"` + strings.Repeat("a", 65) + `"}`, "docstore-secret", "test-internal-token", http.StatusBadRequest},
		"not json":     {`source_id=x`, "docstore-secret", "test-internal-token", http.StatusBadRequest},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			gw, mint := newSourceOperationContextHarness(t)

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, sourceOperationContextRequest(test.body, test.secret, test.internal))

			require.Equal(t, test.want, w.Code)
			require.Zero(t, mint.requestCount, "the perimeter rejects it before accounts is asked")
		})
	}
}

func missingDelegation(reason, domain string) error {
	return delegationRefusal(codes.FailedPrecondition, reason, domain)
}

func refusedDelegation(reason, domain string) error {
	return delegationRefusal(codes.PermissionDenied, reason, domain)
}

func delegationRefusal(code codes.Code, reason, domain string) error {
	refused := status.New(code, "delegation refused")
	detailed, err := refused.WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: domain})
	if err != nil {
		panic(err)
	}
	return detailed.Err()
}

// accounts owns the decision; the gateway keeps its outcomes distinguishable.
// Only a FailedPrecondition carrying accounts' DELEGATION_MISSING reason is the
// 412 a module reads as "a person must reconnect this source", and only a
// PermissionDenied carrying DELEGATION_REVOKED or DELEGATION_INVALID names its
// reason in the 403 body; every other refusal stays a bare `forbidden`, so the
// gateway never says more about a delegation than accounts decided to.
func TestGateway_ModuleSourceOperationContext_RelaysAccountsOutcome(t *testing.T) {
	for name, test := range map[string]struct {
		err      error
		want     int
		wantBody string
	}{
		"unproven module":             {status.Error(codes.Unauthenticated, "denied"), http.StatusUnauthorized, "unauthorized"},
		"untyped denial":              {status.Error(codes.PermissionDenied, "denied"), http.StatusForbidden, "forbidden"},
		"delegation revoked":          {refusedDelegation(sourceDelegationRevokedReason, solutionRegistryErrorDomain), http.StatusForbidden, "DELEGATION_REVOKED"},
		"delegation invalid":          {refusedDelegation(sourceDelegationInvalidReason, solutionRegistryErrorDomain), http.StatusForbidden, "DELEGATION_INVALID"},
		"denial with foreign reason":  {refusedDelegation("TENANT_MISMATCH", solutionRegistryErrorDomain), http.StatusForbidden, "forbidden"},
		"denial with foreign domain":  {refusedDelegation(sourceDelegationRevokedReason, "example.com"), http.StatusForbidden, "forbidden"},
		"missing reason on a denial":  {refusedDelegation(sourceDelegationMissingReason, solutionRegistryErrorDomain), http.StatusForbidden, "forbidden"},
		"rejected request":            {status.Error(codes.InvalidArgument, "bad"), http.StatusBadRequest, "invalid request"},
		"delegation missing":          {missingDelegation(sourceDelegationMissingReason, solutionRegistryErrorDomain), http.StatusPreconditionFailed, "DELEGATION_MISSING"},
		"revoked reason on a precond": {missingDelegation(sourceDelegationRevokedReason, solutionRegistryErrorDomain), http.StatusBadGateway, "source operation context unavailable"},
		"foreign reason":              {missingDelegation("SOMETHING_ELSE", solutionRegistryErrorDomain), http.StatusBadGateway, "source operation context unavailable"},
		"foreign domain":              {missingDelegation(sourceDelegationMissingReason, "example.com"), http.StatusBadGateway, "source operation context unavailable"},
		"issuer unconfigured":         {status.Error(codes.FailedPrecondition, "unconfigured"), http.StatusBadGateway, "source operation context unavailable"},
		"outage":                      {status.Error(codes.Internal, "boom"), http.StatusBadGateway, "source operation context unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			gw, mint := newSourceOperationContextHarness(t)
			mint.err = test.err

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, sourceOperationContextRequest(`{"prefix":"docstore","source_id":"`+testSourceID+`"}`, "docstore-secret", "test-internal-token"))

			require.Equal(t, test.want, w.Code)
			require.Equal(t, 1, mint.requestCount)
			require.Equal(t, test.wantBody, w.Body.String())
		})
	}
}

// The reason strings are a wire contract with accounts
// (adapters.SourceDelegation{Missing,Revoked,Invalid}Reason); pin them on this
// side.
func TestGateway_ModuleSourceOperationContext_ReasonsAreTheWireContract(t *testing.T) {
	require.Equal(t, "DELEGATION_MISSING", sourceDelegationMissingReason)
	require.Equal(t, "DELEGATION_REVOKED", sourceDelegationRevokedReason)
	require.Equal(t, "DELEGATION_INVALID", sourceDelegationInvalidReason)
	require.Equal(t, "/modules/_source-operation-context", moduleSourceOperationContextPath)
}

func TestGateway_ModuleSourceOperationContext_MethodNotAllowed(t *testing.T) {
	gw, mint := newSourceOperationContextHarness(t)

	req := httptest.NewRequest(http.MethodGet, moduleSourceOperationContextPath, nil)
	req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Zero(t, mint.requestCount)
}
