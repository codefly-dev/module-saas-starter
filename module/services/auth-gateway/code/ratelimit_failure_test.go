package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type failingRateLimitBackend struct{}

func (failingRateLimitBackend) Increment(string, time.Duration) (int64, error) {
	return 0, errors.New("redis unavailable")
}

func (failingRateLimitBackend) Close() error { return nil }

func TestLimiterBackendFailureModes(t *testing.T) {
	limiter := &RateLimiter{backend: failingRateLimitBackend{}, limit: 100, burst: 20, stop: make(chan struct{})}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	for _, test := range []struct {
		name   string
		mode   limiterFailureMode
		status int
	}{
		{name: "security route fails closed", mode: limiterFailClosed, status: http.StatusServiceUnavailable},
		{name: "availability route fails open", mode: limiterFailOpen, status: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.RemoteAddr = "198.51.100.10:1234"
			w := httptest.NewRecorder()
			limiter.Middleware(test.mode, false, next).ServeHTTP(w, req)
			require.Equal(t, test.status, w.Code)
		})
	}
}

func TestLimiterFailureModeClassification(t *testing.T) {
	require.Equal(t, limiterFailClosed, limiterFailureModeFor(nil))
	require.Equal(t, limiterFailClosed, limiterFailureModeFor(&RouteEntry{RateLimitBackendFailClosed: true}))
	require.Equal(t, limiterFailOpen, limiterFailureModeFor(&RouteEntry{RateLimitBackendFailClosed: false}))
}

func TestMFACompletionHasDedicatedPerIPBudget(t *testing.T) {
	limiter := &RateLimiter{
		backend: &memoryBackend{},
		limit:   1000,
		burst:   200,
		stop:    make(chan struct{}),
		proxies: newProxyTrust(""),
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := limiter.Middleware(limiterFailClosed, true, next)

	for attempt := 1; attempt <= authenticationAttemptLimitPerMinute; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/mfa/complete", nil)
		req.RemoteAddr = "198.51.100.10:1234"
		// A caller cannot evade the budget by inventing tenant headers.
		req.Header.Set("X-Org-Id", fmt.Sprintf("spoofed-org-%d", attempt))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusNoContent, w.Code)
		require.Equal(t, "10", w.Header().Get("X-RateLimit-Limit"))
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/mfa/complete", nil)
	req.RemoteAddr = "198.51.100.10:9999"
	req.Header.Set("X-Org-Id", "a-new-spoofed-org")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)

	// The strict factor namespace does not consume the general API budget.
	normal := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	normal.RemoteAddr = "198.51.100.10:1234"
	normalW := httptest.NewRecorder()
	limiter.Middleware(limiterFailOpen, false, next).ServeHTTP(normalW, normal)
	require.Equal(t, http.StatusNoContent, normalW.Code)
}

func TestAuthenticationFactorAttemptComesFromGeneratedPolicy(t *testing.T) {
	for _, procedure := range []string{
		"/saas.accounts.v1.AuthService/CompleteMFAChallenge",
		"/saas.accounts.v1.AuthService/CompleteWebAuthnMFAChallenge",
	} {
		metadata, ok := generatedAuthorizationByProcedure[procedure]
		require.True(t, ok, procedure)
		require.True(t, metadata.authenticationFactorAttempt, procedure)
	}
	for _, procedure := range []string{
		"/saas.accounts.v1.AuthService/Authenticate",
		"/saas.accounts.v1.UserService/GetSelf",
	} {
		metadata, ok := generatedAuthorizationByProcedure[procedure]
		require.True(t, ok, procedure)
		require.False(t, metadata.authenticationFactorAttempt, procedure)
	}
}

func TestConfiguredAuthenticationAttemptLimit(t *testing.T) {
	const configurationKey = "CODEFLY__WORKSPACE_CONFIGURATION__SECURITY__MFA_COMPLETION_RATE_LIMIT_PER_MINUTE"
	const secretKey = "CODEFLY__WORKSPACE_SECRET_CONFIGURATION__SECURITY__MFA_COMPLETION_RATE_LIMIT_PER_MINUTE"
	t.Setenv(configurationKey, "")
	t.Setenv(secretKey, "")
	t.Setenv("MFA_COMPLETION_RATE_LIMIT_PER_MINUTE", "")
	limit, err := configuredAuthenticationAttemptLimit()
	require.NoError(t, err)
	require.Equal(t, 10, limit)

	t.Setenv("MFA_COMPLETION_RATE_LIMIT_PER_MINUTE", "7")
	limit, err = configuredAuthenticationAttemptLimit()
	require.NoError(t, err)
	require.Equal(t, 7, limit)

	for _, invalid := range []string{"0", "1001", "-1", "not-a-number"} {
		t.Setenv("MFA_COMPLETION_RATE_LIMIT_PER_MINUTE", invalid)
		_, err = configuredAuthenticationAttemptLimit()
		require.Error(t, err, invalid)
	}
}
