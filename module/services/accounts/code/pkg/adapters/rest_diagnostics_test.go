package adapters

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRESTFailureDiagnosticsNeverLogRequestBody(t *testing.T) {
	previousEmitter := emitHTTPFailureDiagnostic
	var got httpFailureDiagnostic
	emitHTTPFailureDiagnostic = func(d httpFailureDiagnostic) { got = d }
	t.Cleanup(func() { emitHTTPFailureDiagnostic = previousEmitter })

	secretBody := `{"password":"never-log-me","mfa_code":"123456"}`
	handler := logRequestOutcome(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, secretBody, string(body), "middleware must not consume or rewrite the body")
		http.Error(w, "rejected", http.StatusUnauthorized)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/authenticate?code=oauth-secret", bytes.NewBufferString(secretBody))
	req.Header.Set("X-Request-Id", "request-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, "request-123", w.Header().Get("X-Request-Id"))
	require.Equal(t, "request-123", got.RequestID)
	require.Equal(t, http.MethodPost, got.Method)
	require.Equal(t, "/v1/auth/authenticate", got.Path, "query strings may contain OAuth credentials and must not be logged")
	require.Equal(t, http.StatusUnauthorized, got.StatusCode)
	require.NotContains(t, got.Path, "never-log-me")
	require.NotContains(t, got.Path, "oauth-secret")
}

func TestRESTFailureDiagnosticsRejectUntrustedRequestID(t *testing.T) {
	previousEmitter := emitHTTPFailureDiagnostic
	var got httpFailureDiagnostic
	emitHTTPFailureDiagnostic = func(d httpFailureDiagnostic) { got = d }
	t.Cleanup(func() { emitHTTPFailureDiagnostic = previousEmitter })

	handler := logRequestOutcome(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rejected", http.StatusBadRequest)
	}))
	attackerID := "attacker\nforged-log-line"
	req := httptest.NewRequest(http.MethodGet, "/v1/users/"+strings.Repeat("a", 400), nil)
	req.Header.Set("X-Request-Id", attackerID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.NotEmpty(t, got.RequestID)
	require.NotEqual(t, attackerID, got.RequestID)
	require.LessOrEqual(t, len(got.Path), 256)
	require.Equal(t, "safe-forged", boundedLogValue("safe-\nforged", 256))
}
