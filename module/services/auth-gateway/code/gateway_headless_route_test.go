package main

import (
	apigen "auth-gateway/external/saas-starter/accounts"
	"context"
	"crypto/ed25519"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayHeadlessOptInProxyAndRefusals(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	upstream, address := newModuleUpstream(t)
	require.Equal(t, 200, registerModule(t, gw, priv, "documents", address).Code)
	keys := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{"headless": priv.Public().(ed25519.PublicKey)}), nil)
	gw.workContext = newWorkContextVerifier(keys.URL)
	gw.headlessModulePrefixes = map[string]bool{"documents": true}
	var calls, replyCode atomic.Int32
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	apigen.RegisterWorkContextServiceServer(server, &revisionOracleFixture{check: func(ctx context.Context, req *apigen.CheckAuthorizationRevisionRequest) error {
		calls.Add(1)
		require.Equal(t, "tenant-1", req.OrgId)
		require.Equal(t, "owner-1", req.OwnerPrincipalId)
		if code := codes.Code(replyCode.Load()); code != codes.OK {
			return status.Error(code, "oracle refusal")
		}
		return nil
	}})
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	t.Cleanup(func() { _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///route-authority", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	gw.authz.backendConn = conn
	gw.authz.internalToken = "fixture-internal-token"
	mint := func(audience string, expired bool) string {
		now := time.Now
		if expired {
			now = func() time.Time { return time.Now().Add(-time.Hour) }
		}
		signer, err := workcontext.NewWorkContextSigner(workcontext.WorkContextSignerOptions{Issuer: "saas-starter", KeyID: "headless", PrivateKey: priv, Now: now})
		require.NoError(t, err)
		token, _, err := signer.StartTask(workcontext.StartTaskInput{Audience: audience, TenantID: "tenant-1", OwnerPrincipalID: "owner-1", TaskID: "task-1", SessionID: "session-1", AuthorizationRevision: 42, TTL: time.Minute, AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: "documents", Actions: []string{"read"}}}})
		require.NoError(t, err)
		return token.Encoded()
	}
	valid := mint("documents", false)
	request := func(token string, mutate func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/v1/documents/collection", nil)
		if token != "" {
			req.Header.Set(workcontext.WorkContextHeaderName, token)
		}
		if mutate != nil {
			mutate(req)
		}
		out := httptest.NewRecorder()
		gw.ServeHTTP(out, req)
		return out
	}
	for _, tc := range []struct {
		name, token string
		change      func(*http.Request)
	}{
		{name: "missing"}, {name: "wrong audience", token: mint("another", false)},
		{name: "expired", token: mint("documents", true)}, {name: "malformed", token: "invalid"},
		{name: "duplicate", token: valid, change: func(r *http.Request) { r.Header.Add(workcontext.WorkContextHeaderName, valid) }},
		{name: "bad bearer no fallback", token: valid, change: func(r *http.Request) { r.Header.Set("Authorization", "Bearer invalid") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, 401, request(tc.token, tc.change).Code)
			require.Nil(t, upstream.lastHeaders)
		})
	}
	require.Zero(t, calls.Load(), "invalid request must not reach authority oracle")
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.PermissionDenied, 403}, {codes.FailedPrecondition, 403}, {codes.Unavailable, 503}} {
		replyCode.Store(int32(tc.code))
		require.Equal(t, tc.want, request(valid, nil).Code)
		require.Nil(t, upstream.lastHeaders)
	}
	replyCode.Store(int32(codes.OK))
	good := request(valid, func(r *http.Request) {
		r.Header.Set("X-User-Id", "forged")
		r.Header.Set("X-Org-Id", "forged")
		r.Header.Set("X-Org-Role", "admin")
		r.Header.Set("X-Codefly-Internal-Token", "forged")
		r.Header.Set("Grpc-Metadata-X-User-Id", "forged")
	})
	require.Equal(t, 200, good.Code)
	require.Equal(t, "owner-1", upstream.lastHeaders.Get("X-User-Id"))
	require.Equal(t, "tenant-1", upstream.lastHeaders.Get("X-Org-Id"))
	for _, name := range []string{"X-Org-Role", "X-Codefly-Internal-Token", "Grpc-Metadata-X-User-Id"} {
		require.Empty(t, upstream.lastHeaders.Get(name))
	}
	require.Equal(t, valid, upstream.lastHeaders.Get(workcontext.WorkContextHeaderName))
	// Bearer requests retain their existing path even on an opted-in target.
	require.Equal(t, 200, request("", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+signValidToken(t, priv)) }).Code)
	gw.rateLimiter = NewRateLimiter(1)
	// Existing limiter permits one request plus one burst request.
	require.Equal(t, 200, request(valid, nil).Code)
	require.Equal(t, 200, request(valid, nil).Code)
	require.Equal(t, 429, request(valid, func(r *http.Request) { r.Header.Set("X-Org-Id", "another-budget") }).Code)
}

func TestHeadlessModulePolicyRequiresExactOptIn(t *testing.T) {
	for _, raw := range []string{"", "[]"} {
		got, err := parseHeadlessModulePrefixes(raw)
		require.NoError(t, err)
		require.Empty(t, got)
	}
	got, err := parseHeadlessModulePrefixes(`["documents"]`)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"documents": true}, got)
	for _, raw := range []string{`null`, `{}`, `["*"]`, `["documents/other"]`, `["documents","documents"]`, `[""]`, `[1]`} {
		_, err := parseHeadlessModulePrefixes(raw)
		require.Error(t, err, raw)
	}
}
