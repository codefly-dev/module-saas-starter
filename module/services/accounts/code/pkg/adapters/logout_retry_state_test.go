package adapters

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// downstreamStatus answers with a chosen status, for the incomplete-sign-out path.
func downstreamStatus(status int, resp string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	})
}

// An INCOMPLETE sign-out keeps the refresh cookie.
//
// Logout answers 503 "logout incomplete, retry" when it revoked the refresh
// family but could not record an access-revocation marker. That answer exists so
// the caller can retry — and the retry needs the refresh token, which lives only
// in this cookie. The middleware cleared it on EVERY logout response, so the one
// answer that asks for a retry was also the one that destroyed the credential
// the retry needs, while an unmarked access token stayed valid.
//
// The family revocation runs last in Logout and its error is returned directly,
// so a 5xx here means the durable half did not land either: the session really is
// still live, and keeping the credential is the lesser harm of the two.
func TestIncompleteLogoutKeepsTheRetryCredential(t *testing.T) {
	h := refreshTokenCookie(downstreamStatus(
		http.StatusServiceUnavailable, `{"error":"logout incomplete, retry"}`))

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: refreshTokenCookieName, Value: "cookie-rt"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected the downstream status to pass through, got %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == refreshTokenCookieName && c.MaxAge < 0 {
			t.Fatalf("an incomplete sign-out must not clear the refresh cookie: "+
				"it is the only credential a retry has, got %+v", c)
		}
	}
}

// The control, beside the existing TestRefreshCookie_Logout_ClearsCookie: a
// COMPLETED sign-out still clears it. Without this the test above would pass on
// a middleware that never cleared the cookie at all, which would leave a signed
// -out browser holding a live refresh credential.
func TestCompletedLogoutStillClearsTheCookie(t *testing.T) {
	h := refreshTokenCookie(downstreamStatus(http.StatusOK, `{}`))

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: refreshTokenCookieName, Value: "cookie-rt"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == refreshTokenCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("a completed sign-out must clear the refresh cookie")
	}
}
