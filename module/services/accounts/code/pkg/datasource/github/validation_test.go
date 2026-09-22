package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidationStatusDoesNotExposeResponseBody(t *testing.T) {
	for _, tt := range []struct {
		code          int
		want          error
		header, value string
	}{{401, ErrUnauthorized, "", ""}, {403, ErrForbidden, "", ""}, {429, ErrRateLimited, "", ""}, {403, ErrRateLimited, "Retry-After", "60"}, {403, ErrRateLimited, "X-RateLimit-Remaining", "0"}, {404, ErrNotFound, "", ""}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tt.header != "" {
				w.Header().Set(tt.header, tt.value)
			}
			w.WriteHeader(tt.code)
			_, _ = w.Write([]byte("sensitive provider detail"))
		}))
		_, err := New("private-token", server.URL).DefaultBranch(context.Background(), "acme/docs")
		server.Close()
		if !errors.Is(err, tt.want) {
			t.Fatalf("status %d: %v", tt.code, err)
		}
		if strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "private-token") {
			t.Fatal("credential or provider detail leaked")
		}
	}
}

func TestSecondaryRateLimitWithoutRetryHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit. sensitive detail"}`))
	}))
	defer server.Close()
	_, err := New("private-token", server.URL).ResolveCommit(context.Background(), "acme/docs", "main")
	if !errors.Is(err, ErrRateLimited) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("rate limit misclassified or exposed: %v", err)
	}
}
