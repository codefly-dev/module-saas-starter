package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// A check that reached no verdict is refused, and refused as what it is. A
// request the person abandoned (a page or view switch cancels it), a deadline
// that passed, or a store or authority that did not answer is never answered
// as PermissionDenied: a client reads a denial as "you may not" and tells a
// person who holds the grant that they do not. A denial stays a denial, with
// its reason. Neither ever reaches the module or the upstream.

// checkFailures are the three ways a check reaches no verdict, as the
// revocation store reports them, and the answer each must get.
var checkFailures = []struct {
	name       string
	err        error
	code       connect.Code
	httpStatus int
}{
	{"canceled", fmt.Errorf("revocation store get: %w", context.Canceled), connect.CodeCanceled, 499},
	{"deadline", fmt.Errorf("revocation store get: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded, http.StatusServiceUnavailable},
	{"unavailable", errors.New("revocation store get: dial tcp: connection refused"), connect.CodeUnavailable, http.StatusServiceUnavailable},
}

func TestHopBrowserCallAFailedCheckIsNeverADenial(t *testing.T) {
	for _, tc := range checkFailures {
		t.Run(tc.name+"/connect", func(t *testing.T) {
			gw, issuer, face, bearer := hopBrowserHarness(t)
			gw.authz.revoker = &fakeRevoker{err: tc.err}
			server := httptest.NewServer(gw)
			defer server.Close()
			client := connect.NewClient[emptypb.Empty, emptypb.Empty](server.Client(), server.URL+listCollection)
			request := connect.NewRequest(&emptypb.Empty{})
			request.Header().Set("Authorization", "Bearer "+bearer)
			_, err := client.CallUnary(context.Background(), request)
			require.Equal(t, tc.code, connect.CodeOf(err), "%v", err)
			require.NotEqual(t, connect.CodePermissionDenied, connect.CodeOf(err))
			require.ErrorContains(t, err, "revocation check unavailable")
			require.Zero(t, issuer.calls, "a failed check asks the authority for nothing")
			require.Zero(t, face.calls, "a failed check never reaches the module")
		})
		t.Run(tc.name+"/rest", func(t *testing.T) {
			gw, issuer, face, bearer := hopBrowserHarness(t)
			gw.authz.revoker = &fakeRevoker{err: tc.err}
			req := httptest.NewRequest(http.MethodPost, listCollection, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+bearer)
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)
			require.Equal(t, tc.httpStatus, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "revocation check unavailable")
			require.Zero(t, issuer.calls)
			require.Zero(t, face.calls)
		})
	}
}

// The person's own request ending decides, whatever the store says about why
// its read failed.
func TestHopBrowserCallTheCallerCancelledIsCanceled(t *testing.T) {
	gw, issuer, face, bearer := hopBrowserHarness(t)
	gw.authz.revoker = &fakeRevoker{err: errors.New("revocation store get: i/o timeout")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, listCollection, strings.NewReader(`{}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 499, w.Code, w.Body.String())
	require.Zero(t, issuer.calls)
	require.Zero(t, face.calls)
}

func TestHopBrowserCallADeniedCheckStaysADenial(t *testing.T) {
	t.Run("connect", func(t *testing.T) {
		gw, _, face, bearer := hopBrowserHarness(t)
		gw.authz.revoker = &fakeRevoker{revoked: map[string]bool{"jti": true}}
		server := httptest.NewServer(gw)
		defer server.Close()
		client := connect.NewClient[emptypb.Empty, emptypb.Empty](server.Client(), server.URL+listCollection)
		request := connect.NewRequest(&emptypb.Empty{})
		request.Header().Set("Authorization", "Bearer "+bearer)
		_, err := client.CallUnary(context.Background(), request)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.ErrorContains(t, err, "token revoked")
		require.Zero(t, face.calls)
	})
	t.Run("rest", func(t *testing.T) {
		gw, _, face, bearer := hopBrowserHarness(t)
		gw.authz.revoker = &fakeRevoker{revoked: map[string]bool{"jti": true}}
		req := httptest.NewRequest(http.MethodPost, listCollection, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code)
		require.Contains(t, w.Body.String(), "token revoked")
		require.Zero(t, face.calls)
	})
}

// The authority's Issue of the person's Work Context, asked and not answered,
// is not the authority refusing the person. A verdict keeps the authority's
// reason; a failure names only the module and the code, because the
// authority's message for a failure can carry internal error text or a
// transport address. That message goes to the gateway's log instead.
func TestHopBrowserCallTheAuthorityNotAnsweringIsNotARefusal(t *testing.T) {
	const message = "dial tcp 10.0.0.7:9090: connection refused"
	for _, tc := range []struct {
		refusal    codes.Code
		code       connect.Code
		httpStatus int
		reason     string
	}{
		{codes.PermissionDenied, connect.CodePermissionDenied, http.StatusForbidden, "the authority refused this person at documents: " + message},
		{codes.Canceled, connect.CodeCanceled, 499, "the authority gave no answer for this person at documents (Canceled)"},
		{codes.DeadlineExceeded, connect.CodeDeadlineExceeded, http.StatusServiceUnavailable, "the authority gave no answer for this person at documents (DeadlineExceeded)"},
		{codes.Unavailable, connect.CodeUnavailable, http.StatusServiceUnavailable, "the authority gave no answer for this person at documents (Unavailable)"},
		{codes.Internal, connect.CodeInternal, http.StatusInternalServerError, "the authority gave no answer for this person at documents (Internal)"},
	} {
		leaks := tc.refusal != codes.PermissionDenied
		t.Run(tc.refusal.String()+"/connect", func(t *testing.T) {
			gw, issuer, face, bearer := hopBrowserHarness(t)
			issuer.refuse = status.Error(tc.refusal, message)
			logs := captureGatewayLog(t)
			server := httptest.NewServer(gw)
			defer server.Close()
			client := connect.NewClient[emptypb.Empty, emptypb.Empty](server.Client(), server.URL+listCollection)
			request := connect.NewRequest(&emptypb.Empty{})
			request.Header().Set("Authorization", "Bearer "+bearer)
			_, err := client.CallUnary(context.Background(), request)
			require.Equal(t, tc.code, connect.CodeOf(err), "%v", err)
			require.ErrorContains(t, err, tc.reason)
			if leaks {
				require.NotContains(t, err.Error(), "10.0.0.7", "a failure's raw message stays on the server")
				require.Contains(t, logs(), message, "the gateway logs what the authority said")
			}
			require.Zero(t, face.calls)
		})
		t.Run(tc.refusal.String()+"/rest", func(t *testing.T) {
			gw, issuer, face, bearer := hopBrowserHarness(t)
			issuer.refuse = status.Error(tc.refusal, message)
			req := httptest.NewRequest(http.MethodPost, listCollection, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+bearer)
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)
			require.Equal(t, tc.httpStatus, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), tc.reason)
			if leaks {
				require.NotContains(t, w.Body.String(), "10.0.0.7")
			}
			require.Zero(t, face.calls)
		})
	}
}

// captureGatewayLog collects what the gateway logs for the rest of the test.
func captureGatewayLog(t *testing.T) func() string {
	t.Helper()
	var buf safeBuffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return buf.String
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The catalog's own routes answer the same way, and forward nothing.
func TestCatalogRouteAFailedCheckIsNeverADenial(t *testing.T) {
	for _, tc := range checkFailures {
		t.Run(tc.name, func(t *testing.T) {
			gw, api, _, priv := newGatewayHarness(t)
			gw.authz.revoker = &fakeRevoker{err: tc.err}
			req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
			req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)
			require.Equal(t, tc.httpStatus, w.Code, w.Body.String())
			require.NotEqual(t, http.StatusForbidden, w.Code)
			require.Contains(t, w.Body.String(), "revocation check unavailable")
			require.Zero(t, api.calls, "a failed check forwards nothing")
		})
	}
	t.Run("denied", func(t *testing.T) {
		gw, api, _, priv := newGatewayHarness(t)
		gw.authz.revoker = &fakeRevoker{revoked: map[string]bool{"jti": true}}
		req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
		req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code)
		require.Contains(t, w.Body.String(), "token revoked")
		require.Zero(t, api.calls)
	})
}

// A public route forwards a request that presented no credential, and only
// that: a presented credential whose check failed is refused there too.
func TestPublicRouteAFailedCheckForwardsNothing(t *testing.T) {
	gw, api, _, priv := newGatewayHarness(t)
	gw.authz.revoker = &fakeRevoker{err: fmt.Errorf("revocation store get: %w", context.Canceled)}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 499, w.Code, w.Body.String())
	require.Zero(t, api.calls)

	req = httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", nil)
	w = httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, 1, api.calls, "no credential on a public route goes on without identity")
}

// The ext_authz answer itself, as Envoy reads it: a failure is not an allow,
// and its status names the failure rather than PermissionDenied.
func TestExtAuthzAFailedCheckNamesItsFailure(t *testing.T) {
	for _, tc := range checkFailures {
		t.Run(tc.name, func(t *testing.T) {
			gw, _, _, priv := newGatewayHarness(t)
			gw.authz.revoker = &fakeRevoker{err: tc.err}
			resp, err := gw.authz.Check(context.Background(), checkReq("/v1/users", map[string]string{
				"authorization": "Bearer " + signValidToken(t, priv),
			}))
			require.NoError(t, err)
			require.Equal(t, codes.Code(tc.code), codes.Code(resp.GetStatus().GetCode()))
			require.Nil(t, resp.GetOkResponse(), "a failed check is never an allow")
			require.Equal(t, int32(http.StatusServiceUnavailable), int32(resp.GetDeniedResponse().GetStatus().GetCode()),
				"Envoy's denied response keeps 503, the status it can spell")
			require.Equal(t, "revocation check unavailable", resp.GetDeniedResponse().GetBody())
		})
	}
}
