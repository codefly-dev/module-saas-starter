package objectstore

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

// Production wires guardDial as the dial hook, and the listing request and every object request runs it on
// the address being dialed. The hook is wrapped in a recorder that notes the
// guard's verdict and then stops the dial, and the only target is a local
// listener, so the test never dials anything but this process.
func TestEveryOutboundDialIsGuarded(t *testing.T) {
	var reached atomic.Int64
	live := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
	}))
	defer live.Close()
	listener := strings.TrimPrefix(live.URL, "http://")
	origin := live.URL

	if reflect.ValueOf(dialGuard).Pointer() != reflect.ValueOf(guardDial).Pointer() {
		t.Fatal("the production dial hook is not guardDial")
	}
	recorder := &egresstest.Recorder{}
	saved := dialGuard
	dialGuard = recorder.Wrap(guardDial)
	t.Cleanup(func() { dialGuard = saved })

	paths := []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"listing dial (List)", func(ctx context.Context) error {
			_, err := newGuardedClient(origin).List(ctx)
			return err
		}},
		{"object dial (Fetch)", func(ctx context.Context) error {
			_, err := newGuardedClient(origin).Fetch(ctx, "docs/page.md")
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
					t.Fatalf("guard(%q) = %v, want ErrBlockedAddress", v.Address, v.Err)
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

func newGuardedClient(endpoint string) *Client {
	return New(Config{Endpoint: endpoint, Bucket: "example-bucket", Region: "us-east-1", AccessKeyID: "AKIAEXAMPLE"}, "example-secret")
}
