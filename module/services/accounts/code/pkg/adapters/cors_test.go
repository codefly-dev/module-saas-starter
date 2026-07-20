package adapters

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCredentialedCORSUsesExactConfiguredOrigins(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com,http://localhost:3000")
	require.Equal(t, []string{"https://app.example.com", "http://localhost:3000"}, configuredCORSOrigins())
	handler := Cors().Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, test := range []struct {
		name    string
		origin  string
		allowed bool
	}{
		{name: "configured production origin", origin: "https://app.example.com", allowed: true},
		{name: "configured loopback origin", origin: "http://localhost:3000", allowed: true},
		{name: "subdomain is not equivalent", origin: "https://evil.app.example.com"},
		{name: "different loopback port is not equivalent", origin: "http://localhost:3001"},
		{name: "origin suffix attack", origin: "https://app.example.com.evil.test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/saas.accounts.v1.UserService/GetSelf", nil)
			req.Header.Set("Origin", test.origin)
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,idempotency-key")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if test.allowed {
				require.Equal(t, test.origin, w.Header().Get("Access-Control-Allow-Origin"))
				require.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
				require.Contains(t, strings.ToLower(w.Header().Get("Access-Control-Allow-Headers")), "idempotency-key")
			} else {
				require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
}

func TestCredentialedCORSRejectsWildcardAndMalformedConfiguration(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "*,https://*.example.com,https://user@example.com,https://app.example.com/path,null")
	require.Empty(t, configuredCORSOrigins())
}

func TestCredentialedCORSDevelopmentDefaultsAreExact(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	require.Equal(t, defaultAllowedOrigins, configuredCORSOrigins())
	require.NotContains(t, configuredCORSOrigins(), "http://localhost:*")
}
