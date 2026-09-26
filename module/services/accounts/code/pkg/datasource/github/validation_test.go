package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		_, err := New("private-token", server.URL).RepositoryIsPublic(context.Background(), "acme/docs")
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
	_, err := New("private-token", server.URL).RepositoryIsPublic(context.Background(), "acme/docs")
	if !errors.Is(err, ErrRateLimited) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("rate limit misclassified or exposed: %v", err)
	}
}

// Public is an affirmative answer only. GitHub answers an unauthenticated read
// of a private or missing repository with the same 404, and a response that does
// not say "public" must never be read as one.
func TestRepositoryIsPublic(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   bool
		err    error
	}{
		"public":                    {http.StatusOK, `{"private":false,"visibility":"public"}`, true, nil},
		"public without visibility": {http.StatusOK, `{"private":false}`, true, nil},
		"private":                   {http.StatusOK, `{"private":true,"visibility":"private"}`, false, nil},
		"internal":                  {http.StatusOK, `{"private":false,"visibility":"internal"}`, false, nil},
		"flag absent":               {http.StatusOK, `{"visibility":"public"}`, false, nil},
		"private or missing":        {http.StatusNotFound, `{"message":"Not Found"}`, false, ErrNotFound},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/acme/docs" {
					t.Errorf("unexpected path %q", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "" {
					t.Errorf("an unauthenticated client sent Authorization %q", got)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := New("", srv.URL).RepositoryIsPublic(context.Background(), "acme/docs")
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("public = %v, want %v", got, tc.want)
			}
		})
	}
}

// The unauthenticated limit is metered per IP rather than per credential, so it
// is reported distinctly — but still as a rate limit to a caller asking only
// that. The limit's reset time is read from the response, so a caller can back
// off until it rather than retry blindly.
func TestUnauthenticatedRateLimitIsDistinct(t *testing.T) {
	reset := time.Now().Add(17 * time.Minute).Truncate(time.Second).UTC()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconvItoa(reset.Unix()))
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for 203.0.113.7."}`))
	}))
	defer srv.Close()

	_, err := New("", srv.URL).RepositoryIsPublic(context.Background(), "acme/docs")
	if !errors.Is(err, ErrUnauthenticatedRateLimited) || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("unauthenticated read err = %v, want ErrUnauthenticatedRateLimited wrapping ErrRateLimited", err)
	}
	var limited *RateLimitError
	if !errors.As(err, &limited) || !limited.ResetAt.Equal(reset) {
		t.Fatalf("rate limit reset = %v, want %v", limited, reset)
	}
	_, err = New("tok", srv.URL).RepositoryIsPublic(context.Background(), "acme/docs")
	if !errors.Is(err, ErrRateLimited) || errors.Is(err, ErrUnauthenticatedRateLimited) {
		t.Fatalf("authenticated read err = %v, want ErrRateLimited only", err)
	}
}

func TestRateLimitResetPrefersRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	h := http.Header{}
	h.Set("Retry-After", "90")
	h.Set("X-RateLimit-Reset", "1700009999")
	if got := rateLimitReset(h, now); !got.Equal(now.Add(90 * time.Second)) {
		t.Fatalf("reset = %v, want Retry-After", got)
	}
	h.Del("Retry-After")
	if got := rateLimitReset(h, now); !got.Equal(time.Unix(1700009999, 0).UTC()) {
		t.Fatalf("reset = %v, want X-RateLimit-Reset", got)
	}
	if got := rateLimitReset(http.Header{}, now); !got.IsZero() {
		t.Fatalf("reset = %v, want zero when GitHub does not say", got)
	}
}
