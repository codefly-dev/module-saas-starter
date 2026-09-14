package executioncustody

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig.MinVersion = tls.VersionTLS13
	client, err := NewClient(server.URL, transport)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func unavailable(t *testing.T, err error) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != "Unavailable" || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("unsafe or unclassified failure", err)
	}
}

func TestClientRejectsUnsafeTransport(t *testing.T) {
	for _, endpoint := range []string{"http://example.test", "https://user:secret@example.test", "https://example.test/path", "https://example.test?query", "https://example.test#fragment"} {
		if _, err := NewClient(endpoint, &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}); err == nil {
			t.Fatal("unsafe origin accepted", endpoint)
		}
	}
	for _, transport := range []*http.Transport{nil, {}, {TLSClientConfig: &tls.Config{}}, {TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}}, {TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, VerifyConnection: func(tls.ConnectionState) error { return nil }}}, {TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}, Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}} {
		if _, err := NewClient("https://example.test", transport); err == nil {
			t.Fatal("unsafe transport accepted")
		}
	}
	for _, configure := range []func(*http.Transport){
		func(tr *http.Transport) {
			tr.DialTLS = func(string, string) (net.Conn, error) { return nil, errors.New("unused") }
		},
		func(tr *http.Transport) {
			tr.DialTLSContext = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unused") }
		},
		func(tr *http.Transport) {
			tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{"custom": func(string, *tls.Conn) http.RoundTripper { return nil }}
		},
	} {
		tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
		configure(tr)
		if _, err := NewClient("https://example.test", tr); err == nil {
			t.Fatal("custom TLS path can bypass verified transport")
		}
	}
}

func TestV1ResponsesFailClosedWithoutCredentialDisclosure(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"unknown-root":      {200, `{"task_token":"SECRET","future":"SECRET"}`},
		"unknown-nested":    {200, `{"binding":{"future":"SECRET"}}`},
		"trailing":          {200, `{} {"task_token":"SECRET"}`},
		"oversize":          {200, `{}` + strings.Repeat(" ", 128<<10)},
		"scalar":            {200, `"SECRET"`},
		"null":              {200, `null`},
		"error-code":        {503, `{"code":"SECRET"}`},
		"error-fields":      {503, `{"code":"Unavailable","future":"SECRET"}`},
		"redirect-denial":   {307, `{"code":"PermissionDenied"}`},
		"mismatched-denial": {503, `{"code":"PermissionDenied"}`},
		"error-trailing":    {503, `{"code":"Unavailable"} {}`},
	}
	for name, example := range cases {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != RegisterPath || r.Header.Get("Authorization") != "Bearer fixture-owner" || r.Header.Get("Idempotency-Key") != "" {
					t.Error("v1 command or credential changed")
				}
				w.WriteHeader(example.status)
				w.Write([]byte(example.body))
			})
			_, err := client.Register(t.Context(), "fixture-owner", RegisterRequest{ParentToken: "fixture-parent"})
			unavailable(t, err)
			if calls.Load() != 1 {
				t.Fatal("command retried", calls.Load())
			}
		})
	}
}

func TestCredentialsNeverFollowRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != RegisterPath {
					t.Error("credential followed redirect")
				}
				w.Header().Set("Location", "/redirect-target")
				w.WriteHeader(status)
			})
			_, err := client.Register(t.Context(), "fixture-owner", RegisterRequest{ParentToken: "fixture-parent"})
			unavailable(t, err)
			if calls.Load() != 1 {
				t.Fatal("redirect or retry", calls.Load())
			}
		})
	}
}

func TestLostAcknowledgementRequiresExplicitOriginalRecovery(t *testing.T) {
	original := Registration{Reference: "original-reference", Binding: Binding{AdmissionID: "original", TaskClaimsDigest: "sealed"}, ExpiresAt: 12345, TaskToken: "original-child"}
	pending := RecoverRequest{Binding: Binding{AdmissionID: "original"}, TaskExpiresAt: 12348}
	var registers, recovers, exchanges atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case RegisterPath:
			registers.Add(1)
			var in RegisterRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Binding != pending.Binding || in.TaskExpiresAt != pending.TaskExpiresAt || in.ParentToken != "original-parent" {
				t.Error("original admission changed")
			}
			// Controlled lost acknowledgement after owner persistence: incomplete response.
			w.Header().Set("Content-Length", "100")
			w.Write([]byte(`{"reference":`))
		case RecoverPath:
			recovers.Add(1)
			var in map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&in) != nil || len(in) != 2 || in["parent_token"] != nil || r.Header.Get("Authorization") != "Bearer refreshed-owner" {
				t.Error("recovery widened authority")
			}
			var decoded RecoverRequest
			raw, _ := json.Marshal(in)
			if json.Unmarshal(raw, &decoded) != nil || decoded != pending {
				t.Error("recovery changed intent")
			}
			json.NewEncoder(w).Encode(original)
		case ExchangePath:
			exchanges.Add(1)
			var in ExchangeRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Reference != original.Reference || in.Binding != original.Binding || !in.Lookup || r.Header.Get("Authorization") != "" {
				t.Error("exchange changed sealed read binding")
			}
			json.NewEncoder(w).Encode(Child{Token: "original-read-child", ExpiresAt: original.ExpiresAt})
		default:
			t.Error("wrong wire version or path")
		}
	})
	_, err := client.Register(t.Context(), "original-owner", RegisterRequest{Binding: pending.Binding, TaskExpiresAt: pending.TaskExpiresAt, ParentToken: "original-parent"})
	unavailable(t, err)
	if registers.Load() != 1 || recovers.Load() != 0 {
		t.Fatal("implicit retry/recovery")
	}
	got, err := client.Recover(t.Context(), "refreshed-owner", pending)
	if err != nil || got != original {
		t.Fatal("original child/horizon lost", err)
	}
	child, err := client.Exchange(t.Context(), ExchangeRequest{Reference: got.Reference, Binding: got.Binding, Audience: "example", Lookup: true})
	if err != nil || child.ExpiresAt != original.ExpiresAt {
		t.Fatal("child changed", err)
	}
	if registers.Load() != 1 || recovers.Load() != 1 || exchanges.Load() != 1 {
		t.Fatal("unexpected calls")
	}
}

func TestCallerDeadlineBoundsCompleteResponse(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.Recover(ctx, "fixture-owner", RecoverRequest{})
	unavailable(t, err)
	if time.Since(started) > time.Second {
		t.Fatal("caller deadline not honored")
	}
}

func TestTypedErrorsRequireExactBrokerStatus(t *testing.T) {
	for code, status := range map[string]int{"InvalidArgument": 400, "Unauthenticated": 401, "PermissionDenied": 403, "NotFound": 404, "AlreadyExists": 409, "FailedPrecondition": 412, "Unavailable": 503} {
		t.Run(code, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(Error{Code: code})
			})
			_, err := client.Recover(t.Context(), "fixture-owner", RecoverRequest{})
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != code || failure.HTTPStatus() != status {
				t.Fatal("canonical error changed", err)
			}
		})
	}
}
