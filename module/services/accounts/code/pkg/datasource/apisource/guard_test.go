package apisource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/auth/egresstest"
)

func TestGuardDial_RefusesEveryNonPublicRange(t *testing.T) {
	for _, tc := range egresstest.Refused {
		t.Run(tc.Name, func(t *testing.T) {
			if err := guardDial("tcp", tc.Address, nil); !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("guardDial(%q) = %v, want ErrBlockedAddress", tc.Address, err)
			}
		})
	}
}

func TestGuardDial_AdmitsPublicAddresses(t *testing.T) {
	for _, tc := range egresstest.Admitted {
		t.Run(tc.Name, func(t *testing.T) {
			if err := guardDial("tcp", tc.Address, nil); err != nil {
				t.Fatalf("guardDial(%q) = %v, want admitted", tc.Address, err)
			}
		})
	}
}

// Every outbound path a callable source has — the declared operation call and
// each OAuth token exchange — runs guardDial at the dial, on the address being
// connected to. The Control hook is wrapped in a recorder that notes what the
// guard decided and then stops the dial, and the only target is a local
// listener. A path that skipped the hook would reach that listener and record
// nothing; a guard that admitted loopback is caught by the recorded verdict.
// Which ranges the guard refuses is the table test's job above; this proves
// every path reaches it, without dialling anything but this process.
func TestEveryOutboundDialIsGuarded(t *testing.T) {
	var reached atomic.Int64
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"at","expires_in":60}`))
	}))
	defer live.Close()
	listener := strings.TrimPrefix(live.URL, "http://")

	if reflect.ValueOf(dialControl).Pointer() != reflect.ValueOf(guardDial).Pointer() {
		t.Fatal("the production dial hook is not guardDial")
	}
	recorder := &egresstest.Recorder{}
	saved := dialControl
	dialControl = recorder.Wrap(guardDial)
	t.Cleanup(func() { dialControl = saved })

	origin := live.URL
	paths := []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"operation dial (Do)", func(ctx context.Context) error {
			_, err := New(Config{BaseURL: origin, CredentialKind: CredentialKindBearer}, "example-token").
				Do(ctx, http.MethodPost, origin+"/v1/items", []byte(`{}`))
			return err
		}},
		{"operation dial (Fetch)", func(ctx context.Context) error {
			_, err := New(Config{BaseURL: origin, ResourcePath: "/v1/items", CredentialKind: CredentialKindBearer}, "example-token").
				Fetch(ctx)
			return err
		}},
		{"token exchange (refresh)", func(ctx context.Context) error {
			_, err := RefreshOAuth2(ctx, OAuth2Config{TokenURL: origin + "/token"}, "example-refresh", "example-secret")
			return err
		}},
		{"token exchange (client credentials)", func(ctx context.Context) error {
			_, err := ClientCredentials(ctx, OAuth2Config{TokenURL: origin + "/token"}, "example-client", "example-secret")
			return err
		}},
		{"token exchange (authorization code)", func(ctx context.Context) error {
			_, err := AuthorizationCode(ctx, OAuth2Config{TokenURL: origin + "/token"}, "example-secret",
				"example-code", "example-verifier", "https://app.example.com/callback")
			return err
		}},
	}
	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			recorder.Take()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := path.call(ctx)
			got := recorder.Take()
			if len(got) == 0 {
				t.Fatal("the dial did not run the guard")
			}
			for _, v := range got {
				if v.Address != listener {
					t.Fatalf("the guard judged %q, want the address being dialed %q", v.Address, listener)
				}
				if !errors.Is(v.Err, ErrBlockedAddress) {
					t.Fatalf("guardDial(%q) = %v, want ErrBlockedAddress", v.Address, v.Err)
				}
			}
			if !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("call error = %v, want ErrBlockedAddress", err)
			}
		})
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("live listener was reached %d times", n)
	}
}
