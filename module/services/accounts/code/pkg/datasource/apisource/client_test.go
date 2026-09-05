package apisource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuardDial_BlocksNonPublicAddresses(t *testing.T) {
	blocked := []string{
		"127.0.0.1:80",       // loopback
		"[::1]:443",          // loopback v6
		"10.1.2.3:80",        // private
		"192.168.0.1:80",     // private
		"172.16.0.1:80",      // private
		"169.254.169.254:80", // link-local (cloud metadata endpoint)
		"0.0.0.0:80",         // unspecified
		"not-an-ip:80",       // unresolved
	}
	for _, addr := range blocked {
		if err := guardDial("tcp", addr, nil); err == nil {
			t.Errorf("guardDial(%q) = nil, want blocked", addr)
		}
	}
	allowed := []string{"8.8.8.8:443", "1.1.1.1:80", "[2606:4700:4700::1111]:443"}
	for _, addr := range allowed {
		if err := guardDial("tcp", addr, nil); err != nil {
			t.Errorf("guardDial(%q) = %v, want allowed", addr, err)
		}
	}
}

// newTestClient builds a Client whose transport is not the SSRF-guarded one, so
// a test can reach an httptest server on loopback (which the guard blocks by
// design). The credential application and response handling under test are
// independent of the dialer.
func newTestClient(cfg Config, credential string) *Client {
	return &Client{cfg: cfg, credential: credential, http: &http.Client{}}
}

func TestFetch_AppliesCredential(t *testing.T) {
	cases := map[string]struct {
		cfg    Config
		want   string // expected value of the header named in wantHeader
		header string
	}{
		"bearer": {cfg: Config{CredentialKind: CredentialKindBearer}, header: "Authorization", want: "Bearer tok"},
		"basic":  {cfg: Config{CredentialKind: CredentialKindBasic}, header: "Authorization", want: "Basic tok"},
		"header": {cfg: Config{CredentialKind: CredentialKindHeader, CredentialHeader: "X-Api-Key"}, header: "X-Api-Key", want: "tok"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get(tc.header)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer srv.Close()
			cfg := tc.cfg
			cfg.BaseURL = srv.URL
			cfg.ResourcePath = "/v1/items"

			result, err := newTestClient(cfg, "tok").Fetch(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("sent %s=%q, want %q", tc.header, got, tc.want)
			}
			if string(result.Body) != `{"ok":true}` || result.ContentType != "application/json" {
				t.Fatalf("body=%q type=%q", result.Body, result.ContentType)
			}
		})
	}
}

func TestFetch_RejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := newTestClient(Config{BaseURL: srv.URL, CredentialKind: CredentialKindBearer}, "tok").Fetch(context.Background())
	if err == nil {
		t.Fatal("want error on non-2xx status")
	}
}

func TestFetch_HeaderKindRequiresName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	_, err := newTestClient(Config{BaseURL: srv.URL, CredentialKind: CredentialKindHeader}, "tok").Fetch(context.Background())
	if err == nil {
		t.Fatal("want error when header credential kind has no header name")
	}
}

func TestResolveURL(t *testing.T) {
	if _, err := resolveURL("ftp://x", ""); err == nil {
		t.Error("want error for non-http scheme")
	}
	if _, err := resolveURL("https://", "/x"); err == nil {
		t.Error("want error for missing host")
	}
	got, err := resolveURL("https://api.example.com/", "/v1/docs")
	if err != nil || got != "https://api.example.com/v1/docs" {
		t.Fatalf("resolveURL = %q, %v", got, err)
	}
}
