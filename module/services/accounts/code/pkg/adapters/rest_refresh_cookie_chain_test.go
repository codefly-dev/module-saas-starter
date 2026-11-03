package adapters

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/gen/saas/accounts/v1/accountsv1connect"
)

// chainAuthHandler serves the three cookie-carrying AuthService methods exactly
// as authConnectHandler does — through the production lift and cookie readers —
// with the business layer replaced by fixed answers.
type chainAuthHandler struct {
	accountsv1connect.UnimplementedAuthServiceHandler
	mu       sync.Mutex
	received map[string]string // method -> refresh token the handler saw
}

func (h *chainAuthHandler) saw(method, token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.received[method] = token
}

func (h *chainAuthHandler) Authenticate(ctx context.Context, req *connect.Request[gen.AuthenticateRequest]) (*connect.Response[gen.AuthenticateResponse], error) {
	return unaryCookieingRefreshToken(ctx, req, func(context.Context, *gen.AuthenticateRequest) (*gen.AuthenticateResponse, error) {
		return &gen.AuthenticateResponse{AccessToken: "access-1", RefreshToken: "refresh-1"}, nil
	})
}

func (h *chainAuthHandler) RefreshToken(ctx context.Context, req *connect.Request[gen.RefreshTokenRequest]) (*connect.Response[gen.RefreshTokenResponse], error) {
	refreshTokenFromCookie(req.Header(), req.Msg.GetRefreshToken(), func(token string) { req.Msg.RefreshToken = token })
	return unaryCookieingRefreshToken(ctx, req, func(_ context.Context, msg *gen.RefreshTokenRequest) (*gen.RefreshTokenResponse, error) {
		h.saw("refresh", msg.GetRefreshToken())
		return &gen.RefreshTokenResponse{AccessToken: "access-2", RefreshToken: "refresh-2"}, nil
	})
}

func (h *chainAuthHandler) Logout(ctx context.Context, req *connect.Request[gen.LogoutRequest]) (*connect.Response[emptypb.Empty], error) {
	refreshTokenFromCookie(req.Header(), req.Msg.GetRefreshToken(), func(token string) { req.Msg.RefreshToken = token })
	return unary(ctx, req, func(_ context.Context, msg *gen.LogoutRequest) (*emptypb.Empty, error) {
		h.saw("logout", msg.GetRefreshToken())
		return &emptypb.Empty{}, nil
	})
}

// TestABrowserSessionSurvivesTheRealRESTChain drives the whole path a browser's
// sign-in takes: REST -> the cookie middleware -> grpc-gateway (with the mux
// options rest_gen.go builds) -> gRPC over h2c -> the Connect AuthService with
// the production cookie lift. #1019 shipped a REST sign-in whose cookie never
// reached the browser because each half was tested alone; this is the joined
// test. It asserts, for authenticate, refresh and logout: exactly one Set-Cookie
// for the refresh credential, no token in any body, nothing prefixed, and that
// the cookie a browser would send back is what the handler receives.
func TestABrowserSessionSurvivesTheRealRESTChain(t *testing.T) {
	handler := &chainAuthHandler{received: map[string]string{}}
	connectMux := http.NewServeMux()
	connectMux.Handle(accountsv1connect.NewAuthServiceHandler(handler))
	backend := httptest.NewServer(h2c.NewHandler(connectMux, &http2.Server{}))
	t.Cleanup(backend.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	gwMux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(restIdentityHeaderMatcher),
		runtime.WithOutgoingHeaderMatcher(restOutgoingHeaderMatcher),
	)
	endpoint := strings.TrimPrefix(backend.URL, "http://")
	if err := gen.RegisterAuthServiceHandlerFromEndpoint(ctx, gwMux, endpoint,
		[]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}); err != nil {
		t.Fatal(err)
	}
	rest := httptest.NewServer(refreshTokenCookie(gwMux))
	t.Cleanup(rest.Close)

	post := func(path string, cookie *http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, rest.URL+path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	onlyCookie := func(path string, resp *http.Response) *http.Cookie {
		t.Helper()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s: HTTP %d %s", path, resp.StatusCode, body)
		}
		for name := range resp.Header {
			if strings.HasPrefix(strings.ToLower(name), strings.ToLower(runtime.MetadataHeaderPrefix)) &&
				strings.Contains(strings.ToLower(name), "cookie") {
				t.Fatalf("%s: a cookie left as %s, readable by page script", path, name)
			}
		}
		cookies := resp.Cookies()
		if len(cookies) != 1 || cookies[0].Name != refreshTokenCookieName {
			t.Fatalf("%s: want exactly one %s cookie, got %v", path, refreshTokenCookieName, cookies)
		}
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		for _, key := range []string{"refreshToken", "refresh_token"} {
			if v, ok := body[key].(string); ok && v != "" {
				t.Fatalf("%s: the refresh credential is readable in the body", path)
			}
		}
		return cookies[0]
	}

	signedIn := onlyCookie("/v1/auth/authenticate", post("/v1/auth/authenticate", nil))
	if signedIn.Value != "refresh-1" || !signedIn.HttpOnly || signedIn.SameSite != http.SameSiteStrictMode || signedIn.Path != "/v1/auth" {
		t.Fatalf("authenticate cookie = %+v", signedIn)
	}
	rotated := onlyCookie("/v1/auth/refresh", post("/v1/auth/refresh", signedIn))
	if rotated.Value != "refresh-2" {
		t.Fatalf("refresh did not rotate the cookie: %+v", rotated)
	}
	cleared := onlyCookie("/v1/auth/logout", post("/v1/auth/logout", rotated))
	if cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("logout did not clear the cookie: %+v", cleared)
	}
	if handler.received["refresh"] != "refresh-1" || handler.received["logout"] != "refresh-2" {
		t.Fatalf("the cookie a browser sends back did not reach the handler: %v", handler.received)
	}
}
