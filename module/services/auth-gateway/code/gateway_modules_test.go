package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestIsDisallowedModuleUpstreamHost(t *testing.T) {
	allowed := []string{
		"127.0.0.1", "::1", "localhost",
		"10.1.2.3", "172.16.0.5", "192.168.1.1", // RFC1918
		"fd00::1",                       // ULA
		"accounts",                      // bare mesh service name
		"documents.svc", "api.internal", // cluster suffixes
		"pod.namespace.svc.cluster.local",
	}
	for _, h := range allowed {
		require.Falsef(t, isDisallowedRegisteredUpstreamHost(h), "%q should be allowed (mesh-local)", h)
	}

	disallowed := []string{
		"", "8.8.8.8", "93.184.216.34",
		"api.example.com", "evil.co.uk",
		"169.254.169.254",          // link-local / metadata IP
		"metadata.google.internal", // metadata hostname
		"0.0.0.0",                  // unspecified
		"evil.localhost",           // multi-label *.localhost is NOT loopback: real DNS lookup
	}
	for _, h := range disallowed {
		require.Truef(t, isDisallowedRegisteredUpstreamHost(h), "%q should be rejected", h)
	}
}

// ============================================================================
// Resolve-time SSRF / DNS-rebinding defense.
// ============================================================================

// isAllowedResolvedModuleIP is the resolve-time counterpart to the host-string
// guard: only loopback and private (RFC1918/ULA) addresses are mesh-local.
func TestIsAllowedResolvedModuleIP(t *testing.T) {
	allowed := []string{
		"127.0.0.1", "127.0.0.53", "::1", // loopback
		"10.1.2.3", "172.16.0.5", "192.168.1.1", "fd00::1", // private / ULA
	}
	for _, s := range allowed {
		require.Truef(t, isAllowedResolvedModuleIP(net.ParseIP(s)), "%q should be mesh-local", s)
	}
	disallowed := []string{
		"8.8.8.8", "93.184.216.34", "1.1.1.1", // public
		"169.254.169.254", "fe80::1", // link-local (metadata)
		"0.0.0.0", "::", // unspecified
	}
	for _, s := range disallowed {
		require.Falsef(t, isAllowedResolvedModuleIP(net.ParseIP(s)), "%q should be rejected", s)
	}
	require.False(t, isAllowedResolvedModuleIP(nil))
}

// validateResolvedModuleAddrs fails closed on an empty result and rejects the
// WHOLE set if any single address is off-mesh (no retry to a rebinding-mixed
// forbidden IP).
func TestValidateResolvedModuleAddrs(t *testing.T) {
	require.Error(t, validateResolvedModuleAddrs(nil))
	require.NoError(t, validateResolvedModuleAddrs([]net.IPAddr{
		{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("10.0.0.1")},
	}))
	// A benign address mixed with a forbidden one rejects the entire dial.
	require.Error(t, validateResolvedModuleAddrs([]net.IPAddr{
		{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("8.8.8.8")},
	}))
}

// The module transport MUST NOT honor an environment proxy. It is built by
// cloning http.DefaultTransport, which carries Proxy: ProxyFromEnvironment; left
// intact, a configured HTTP(S)_PROXY would make the transport dial the proxy, so
// the resolve-and-pin DialContext would validate the proxy's address instead of
// the upstream's and tunnel in-mesh traffic off-mesh. Proxy must be disabled so
// the validated direct dial is the only path to the upstream.
func TestModuleUpstreamTransport_DisablesProxy(t *testing.T) {
	// Baseline: the clone source really does carry a proxy, so a nil Proxy on the
	// module transport is a deliberate override, not a coincidental default.
	require.NotNil(t, http.DefaultTransport.(*http.Transport).Clone().Proxy,
		"precondition: DefaultTransport clone carries ProxyFromEnvironment")

	tr := newModuleUpstreamTransport(stubResolver{})
	require.Nil(t, tr.Proxy, "module transport must not route module upstreams through an env proxy")
}

// stubResolver maps a host to a fixed set of addresses, standing in for DNS so a
// rebinding scenario is deterministic in a unit test.
type stubResolver map[string][]net.IP

func (s stubResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips, ok := s[host]
	if !ok {
		return nil, fmt.Errorf("no stub for %s", host)
	}
	addrs := make([]net.IPAddr, len(ips))
	for i, ip := range ips {
		addrs[i] = net.IPAddr{IP: ip}
	}
	return addrs, nil
}

func mustPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u.Port()
}

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u.Host
}

// The module secret header is gone with the secret (#1056) and nothing reads it,
// but it stays on the strip list, like the other retired credential headers:
// stripping it is what stops a caller supplying it to an upstream that might.
func TestGateway_ModuleSecretHeaderStrippedFromProxiedRequests(t *testing.T) {
	const retiredModuleSecretHeader = "X-Codefly-Module-Secret"
	gw, apiFake, _, priv := newGatewayHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set(retiredModuleSecretHeader, "documents-secret")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, apiFake.lastHeaders.Get(retiredModuleSecretHeader))
}

// A mint presents exactly one bearer, and the broker forwards nothing else: a
// retired secret header, a second Authorization header or another scheme each
// leave accounts unasked.
func TestGateway_ModuleWorkContext_TakesOnlyTheProjectedTokenBearer(t *testing.T) {
	for name, set := range map[string]func(h http.Header){
		"the retired secret header": func(h http.Header) { h.Set("X-Codefly-Module-Secret", "documents-secret") },
		"another scheme":            func(h http.Header) { h.Set("Authorization", "Basic ZG9jdW1lbnRzOnNlY3JldA==") },
		"two bearers": func(h http.Header) {
			h.Add("Authorization", "Bearer one")
			h.Add("Authorization", "Bearer two")
		},
		"an empty bearer": func(h http.Header) { h.Set("Authorization", "Bearer   ") },
	} {
		t.Run(name, func(t *testing.T) {
			gw, mint := newWorkContextExchangeHarness(t)
			req := workContextRequest("documents", "", "test-internal-token")
			set(req.Header)
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)

			require.Equal(t, http.StatusUnauthorized, w.Code)
			require.Zero(t, mint.requestCount)
		})
	}
}

// ============================================================================
// Work Context exchange (/modules/_work-context)
// ============================================================================

// fakeAccountsWorkContextMint stands in for accounts' MintModuleWorkContext RPC,
// registered under the real service and method name so the test exercises the
// exact procedure string the generated mesh policy admits.
type fakeAccountsWorkContextMint struct {
	code             codes.Code
	lastInternal     string
	lastPrefix       string
	lastOrganization string
	lastToken        string
	requestCount     int
}

func (f *fakeAccountsWorkContextMint) handle(ctx context.Context, dec func(any) error) (any, error) {
	req := &accountsv1.ModuleMintWorkContextRequest{}
	if err := dec(req); err != nil {
		return nil, err
	}
	f.requestCount++
	f.lastPrefix = req.GetPrefix()
	f.lastOrganization = req.GetOrganizationId()
	f.lastToken = req.GetServiceAccountToken()
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("x-codefly-internal-token"); len(values) > 0 {
			f.lastInternal = values[0]
		}
	}
	if f.code != codes.OK {
		return nil, status.Error(f.code, "denied")
	}
	return &accountsv1.ModuleMintWorkContextResponse{
		Token:       "work-context-for-" + req.GetPrefix(),
		ExpiresAt:   timestamppb.New(time.Now().Add(15 * time.Minute)),
		PrincipalId: "00000000-0000-4000-8000-00000000beef",
		Tenant:      "11111111-1111-4111-8111-111111111111",
	}, nil
}

func newWorkContextExchangeHarness(t *testing.T) (*Gateway, *fakeAccountsWorkContextMint) {
	t.Helper()
	gw, _, _, _ := newGatewayHarness(t)
	mint := &fakeAccountsWorkContextMint{}

	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "saas.accounts.v1.ModuleCapabilitiesService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "MintModuleWorkContext",
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

// onWorkloadEndpoint marks a request as arrived on the gateway's workload
// endpoint, as that endpoint's server does for everything it serves. The three
// module credential exchanges are served there and nowhere else.
func onWorkloadEndpoint(req *http.Request) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), workloadListenerKey{}, true))
}

func workContextRequest(prefix, token, internal string) *http.Request {
	req := onWorkloadEndpoint(httptest.NewRequest(http.MethodPost, moduleWorkContextPath,
		strings.NewReader(fmt.Sprintf(`{"prefix":%q}`, prefix))))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if internal != "" {
		req.Header.Set("X-Codefly-Internal-Token", internal)
	}
	return req
}

func TestGateway_ModuleWorkContext_BrokersToAccounts(t *testing.T) {
	gw, mint := newWorkContextExchangeHarness(t)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, workContextRequest("documents", "documents-projected-token", "test-internal-token"))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "no-store", w.Header().Get("cache-control"))
	// The gateway presents its OWN cluster credential on the internal leg and
	// forwards the module pod's projected token for accounts to judge.
	require.Equal(t, "test-internal-token", mint.lastInternal)
	require.Equal(t, "documents", mint.lastPrefix)
	require.Equal(t, "documents-projected-token", mint.lastToken)

	var payload struct {
		Token       string `json:"token"`
		PrincipalID string `json:"principalId"`
		Tenant      string `json:"tenant"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Equal(t, "work-context-for-documents", payload.Token)
	require.Equal(t, "00000000-0000-4000-8000-00000000beef", payload.PrincipalID)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", payload.Tenant)
}

func TestGateway_ModuleWorkContext_FailsClosed(t *testing.T) {
	tests := map[string]struct {
		prefix   string
		token    string
		internal string
		want     int
	}{
		"no projected token": {"documents", "", "test-internal-token", http.StatusUnauthorized},
		"path prefix":        {"documents/nested", "documents-projected-token", "test-internal-token", http.StatusBadRequest},
		"wildcard prefix":    {"*", "documents-projected-token", "test-internal-token", http.StatusBadRequest},
		"empty prefix":       {"", "documents-projected-token", "test-internal-token", http.StatusBadRequest},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			gw, mint := newWorkContextExchangeHarness(t)

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, workContextRequest(test.prefix, test.token, test.internal))

			require.Equal(t, test.want, w.Code)
			// Nothing reached accounts: the perimeter rejected it first.
			require.Zero(t, mint.requestCount)
		})
	}
}

// accounts owns the decision, so its refusal is the gateway's — and a refusal, a
// rejected request, and an outage stay distinguishable to the module.
func TestGateway_ModuleWorkContext_RelaysAccountsOutcome(t *testing.T) {
	for name, test := range map[string]struct {
		code codes.Code
		want int
	}{
		// A refused token is invalid credentials to the published client.
		"refusal":          {codes.PermissionDenied, http.StatusUnauthorized},
		"rejected request": {codes.InvalidArgument, http.StatusBadRequest},
		// accounts could not establish what the pod runs: retryable, not a verdict.
		"execution unbound": {codes.Unavailable, http.StatusServiceUnavailable},
		// Admitted and installed nowhere: its own answer, so the module serves
		// its install endpoint and waits instead of stopping on a refusal.
		"installed nowhere": {codes.FailedPrecondition, http.StatusPreconditionFailed},
		"outage":            {codes.Internal, http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			gw, mint := newWorkContextExchangeHarness(t)
			mint.code = test.code

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, workContextRequest("documents", "documents-projected-token", "test-internal-token"))

			require.Equal(t, test.want, w.Code)
			require.Equal(t, 1, mint.requestCount)
		})
	}
}

// The body is bounded: the manifest is the only large field, and a request
// past the bound is refused before accounts is asked anything.
func TestGateway_ModuleWorkContext_BoundsTheRequest(t *testing.T) {
	gw, mint := newWorkContextExchangeHarness(t)
	body, err := json.Marshal(map[string]string{"prefix": "documents", "padding": strings.Repeat("a", 4096)})
	require.NoError(t, err)
	req := workContextRequest("documents", "documents-projected-token", "test-internal-token")
	req.Body = io.NopCloser(bytes.NewReader(body))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, mint.requestCount)

	body, err = json.Marshal(map[string]string{"prefix": "documents", "padding": strings.Repeat("a", 2048)})
	require.NoError(t, err)
	req = workContextRequest("documents", "documents-projected-token", "test-internal-token")
	req.Body = io.NopCloser(bytes.NewReader(body))
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, mint.requestCount)
}

// The three exchanges are served on the workload endpoint alone. On the public
// endpoint each is an unexposed path: 404, with accounts never asked.
func TestGateway_ModuleExchanges_AreServedOnTheWorkloadEndpointOnly(t *testing.T) {
	gw, mint := newWorkContextExchangeHarness(t)
	for _, path := range []string{moduleWorkContextPath, moduleOperationContextPath, moduleSourceOperationContextPath} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"prefix":"documents"}`))
		req.Header.Set("Authorization", "Bearer documents-projected-token")
		req.Header.Set("X-Codefly-Internal-Token", "test-internal-token")
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, path)
		require.Zero(t, mint.requestCount, path)
	}
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, workContextRequest("documents", "documents-projected-token", ""))
	require.Equal(t, http.StatusOK, w.Code, "on the workload endpoint the projected token is the one credential")
	require.Equal(t, 1, mint.requestCount)
}

func TestGateway_ModuleWorkContext_MethodNotAllowed(t *testing.T) {
	gw, _ := newWorkContextExchangeHarness(t)

	req := onWorkloadEndpoint(httptest.NewRequest(http.MethodGet, moduleWorkContextPath, nil))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestGateway_ModuleWorkContext_ForwardsOrganizationSelector(t *testing.T) {
	gw, mint := newWorkContextExchangeHarness(t)
	req := workContextRequest("example", "projected-token", "test-internal-token")
	req.Body = io.NopCloser(strings.NewReader(`{"prefix":"example","organization_id":"11111111-1111-4111-8111-111111111111"}`))
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", mint.lastOrganization)
}
