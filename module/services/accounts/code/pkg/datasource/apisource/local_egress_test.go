package apisource

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"syscall"
	"testing"
)

// installLocalEgress installs value as a LOCAL runtime's allowance for one test
// and withdraws it afterwards, so no other test in the package dials through it.
func installLocalEgress(t *testing.T, value string) {
	t.Helper()
	if err := ConfigureLocalEgress(value, true); err != nil {
		t.Fatalf("ConfigureLocalEgress(%q, local) = %v", value, err)
	}
	t.Cleanup(func() { _ = ConfigureLocalEgress("", false) })
}

// Every spelling that is not exactly one loopback IP literal and one port is
// refused whole. Each case names the property it holds, so dropping one check
// fails a case named after it.
func TestLocalEgress_RefusesAnythingButAnExplicitLoopbackAddressAndPort(t *testing.T) {
	for name, entry := range map[string]string{
		"no port":                    "127.0.0.1",
		"port zero":                  "127.0.0.1:0",
		"wildcard host":              "*:18080",
		"wildcard port":              "127.0.0.1:*",
		"port range":                 "127.0.0.1:18080-18090",
		"cidr":                       "127.0.0.0/8:18080",
		"hostname":                   "localhost:18080",
		"unspecified v4":             "0.0.0.0:18080",
		"unspecified v6":             "[::]:18080",
		"private":                    "10.0.0.1:18080",
		"link-local metadata":        "169.254.169.254:80",
		"public":                     "93.184.216.34:443",
		"ipv4-mapped loopback":       "[::ffff:127.0.0.1]:18080",
		"zoned loopback":             "[::1%lo0]:18080",
		"leading-zero port":          "127.0.0.1:018080",
		"unbracketed v6":             "::1:18080",
		"valid entry beside invalid": "127.0.0.1:18080,10.0.0.1:18080",
	} {
		t.Run(name, func(t *testing.T) {
			localEgress.Store(nil)
			t.Cleanup(func() { localEgress.Store(nil) })
			err := ConfigureLocalEgress(entry, true)
			if !errors.Is(err, ErrInvalidLocalEgress) {
				t.Fatalf("ConfigureLocalEgress(%q) = %v, want ErrInvalidLocalEgress", entry, err)
			}
			if localEgress.Load() != nil {
				t.Fatal("a refused allowance must install nothing")
			}
		})
	}
	for _, entry := range []string{"127.0.0.1:18080", "[::1]:18080", "127.0.0.2:80", "127.0.0.1:18080, [::1]:18081"} {
		installLocalEgress(t, entry)
	}
}

// The allowance is refused, not ignored, on a runtime that is not local; an
// empty value is the default everywhere and is no allowance at all.
func TestLocalEgress_RefusedOutsideALocalRuntime(t *testing.T) {
	t.Cleanup(func() { _ = ConfigureLocalEgress("", false) })
	if err := ConfigureLocalEgress("127.0.0.1:18080", false); !errors.Is(err, ErrLocalEgressOutsideLocal) {
		t.Fatalf("ConfigureLocalEgress outside local = %v, want ErrLocalEgressOutsideLocal", err)
	}
	if localEgress.Load() != nil {
		t.Fatal("an allowance refused outside local must install nothing")
	}
	for _, empty := range []string{"", "  ", " , "} {
		if err := ConfigureLocalEgress(empty, false); err != nil {
			t.Fatalf("empty allowance %q outside local = %v, want nil", empty, err)
		}
		if localEgress.Load() != nil {
			t.Fatalf("empty allowance %q installed something", empty)
		}
	}
}

// The allowance admits the exact address and port it lists and nothing else:
// not the next port, not a neighbouring loopback address, not the other loopback
// family, and never a non-public address the public guard refuses.
func TestLocalEgress_AdmitsOnlyTheListedAddress(t *testing.T) {
	installLocalEgress(t, "127.0.0.1:18080")
	control := withLocalEgress(dialControl)
	if err := control("tcp", "127.0.0.1:18080", nil); err != nil {
		t.Fatalf("listed address refused: %v", err)
	}
	if err := control("tcp6", "[::ffff:127.0.0.1]:18080", nil); err != nil {
		t.Fatalf("the listed address dialed in its mapped form refused: %v", err)
	}
	for _, address := range []string{
		"127.0.0.1:18081",
		"127.0.0.2:18080",
		"[::1]:18080",
		"10.0.0.1:18080",
		"169.254.169.254:18080",
		"192.168.1.10:18080",
		"100.100.100.200:18080",
		"[64:ff9b::a9fe:a9fe]:18080",
		"not-an-address",
	} {
		if err := control("tcp", address, nil); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("unlisted %q = %v, want ErrBlockedAddress", address, err)
		}
	}
	// A public address is still judged by the public guard alone: the allowance
	// neither admits nor refuses anything it does not list.
	if err := control("tcp", "93.184.216.34:443", nil); err != nil {
		t.Errorf("public address = %v, want the public guard's answer (allowed)", err)
	}
}

// With no allowance installed the guarded client is exactly guardDial.
func TestLocalEgress_NoAllowanceIsThePublicGuard(t *testing.T) {
	if err := ConfigureLocalEgress("", true); err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(withLocalEgress(guardDial)).Pointer() != reflect.ValueOf(guardDial).Pointer() {
		t.Fatal("with no allowance the dial control must be the guard itself")
	}
	if err := withLocalEgress(dialControl)("tcp", "127.0.0.1:18080", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("loopback without an allowance = %v, want ErrBlockedAddress", err)
	}
}

// Every address the allowance does not list is handed to the guard it wraps,
// unchanged: the shared classifier, not this file, judges it. Only the exact
// listed entry skips the guard.
func TestLocalEgress_EveryUnlistedAddressReachesTheGuard(t *testing.T) {
	installLocalEgress(t, "127.0.0.1:18080")
	var judged []string
	control := withLocalEgress(func(_, address string, _ syscall.RawConn) error {
		judged = append(judged, address)
		return ErrBlockedAddress
	})
	unlisted := []string{"127.0.0.1:18081", "[::1]:18080", "100.100.100.200:18080", "93.184.216.34:443", "not-an-address"}
	for _, address := range unlisted {
		_ = control("tcp", address, nil)
	}
	if err := control("tcp", "127.0.0.1:18080", nil); err != nil {
		t.Fatalf("listed address = %v, want admitted", err)
	}
	if !reflect.DeepEqual(judged, unlisted) {
		t.Fatalf("the guard judged %q, want exactly the unlisted addresses %q", judged, unlisted)
	}
}

// The issue's done-criterion at this layer: an httptest provider on loopback,
// reached by the REAL guarded clients — the client_credentials token exchange
// and the operation call — with no transport substituted. Without the
// allowance both dials are refused; with an allowance for another port they
// still are; with the listed address both succeed.
func TestLocalEgress_ClientCredentialsReachALocalStubThroughTheRealGuard(t *testing.T) {
	var tokenCalls, apiCalls int
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls++
			if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "client_credentials" ||
				r.PostForm.Get("client_id") != "example-client" || r.PostForm.Get("client_secret") != "example-secret-value" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "stub-access-token-0123456789", "expires_in": 3600})
		case "/v1/items":
			apiCalls++
			if r.Header.Get("Authorization") != "Bearer stub-access-token-0123456789" {
				http.Error(w, "", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"items":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer stub.Close()
	address := stub.Listener.Addr().String()
	oauth := OAuth2Config{TokenURL: stub.URL + "/token"}
	fetch := func(token string) error {
		_, err := New(Config{BaseURL: stub.URL, ResourcePath: "/v1/items", CredentialKind: CredentialKindBearer}, token).Fetch(context.Background())
		return err
	}

	_ = ConfigureLocalEgress("", true)
	if _, err := ClientCredentials(context.Background(), oauth, "example-client", "example-secret-value"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("token exchange without an allowance = %v, want ErrBlockedAddress", err)
	}
	if err := fetch("stub-access-token-0123456789"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("operation call without an allowance = %v, want ErrBlockedAddress", err)
	}

	host, port, _ := net.SplitHostPort(address)
	other, _ := strconv.Atoi(port)
	installLocalEgress(t, net.JoinHostPort(host, strconv.Itoa(other+1)))
	if _, err := ClientCredentials(context.Background(), oauth, "example-client", "example-secret-value"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("token exchange with another port listed = %v, want ErrBlockedAddress", err)
	}
	if tokenCalls != 0 || apiCalls != 0 {
		t.Fatalf("a refused dial reached the stub (token %d, api %d)", tokenCalls, apiCalls)
	}

	installLocalEgress(t, address)
	token, err := ClientCredentials(context.Background(), oauth, "example-client", "example-secret-value")
	if err != nil {
		t.Fatalf("token exchange with the stub listed = %v", err)
	}
	if err := fetch(token.AccessToken); err != nil {
		t.Fatalf("operation call with the stub listed = %v", err)
	}
	if tokenCalls != 1 || apiCalls != 1 {
		t.Fatalf("stub calls (token %d, api %d), want one of each", tokenCalls, apiCalls)
	}
	if u, _ := url.Parse(stub.URL); u.Hostname() != "127.0.0.1" {
		t.Fatalf("httptest stub is on %s, not loopback IPv4", u.Hostname())
	}
}
