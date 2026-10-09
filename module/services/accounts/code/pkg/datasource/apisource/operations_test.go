package apisource

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("client_id") != "example" || r.PostForm.Get("client_secret") != "secret" || r.PostForm.Get("scope") != "read write" || r.PostForm.Has("refresh_token") {
			t.Error("wrong client credentials request")
		}
		_, _ = io.WriteString(w, `{"access_token":"access","expires_in":3600}`)
	}))
	defer srv.Close()
	tok, err := (&oauth2Refresher{http: srv.Client()}).clientCredentials(t.Context(), OAuth2Config{TokenURL: srv.URL, ClientID: "example", Scopes: []string{"read", "write"}}, "secret")
	if err != nil || tok.AccessToken != "access" || tok.ExpiresIn != time.Hour {
		t.Fatalf("exchange: %v", err)
	}
}

func TestClientCredentialsTerminalAndGuards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	_, err := (&oauth2Refresher{http: srv.Client()}).clientCredentials(t.Context(), OAuth2Config{TokenURL: srv.URL}, "secret")
	if !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("401 must be terminal: %v", err)
	}
	_, err = ClientCredentials(t.Context(), OAuth2Config{TokenURL: srv.URL}, "example", "secret")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("token endpoint must use guard: %v", err)
	}
	var reached bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) }))
	defer redirect.Close()
	_, err = (&oauth2Refresher{http: &http.Client{CheckRedirect: checkRedirect}}).clientCredentials(t.Context(), OAuth2Config{TokenURL: redirect.URL}, "secret")
	if err == nil || reached {
		t.Fatal("cross-host redirect sent credentials")
	}
}

func TestDoMethodsBoundsAndRateLimit(t *testing.T) {
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			var body []byte
			if method != "GET" && method != "DELETE" {
				body = []byte(`{"value":1}`)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, _ := io.ReadAll(r.Body)
				if r.Method != method || string(got) != string(body) || r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("request mismatch")
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer srv.Close()
			c := newTestClient(Config{BaseURL: srv.URL, CredentialKind: CredentialKindBearer, MaxOutputBytes: 11}, "secret")
			got, err := c.Do(t.Context(), method, srv.URL, body)
			if err != nil || got.StatusCode != 200 {
				t.Fatalf("do: %v", err)
			}
			c.cfg.MaxOutputBytes = 10
			if _, err := c.Do(t.Context(), method, srv.URL, body); err == nil {
				t.Fatal("unbounded output")
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "120"); w.WriteHeader(429) }))
	defer srv.Close()
	c := newTestClient(Config{BaseURL: srv.URL, CredentialKind: CredentialKindBearer}, "secret")
	_, err := c.Do(t.Context(), "GET", srv.URL, nil)
	var fail *Failure
	if !errors.As(err, &fail) || fail.ProviderStatus != 429 || fail.RetryAfter != 2*time.Minute {
		t.Fatalf("rate limit: %v", err)
	}
	if _, err := c.Do(t.Context(), "GET", srv.URL, []byte(`{}`)); err == nil {
		t.Fatal("GET body admitted")
	}
}

// Transport-side disclosure coverage. The business suite additionally inspects
// the sealed envelope, receipt, audit and captured log sinks.
func TestNoCredentialByteSequenceLeaks(t *testing.T) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	for _, mode := range []string{"bad_url", "401", "429", "redirect", "timeout", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				switch mode {
				case "401":
					w.WriteHeader(401)
				case "429":
					w.WriteHeader(429)
				case "redirect":
					http.Redirect(w, r, "http://example.invalid/"+secret, 307)
					return
				case "timeout":
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, secret)
			}))
			defer srv.Close()
			target := srv.URL + "/?credential=" + secret
			if mode == "bad_url" {
				target = "://" + secret
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			_, err := (&oauth2Refresher{http: &http.Client{CheckRedirect: checkRedirect}}).refresh(ctx, OAuth2Config{TokenURL: target}, secret, secret)
			if err == nil {
				t.Fatal("expected refusal")
			}
			for i := 0; i+16 <= len(secret); i++ {
				if strings.Contains(err.Error(), secret[i:i+16]) {
					t.Fatal("credential disclosed")
				}
			}
		})
	}
}

func TestDoRefusesCredentialReflectedByProvider(t *testing.T) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	credential := base64.RawURLEncoding.EncodeToString(raw)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"reflected":"`+credential+`"}`)
	}))
	defer srv.Close()
	client := New(Config{BaseURL: srv.URL, CredentialKind: CredentialKindBearer}, credential)
	client.http = srv.Client()
	_, err := client.Do(t.Context(), http.MethodGet, srv.URL, nil)
	var failure *Failure
	if !errors.As(err, &failure) || !failure.OutputRefused || failure.ProviderStatus != 200 {
		t.Fatalf("credential reflection not refused: %v", err)
	}
	for i := 0; i+16 <= len(credential); i++ {
		if strings.Contains(err.Error(), credential[i:i+16]) {
			t.Fatal("refusal disclosed credential")
		}
	}
}

func TestNoCredentialLeaksThroughJSONEscapesOrBasicEncoding(t *testing.T) {
	secret := "user:" + strings.Repeat("aB7k", 12)
	for _, kind := range []string{CredentialKindBearer, CredentialKindBasic} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				value := secret
				if kind == CredentialKindBasic {
					value = r.Header.Get("Authorization")
				}
				var escaped strings.Builder
				for _, r := range value {
					fmt.Fprintf(&escaped, `\u%04x`, r)
				}
				_, _ = fmt.Fprintf(w, `{"nested":[{"%s":"%s"}]}`, escaped.String(), escaped.String())
			}))
			defer server.Close()
			client := New(Config{BaseURL: server.URL, CredentialKind: kind}, secret)
			client.http = server.Client()
			out, err := client.Do(t.Context(), http.MethodGet, server.URL, nil)
			var refused *Failure
			if out != nil || !errors.As(err, &refused) || !refused.OutputRefused {
				t.Fatal("encoded credential reflection escaped the refusal")
			}
		})
	}
}

// HTTP method names cannot authorize transport retries: the declaration owns
// the effect, and even GET may represent a provider mutation.
func TestDoDoesNotReplayLostReplyOnReusedClient(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL, CredentialKind: CredentialKindBearer}, "example-token")
	// Only replace resolved-IP dialing to reach the local fixture. Retain every
	// other production transport option, especially connection/retry behavior.
	client.http.Transport.(*http.Transport).DialContext = (&net.Dialer{}).DialContext
	if _, err := client.Do(t.Context(), http.MethodGet, server.URL, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(t.Context(), http.MethodGet, server.URL, nil); err == nil {
		t.Fatal("lost reply must be reported")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("provider received %d calls; want 2", got)
	}
}
