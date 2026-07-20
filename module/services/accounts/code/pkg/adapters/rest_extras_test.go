package adapters

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// downstream echoes the request body it received and returns a fixed auth-style
// JSON response, so the cookie middleware's request-injection and response-
// rewriting can both be observed.
func downstream(resp string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo-Body", string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, resp)
	})
}

func TestRefreshCookie_AuthResponse_StripsTokenAndSetsCookie(t *testing.T) {
	h := refreshTokenCookie(downstream(`{"accessToken":"at","refreshToken":"rt-secret"}`))

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/authenticate", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Refresh token must be stripped from the body.
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response not JSON: %v (%s)", err, rec.Body.String())
	}
	if _, present := got["refreshToken"]; present {
		t.Fatalf("refreshToken should be stripped from the body, got %s", rec.Body.String())
	}
	if got["accessToken"] != "at" {
		t.Fatalf("accessToken should survive, got %v", got["accessToken"])
	}

	// Refresh token must be set as an httpOnly cookie.
	cookies := rec.Result().Cookies()
	var rt *http.Cookie
	for _, c := range cookies {
		if c.Name == refreshTokenCookieName {
			rt = c
		}
	}
	if rt == nil {
		t.Fatal("refresh-token cookie was not set")
	}
	if rt.Value != "rt-secret" {
		t.Fatalf("cookie value = %q, want rt-secret", rt.Value)
	}
	if !rt.HttpOnly {
		t.Fatal("refresh cookie must be HttpOnly")
	}
	if !rt.Secure {
		t.Fatal("refresh cookie must always be Secure")
	}
	if rt.SameSite != http.SameSiteStrictMode {
		t.Fatal("refresh cookie must be SameSite=Strict")
	}
}

func TestRefreshCookie_MFACompletionResponse_StripsTokenAndSetsCookie(t *testing.T) {
	h := refreshTokenCookie(downstream(`{"accessToken":"at","refreshToken":"rt-after-mfa"}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/mfa/complete", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "rt-after-mfa") {
		t.Fatalf("MFA completion exposed refresh token: %s", rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == refreshTokenCookieName && cookie.Value == "rt-after-mfa" && cookie.HttpOnly && cookie.Secure {
			return
		}
	}
	t.Fatal("MFA completion did not move refresh token into the secure httpOnly cookie")
}

func TestRefreshCookie_DoesNotTrustForwardedProto(t *testing.T) {
	h := refreshTokenCookie(downstream(`{"accessToken":"at","refreshToken":"rt-secret"}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/authenticate", strings.NewReader(`{}`))
	req.Header.Set("X-Forwarded-Proto", "http")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == refreshTokenCookieName && !cookie.Secure {
			t.Fatal("caller-controlled X-Forwarded-Proto must not disable Secure cookies")
		}
	}
}

func TestRefreshCookie_RefreshRequest_InjectsCookieIntoBody(t *testing.T) {
	h := refreshTokenCookie(downstream(`{"accessToken":"at2","refreshToken":"rt2"}`))

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: refreshTokenCookieName, Value: "cookie-rt"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// The downstream handler must have received the cookie's token in the body.
	echo := rec.Header().Get("X-Echo-Body")
	if !strings.Contains(echo, "cookie-rt") {
		t.Fatalf("refresh token from cookie not injected into request body: %q", echo)
	}
	if strings.Contains(echo, "refreshToken") {
		t.Fatalf("request must not contain both proto JSON aliases: %q", echo)
	}
}

func TestRefreshCookie_NonAuthPath_Untouched(t *testing.T) {
	h := refreshTokenCookie(downstream(`{"refreshToken":"should-stay"}`))

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "should-stay") {
		t.Fatalf("non-auth path must pass through untouched, got %s", rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("non-auth path must not set cookies")
	}
}

func TestRefreshCookie_Logout_ClearsCookie(t *testing.T) {
	h := refreshTokenCookie(downstream(`{}`))

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var rt *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == refreshTokenCookieName {
			rt = c
		}
	}
	if rt == nil || rt.MaxAge >= 0 {
		t.Fatalf("logout must clear the refresh cookie (MaxAge<0), got %+v", rt)
	}
}
