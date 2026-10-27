package main

import (
	"connectrpc.com/connect"
	"context"
	"google.golang.org/protobuf/types/known/emptypb"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authorityv1 "github.com/codefly-dev/authority/libraries/authority-client/go/gen/host/authority/v1"
	hop "github.com/codefly-dev/authority/libraries/hop/go"
	"github.com/codefly-dev/authority/libraries/hop/go/routing"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// A browser's module call through the hop: the gateway authenticates the
// person, the authority issues the person's Work Context at the module the
// published routes name for the path, and the hop's face receives the call
// with that context and nothing else about who the caller is. The face is a
// recorder here; gateway_hop_test.go runs the hop library's own face.

const listCollection = "/documents.v1.DocumentsService/ListCollection"

// testHopRoutes is a composition of two modules: documents serving Connect,
// tracing serving HTTP.
func testHopRoutes(t *testing.T) *routing.Routing {
	t.Helper()
	routes := &routing.Routing{Schema: routing.RoutingSchema, Endpoints: []routing.RouteEndpoint{
		{Audience: "documents", Service: "documents", Endpoint: "connect", Transport: "connect",
			Address: "http://127.0.0.1:1", Package: "documents", Version: "0.1.0", Digest: "sha256:d",
			Methods: []string{listCollection}},
		{Audience: "tracing", Service: "query", Endpoint: "http", Transport: "http",
			Address: "http://127.0.0.1:2", Package: "tracing", Version: "0.1.0", Digest: "sha256:t",
			Methods: []string{"GET /v1/traces/{id}"}},
	}}
	require.NoError(t, routes.Validate())
	return routes
}

// issuerFake is the authority's Issue: it records who asked for which module
// and answers a token named after both, or the refusal it is given.
type issuerFake struct {
	mu      sync.Mutex
	calls   int
	bearer  string
	request *authorityv1.IssueRequest
	refuse  error
}

func (f *issuerFake) Issue(ctx context.Context, in *authorityv1.IssueRequest, _ ...grpc.CallOption) (*authorityv1.IssueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	md, _ := metadata.FromOutgoingContext(ctx)
	f.bearer = strings.Join(md.Get("authorization"), ",")
	f.request = in
	if f.refuse != nil {
		return nil, f.refuse
	}
	return &authorityv1.IssueResponse{Token: "wc-for-" + in.GetAudience(), ExpiresAtUnix: time.Now().Add(5 * time.Minute).Unix()}, nil
}

// faceRecorder stands in for the hop's HTTP face.
type faceRecorder struct {
	calls  int
	header http.Header
	path   string
}

func (f *faceRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	f.header = r.Header.Clone()
	f.path = r.URL.Path
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("module-answer"))
}

func hopBrowserHarness(t *testing.T) (*Gateway, *issuerFake, *faceRecorder, string) {
	t.Helper()
	gw, _, _, priv := newGatewayHarness(t)
	issuer, face := &issuerFake{}, &faceRecorder{}
	gw.hop = &hopRole{modules: []string{"documents", "tracing"}, routes: testHopRoutes(t), issuer: issuer, face: face}
	return gw, issuer, face, signValidToken(t, priv)
}

func TestHopBrowserCallReachesTheHopWithOnlyThePersonsWorkContext(t *testing.T) {
	gw, issuer, face, bearer := hopBrowserHarness(t)

	req := httptest.NewRequest(http.MethodPost, listCollection, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Cookie", "codefly_rt=a-refresh-token")
	req.Header.Set("X-User-Id", "someone-else")
	req.Header.Set(hop.WorkContextKey, "a-context-the-caller-chose")
	req.Header.Set("X-Codefly-Hop-Proof", "a-forged-admission")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "module-answer", w.Body.String())
	require.Equal(t, 1, face.calls)
	require.Equal(t, listCollection, face.path)
	require.Equal(t, []string{"wc-for-documents"}, face.header.Values(hop.WorkContextKey),
		"the hop is handed the Work Context the authority issued, and only that one")
	for _, name := range []string{"Authorization", "Cookie", "X-User-Id", "X-Org-Id", "X-Credential-Kind", "X-Codefly-Hop-Proof", "X-Codefly-Gateway-Token"} {
		require.Empty(t, face.header.Values(name), "%s reached the hop", name)
	}

	require.Equal(t, "documents", issuer.request.GetAudience())
	require.NotNil(t, issuer.request.GetPerson(), "the Work Context is the person's")
	require.Equal(t, "Bearer "+bearer, issuer.bearer, "the authority authenticates the person by their own session")
}

func TestHopBrowserCallToAnHTTPRouteNamesItsModule(t *testing.T) {
	gw, issuer, face, bearer := hopBrowserHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/traces/abc", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "tracing", issuer.request.GetAudience())
	require.Equal(t, []string{"wc-for-tracing"}, face.header.Values(hop.WorkContextKey))
}

func TestHopBrowserCallHoldsTheWorkContextUntilItNearlyLapses(t *testing.T) {
	gw, issuer, face, bearer := hopBrowserHarness(t)
	for range 3 {
		req := httptest.NewRequest(http.MethodPost, listCollection, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	}
	require.Equal(t, 3, face.calls)
	require.Equal(t, 1, issuer.calls, "the person's Work Context is held, and the session re-authenticated on each call")
}

func TestHopBrowserCallWithoutASessionIsRefusedBeforeTheAuthority(t *testing.T) {
	gw, issuer, face, _ := hopBrowserHarness(t)
	req := httptest.NewRequest(http.MethodPost, listCollection, strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.Zero(t, issuer.calls)
	require.Zero(t, face.calls)
}

func TestHopBrowserCallTheAuthorityRefusesNamesItsReason(t *testing.T) {
	gw, issuer, face, bearer := hopBrowserHarness(t)
	issuer.refuse = status.Error(codes.PermissionDenied, "no installation of documents grants this person")
	req := httptest.NewRequest(http.MethodPost, listCollection, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "the authority refused this person at documents: no installation of documents grants this person")
	require.Zero(t, face.calls)
}

// A path no module publishes is answered as any unexposed path, and a module
// path on a gateway that stands no hop (a local run) is too: nothing reaches a
// module except through the hop.
func TestHopNoRouteReachesAModuleDirectly(t *testing.T) {
	gw, issuer, face, bearer := hopBrowserHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/nothing-publishes-this", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Zero(t, issuer.calls)
	require.Zero(t, face.calls)

	// A declared module record names an upstream in the registry; it is not a
	// route.
	module := declareModule(t, gw, "documents")
	gw.hop = nil
	for _, path := range []string{listCollection, "/v1/documents/collection"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, path)
	}
	require.Zero(t, module.calls, "the module was reached without the hop")
}

// The same procedure published by two modules is refused rather than sent to
// either.
func TestHopBrowserCallAnAmbiguousRouteIsRefused(t *testing.T) {
	gw, issuer, face, bearer := hopBrowserHarness(t)
	routes := testHopRoutes(t)
	second := routes.Endpoints[0]
	second.Audience, second.Package = "records", "records"
	routes.Endpoints = append(routes.Endpoints, second)
	gw.hop.routes, gw.hop.modules = routes, []string{"documents", "records", "tracing"}

	req := httptest.NewRequest(http.MethodPost, listCollection, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "published by more than one module (documents, records)")
	require.Zero(t, issuer.calls)
	require.Zero(t, face.calls)
}

func TestHopModulesReadsTheComposition(t *testing.T) {
	require.Equal(t, []string{"documents", "tracing"}, hopModules(" tracing,documents,, tracing "))
	require.Empty(t, hopModules(""))
}

func TestHopBrowserConnectClientKeepsAuthorityRefusal(t *testing.T) {
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unavailable, codes.Unauthenticated} {
		t.Run(code.String(), func(t *testing.T) {
			gw, issuer, face, bearer := hopBrowserHarness(t)
			const reason = "the current installation does not grant this person"
			issuer.refuse = status.Error(code, reason)
			server := httptest.NewServer(gw)
			defer server.Close()
			client := connect.NewClient[emptypb.Empty, emptypb.Empty](server.Client(), server.URL+listCollection)
			request := connect.NewRequest(&emptypb.Empty{})
			request.Header().Set("Authorization", "Bearer "+bearer)
			_, err := client.CallUnary(context.Background(), request)
			require.Equal(t, connect.Code(code), connect.CodeOf(err))
			require.ErrorContains(t, err, reason)
			require.Zero(t, face.calls)
		})
	}
}
