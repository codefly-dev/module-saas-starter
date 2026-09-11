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
		code int
		want error
	}{{401, ErrUnauthorized}, {403, ErrForbidden}, {429, ErrForbidden}, {404, ErrNotFound}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
