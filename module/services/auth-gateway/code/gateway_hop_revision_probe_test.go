package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authorityv1 "github.com/codefly-dev/authority/libraries/authority-client/go/gen/host/authority/v1"
	hop "github.com/codefly-dev/authority/libraries/hop/go"
	"github.com/codefly-dev/authority/libraries/hop/go/hoptest"
	"github.com/codefly-dev/authority/libraries/hop/go/routing"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type revisionProbeIssuer struct {
	mu              sync.Mutex
	revision, calls int
	revoked         bool
}

func (i *revisionProbeIssuer) Issue(_ context.Context, _ *authorityv1.IssueRequest, _ ...grpc.CallOption) (*authorityv1.IssueResponse, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.calls++
	if i.revoked {
		return nil, status.Error(codes.PermissionDenied, "access revoked")
	}
	return &authorityv1.IssueResponse{Token: fmt.Sprintf("revision-%d", i.revision), ExpiresAtUnix: time.Now().Add(5 * time.Minute).Unix()}, nil
}
func (i *revisionProbeIssuer) set(revision int, revoked bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.revision, i.revoked = revision, revoked
}
func (i *revisionProbeIssuer) count() int { i.mu.Lock(); defer i.mu.Unlock(); return i.calls }
func revisionRequest(bearer string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, browserModulePrefix+listCollection, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	return req
}
func revisionCall(gw *Gateway, bearer string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, revisionRequest(bearer))
	return w
}

func TestBrowserDownstreamResponseKeepsCachedContext(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusUnauthorized, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			gw, _, _, bearer := hopBrowserHarness(t)
			issuer := &revisionProbeIssuer{revision: 1}
			gw.hop.issuer = issuer
			calls := 0
			gw.hop.face = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(code) })
			require.Equal(t, code, revisionCall(gw, bearer).Code)
			require.Equal(t, code, revisionCall(gw, bearer).Code)
			require.Equal(t, 2, calls)
			require.Equal(t, 1, issuer.count())
		})
	}
}

type fullWriter struct{ *httptest.ResponseRecorder }

func (*fullWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, fmt.Errorf("unused")
}
func (*fullWriter) ReadFrom(r io.Reader) (int64, error) { return io.Copy(io.Discard, r) }
func TestBrowserFaceKeepsWriterInterfaces(t *testing.T) {
	gw, _, _, bearer := hopBrowserHarness(t)
	gw.hop.face = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, flush := w.(http.Flusher)
		require.True(t, flush)
		_, _, err := http.NewResponseController(w).Hijack()
		require.EqualError(t, err, "unused")
		require.NoError(t, http.NewResponseController(w).Flush())
		w.WriteHeader(http.StatusOK)
	})
	w := &fullWriter{httptest.NewRecorder()}
	gw.ServeHTTP(w, revisionRequest(bearer))
	require.Equal(t, http.StatusOK, w.Code)
}

func TestBrowserIssueFailureIsNotCached(t *testing.T) {
	gw, _, _, bearer := hopBrowserHarness(t)
	issuer := &revisionProbeIssuer{revision: 1, revoked: true}
	gw.hop.issuer = issuer
	calls := 0
	gw.hop.face = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusOK) })
	require.Equal(t, http.StatusForbidden, revisionCall(gw, bearer).Code)
	require.Equal(t, http.StatusForbidden, revisionCall(gw, bearer).Code)
	require.Equal(t, 2, issuer.count())
	require.Zero(t, calls)
	issuer.set(2, false)
	require.Equal(t, http.StatusOK, revisionCall(gw, bearer).Code)
	require.Equal(t, 3, issuer.count())
	require.Equal(t, 1, calls)
}

func TestBrowserStreamingFlushAndTrailerRemainIntact(t *testing.T) {
	gw, _, _, bearer := hopBrowserHarness(t)
	issuer := &revisionProbeIssuer{revision: 1}
	gw.hop.issuer = issuer
	release := make(chan struct{})
	gw.hop.face = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Trailer", "X-Stream-Result")
		_, _ = io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "second")
		w.Header().Set("X-Stream-Result", "complete")
	})
	server := httptest.NewServer(gw)
	defer server.Close()
	call := func() {
		req, err := http.NewRequest(http.MethodPost, server.URL+browserModulePrefix+listCollection, strings.NewReader(`{}`))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := server.Client().Do(req)
		require.NoError(t, err)
		first := make([]byte, len("first"))
		_, err = io.ReadFull(resp.Body, first)
		require.NoError(t, err)
		require.Equal(t, "first", string(first))
		release <- struct{}{}
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "second", string(body))
		require.Equal(t, "complete", resp.Trailer.Get("X-Stream-Result"))
	}
	call()
	call()
	require.Equal(t, 1, issuer.count(), "a successful stream retains its cached context")
}

type streamingProbeAuthority struct {
	*hostAuthority
	mu      sync.Mutex
	issued  []string
	revoked string
	denyAll bool
}

func (a *streamingProbeAuthority) Issue(ctx context.Context, req *authorityv1.IssueRequest, opts ...grpc.CallOption) (*authorityv1.IssueResponse, error) {
	result, err := a.hostAuthority.Issue(ctx, req, opts...)
	if err == nil {
		a.mu.Lock()
		a.issued = append(a.issued, result.GetToken())
		a.mu.Unlock()
	}
	return result, err
}
func (a *streamingProbeAuthority) Check(ctx context.Context, req *authorityv1.CheckRequest, opts ...grpc.CallOption) (*authorityv1.CheckResponse, error) {
	a.mu.Lock()
	revoked := a.denyAll || (req.GetToken() == a.revoked && a.revoked != "")
	a.mu.Unlock()
	if revoked {
		return nil, status.Error(codes.PermissionDenied, "authorization revision superseded")
	}
	return a.hostAuthority.Check(ctx, req, opts...)
}
func TestBrowserRealHopRefusalThenExplicitRecovery(t *testing.T) {
	for _, contentType := range []string{"application/json", "application/connect+json"} {
		t.Run(contentType, func(t *testing.T) {
			t.Setenv("CODEFLY__ENVIRONMENT", "local")
			gw, _, _, bearer := hopBrowserHarness(t)
			authority := &streamingProbeAuthority{hostAuthority: &hostAuthority{signer: hoptest.New(t), t: t}}
			var reached atomic.Int32
			var revokeAtModule atomic.Bool
			module := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached.Add(1)
				if revokeAtModule.Swap(false) {
					authority.mu.Lock()
					authority.revoked = authority.issued[len(authority.issued)-1]
					authority.mu.Unlock()
					http.Error(w, "downstream refusal after Check", http.StatusForbidden)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer module.Close()
			routes := &routing.Routing{Schema: routing.RoutingSchema, Endpoints: []routing.RouteEndpoint{{Audience: "documents", Service: "documents", Endpoint: "connect", Transport: "connect", Address: module.URL, Package: "documents", Version: "0.1.0", Digest: "sha256:d", Methods: []string{listCollection}}}}
			raw, err := json.Marshal(routes)
			require.NoError(t, err)
			role, front, err := newHopRole(t.Context(), authority, workloadCredential, []string{"documents"}, string(raw), routes, "")
			require.NoError(t, err)
			defer front.Close()
			gw.hop = role
			call := func() *httptest.ResponseRecorder {
				req := revisionRequest(bearer)
				req.Header.Set("Content-Type", contentType)
				req.Header.Set("Connect-Protocol-Version", "1")
				w := httptest.NewRecorder()
				gw.ServeHTTP(w, req)
				return w
			}
			require.Equal(t, http.StatusNoContent, call().Code)
			authority.mu.Lock()
			require.Len(t, authority.issued, 1)
			authority.revoked = authority.issued[0]
			oldToken := authority.revoked
			authority.mu.Unlock()
			native := listCollectionRequest(listCollection)
			native.Header.Set(hop.WorkContextKey, oldToken)
			nativeResponse := httptest.NewRecorder()
			gw.hop.face.ServeHTTP(nativeResponse, native)
			require.Equal(t, http.StatusForbidden, nativeResponse.Code)
			require.EqualValues(t, 1, reached.Load(), "native denial does not forward")
			refused := call()
			if contentType == "application/connect+json" {
				require.Equal(t, http.StatusOK, refused.Code)
				require.Greater(t, refused.Body.Len(), 5)
				require.Equal(t, byte(2), refused.Body.Bytes()[0], "Connect end-stream envelope")
			} else {
				require.Equal(t, http.StatusForbidden, refused.Code)
			}
			require.Contains(t, refused.Body.String(), "permission_denied")
			require.Contains(t, refused.Body.String(), "authorization revision superseded")
			require.EqualValues(t, 1, reached.Load(), "refusal is pre-forward and not retried")
			require.Equal(t, http.StatusNoContent, call().Code, "next explicit call must issue current authority")
			require.EqualValues(t, 2, reached.Load())
			revokeAtModule.Store(true)
			require.Equal(t, http.StatusForbidden, call().Code, "downstream refusal is returned")
			delayed := call()
			require.Contains(t, delayed.Body.String(), "permission_denied", "next hop Check discovers stale authority")
			require.EqualValues(t, 3, reached.Load(), "the stale Check is not forwarded")
			require.Equal(t, http.StatusNoContent, call().Code, "a further explicit call remints")
			require.EqualValues(t, 4, reached.Load())
			authority.mu.Lock()
			authority.denyAll = true
			authority.mu.Unlock()
			firstDenied := call()
			secondDenied := call()
			require.Contains(t, firstDenied.Body.String(), "permission_denied")
			require.Contains(t, secondDenied.Body.String(), "permission_denied")
			require.EqualValues(t, 4, reached.Load(), "repeated live denials never forward")
			authority.mu.Lock()
			defer authority.mu.Unlock()
			require.Len(t, authority.issued, 4)
			require.Equal(t, 1, authority.registered, "registration is delegated unchanged")
		})
	}
}

type checkProbe struct {
	hopAuthorityClient
	result *authorityv1.CheckResponse
	err    error
}

func (s checkProbe) Check(_ context.Context, _ *authorityv1.CheckRequest, _ ...grpc.CallOption) (*authorityv1.CheckResponse, error) {
	return s.result, s.err
}

func TestBrowserCheckEvictionIsBoundAndTransparent(t *testing.T) {
	denied := status.Error(codes.PermissionDenied, "method denied")
	unauth := status.Error(codes.Unauthenticated, "revoked")
	unavailable := status.Error(codes.Unavailable, "unavailable")
	advisory := &authorityv1.CheckResponse{Refusal: "policy"}
	success := &authorityv1.CheckResponse{}
	for _, tc := range []struct {
		name                  string
		response              *authorityv1.CheckResponse
		err                   error
		marker                bool
		token, audience, held string
		cancel, evict         bool
	}{
		{"permission denied", nil, denied, true, "old", "documents", "old", false, true},
		{"unauthenticated", nil, unauth, true, "old", "documents", "old", false, true},
		{"advisory refusal", advisory, nil, true, "old", "documents", "old", false, true},
		{"success", success, nil, true, "old", "documents", "old", false, false},
		{"nil success", nil, nil, true, "old", "documents", "old", false, false},
		{"unavailable", nil, unavailable, true, "old", "documents", "old", false, false},
		{"cancelled", nil, denied, true, "old", "documents", "old", true, false},
		{"native without marker", nil, denied, false, "old", "documents", "old", false, false},
		{"different token", nil, denied, true, "other", "documents", "old", false, false},
		{"different audience", nil, denied, true, "old", "tracing", "old", false, false},
		{"newer held token", nil, denied, true, "old", "documents", "new", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := sha256.Sum256([]byte("session\x00documents"))
			other := sha256.Sum256([]byte("other\x00documents"))
			role := &hopRole{held: map[[32]byte]heldWorkContext{key: {token: tc.held}, other: {token: "other-session"}}}
			wrapper := browserCheckAuthority{hopAuthorityClient: checkProbe{result: tc.response, err: tc.err}, role: role}
			ctx := context.Background()
			if tc.marker {
				ctx = context.WithValue(ctx, browserCheckKey{}, browserCheck{key: key, token: "old", audience: "documents"})
			}
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := wrapper.Check(ctx, &authorityv1.CheckRequest{Token: tc.token, Audience: tc.audience})
			require.Same(t, tc.response, got)
			require.Equal(t, tc.err, err)
			_, exists := role.held[key]
			require.Equal(t, !tc.evict, exists)
			require.Equal(t, "other-session", role.held[other].token)
		})
	}
}

type delayedCheck struct {
	hopAuthorityClient
	entered, release chan struct{}
	result           error
}

func (s delayedCheck) Check(ctx context.Context, _ *authorityv1.CheckRequest, _ ...grpc.CallOption) (*authorityv1.CheckResponse, error) {
	close(s.entered)
	select {
	case <-s.release:
		return nil, s.result
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestBrowserCheckLateRefusalCannotEvictReplacement(t *testing.T) {
	key := sha256.Sum256([]byte("session\x00documents"))
	role := &hopRole{held: map[[32]byte]heldWorkContext{key: {token: "old"}}}
	stub := delayedCheck{entered: make(chan struct{}), release: make(chan struct{}), result: status.Error(codes.PermissionDenied, "denied")}
	wrapper := browserCheckAuthority{hopAuthorityClient: stub, role: role}
	ctx := context.WithValue(context.Background(), browserCheckKey{}, browserCheck{key: key, token: "old", audience: "documents"})
	done := make(chan error, 1)
	go func() {
		_, err := wrapper.Check(ctx, &authorityv1.CheckRequest{Token: "old", Audience: "documents"})
		done <- err
	}()
	<-stub.entered
	role.mu.Lock()
	role.held[key] = heldWorkContext{token: "new"}
	role.mu.Unlock()
	close(stub.release)
	require.Equal(t, stub.result, <-done)
	role.mu.Lock()
	defer role.mu.Unlock()
	require.Equal(t, "new", role.held[key].token)
}
func TestBrowserCheckCancellationIsPropagatedAndRetainsCache(t *testing.T) {
	key := sha256.Sum256([]byte("session\x00documents"))
	role := &hopRole{held: map[[32]byte]heldWorkContext{key: {token: "old"}}}
	stub := delayedCheck{entered: make(chan struct{}), release: make(chan struct{})}
	wrapper := browserCheckAuthority{hopAuthorityClient: stub, role: role}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), browserCheckKey{}, browserCheck{key: key, token: "old", audience: "documents"}))
	done := make(chan error, 1)
	go func() {
		_, err := wrapper.Check(ctx, &authorityv1.CheckRequest{Token: "old", Audience: "documents"})
		done <- err
	}()
	<-stub.entered
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	role.mu.Lock()
	defer role.mu.Unlock()
	require.Equal(t, "old", role.held[key].token)
}

type delayedProbeIssuer struct {
	mu                         sync.Mutex
	calls                      int
	firstStarted, releaseFirst chan struct{}
}

func (i *delayedProbeIssuer) Issue(_ context.Context, _ *authorityv1.IssueRequest, _ ...grpc.CallOption) (*authorityv1.IssueResponse, error) {
	i.mu.Lock()
	i.calls++
	n := i.calls
	i.mu.Unlock()
	if n == 1 {
		close(i.firstStarted)
		<-i.releaseFirst
	}
	revision := 2
	if n == 1 {
		revision = 1
	}
	return &authorityv1.IssueResponse{Token: fmt.Sprintf("revision-%d", revision), ExpiresAtUnix: time.Now().Add(5 * time.Minute).Unix()}, nil
}
func TestBrowserDelayedIssueCanRequireAnotherExplicitRetry(t *testing.T) {
	issuer := &delayedProbeIssuer{firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	role := &hopRole{issuer: issuer}
	old := make(chan string, 1)
	go func() { token, _ := role.workContext(context.Background(), "session", "documents"); old <- token }()
	<-issuer.firstStarted
	fresh, err := role.workContext(context.Background(), "session", "documents")
	require.NoError(t, err)
	require.Equal(t, "revision-2", fresh)
	close(issuer.releaseFirst)
	require.Equal(t, "revision-1", <-old)
	stale, err := role.workContext(context.Background(), "session", "documents")
	require.NoError(t, err)
	require.Equal(t, "revision-1", stale)
	key := sha256.Sum256([]byte("session\x00documents"))
	ctx := context.WithValue(context.Background(), browserCheckKey{}, browserCheck{key: key, token: stale, audience: "documents"})
	wrapper := browserCheckAuthority{hopAuthorityClient: checkProbe{err: status.Error(codes.PermissionDenied, "superseded")}, role: role}
	_, err = wrapper.Check(ctx, &authorityv1.CheckRequest{Token: stale, Audience: "documents"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	fresh, err = role.workContext(context.Background(), "session", "documents")
	require.NoError(t, err)
	require.Equal(t, "revision-2", fresh)
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	require.Equal(t, 3, issuer.calls)
}
