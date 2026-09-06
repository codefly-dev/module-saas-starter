package apisource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
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

func TestFetch_AppliesQueryCredential(t *testing.T) {
	var gotKey, gotPage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("api_key")
		gotPage = r.URL.Query().Get("page")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	cfg := Config{
		BaseURL:              srv.URL,
		ResourcePath:         "/v1/items?page=2",
		CredentialKind:       CredentialKindQuery,
		CredentialQueryParam: "api_key",
	}
	if _, err := newTestClient(cfg, "tok").Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotKey != "tok" {
		t.Fatalf("api_key=%q, want tok", gotKey)
	}
	// The credential is added without clobbering an existing query parameter.
	if gotPage != "2" {
		t.Fatalf("page=%q, want the resource path's own query preserved", gotPage)
	}
}

func TestFetch_QueryKindRequiresParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	_, err := newTestClient(Config{BaseURL: srv.URL, CredentialKind: CredentialKindQuery}, "tok").Fetch(context.Background())
	if err == nil {
		t.Fatal("want error when query credential kind has no parameter name")
	}
}

func TestRefreshOAuth2(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt2","expires_in":3600}`))
	}))
	defer srv.Close()

	tok, err := (&oauth2Refresher{http: &http.Client{}}).refresh(context.Background(),
		OAuth2Config{TokenURL: srv.URL, ClientID: "cid", Scopes: []string{"read", "write"}}, "rt", "csecret")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt2" || tok.ExpiresIn != time.Hour {
		t.Fatalf("token = %+v, want at/rt2/1h", tok)
	}
	if gotForm.Get("grant_type") != "refresh_token" || gotForm.Get("refresh_token") != "rt" ||
		gotForm.Get("client_id") != "cid" || gotForm.Get("client_secret") != "csecret" ||
		gotForm.Get("scope") != "read write" {
		t.Fatalf("form = %v", gotForm)
	}
}

func TestCheckRedirect(t *testing.T) {
	orig := httptest.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)

	sameHost := httptest.NewRequest(http.MethodGet, "https://api.example.com/v2", nil)
	if err := checkRedirect(sameHost, []*http.Request{orig}); err != nil {
		t.Fatalf("same-host redirect must be allowed: %v", err)
	}

	// A query-string/custom-header credential (and the OAuth POST body) would be
	// resent to a host the tenant did not name — net/http only strips
	// Authorization on cross-host redirects — so a cross-host hop is refused.
	crossHost := httptest.NewRequest(http.MethodGet, "https://evil.example.net/v1", nil)
	if err := checkRedirect(crossHost, []*http.Request{orig}); err == nil {
		t.Fatal("cross-host redirect must be blocked")
	}

	if err := checkRedirect(sameHost, []*http.Request{orig, orig, orig}); err == nil {
		t.Fatal("redirect chain must be bounded")
	}
}

func TestRefreshOAuth2_RejectedIsTerminal(t *testing.T) {
	rejected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer rejected.Close()
	_, err := (&oauth2Refresher{http: &http.Client{}}).refresh(context.Background(),
		OAuth2Config{TokenURL: rejected.URL}, "rt", "")
	if !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("invalid_grant must be terminal (ErrRefreshRejected), got %v", err)
	}

	// A 5xx (or a 400 without a terminal OAuth error code) stays transient so the
	// job framework retries past a blip.
	transient := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer transient.Close()
	_, err = (&oauth2Refresher{http: &http.Client{}}).refresh(context.Background(),
		OAuth2Config{TokenURL: transient.URL}, "rt", "")
	if err == nil || errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("5xx must stay retryable, got %v", err)
	}

	rateLimited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"slow_down"}`))
	}))
	defer rateLimited.Close()
	_, err = (&oauth2Refresher{http: &http.Client{}}).refresh(context.Background(),
		OAuth2Config{TokenURL: rateLimited.URL}, "rt", "")
	if err == nil || errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("a non-terminal 400 error code must stay retryable, got %v", err)
	}
}

func TestRefreshOAuth2_Errors(t *testing.T) {
	non2xx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer non2xx.Close()
	if _, err := (&oauth2Refresher{http: &http.Client{}}).refresh(context.Background(),
		OAuth2Config{TokenURL: non2xx.URL}, "rt", ""); err == nil {
		t.Fatal("want error on non-2xx token response")
	}

	noToken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"expires_in":60}`))
	}))
	defer noToken.Close()
	if _, err := (&oauth2Refresher{http: &http.Client{}}).refresh(context.Background(),
		OAuth2Config{TokenURL: noToken.URL}, "rt", ""); err == nil {
		t.Fatal("want error when token response has no access_token")
	}

	if _, err := (&oauth2Refresher{http: &http.Client{}}).refresh(context.Background(),
		OAuth2Config{TokenURL: ""}, "rt", ""); err == nil {
		t.Fatal("want error when token url is empty")
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
