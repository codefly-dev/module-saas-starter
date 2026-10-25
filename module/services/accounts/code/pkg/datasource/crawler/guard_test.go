package crawler

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

	"accounts/pkg/auth"
	"accounts/pkg/auth/egresstest"
)

func TestGuardDial_RefusesEveryNonPublicRange(t *testing.T) {
	for _, tc := range egresstest.Refused {
		t.Run(tc.Name, func(t *testing.T) {
			if err := guardDial(tc.DialAddress())("tcp", tc.DialAddress(), nil); !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("guardDial(%q) = %v, want ErrBlockedAddress", tc.DialAddress(), err)
			}
		})
	}
	for _, tc := range egresstest.Unclassifiable {
		t.Run(tc.Name, func(t *testing.T) {
			if err := guardDial(tc.Address)("tcp", tc.Address, nil); !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("guardDial(%q) = %v, want ErrBlockedAddress", tc.Address, err)
			}
		})
	}
}

func TestGuardDial_AdmitsPublicAddresses(t *testing.T) {
	for _, tc := range egresstest.Admitted {
		t.Run(tc.Name, func(t *testing.T) {
			if err := guardDial(tc.DialAddress())("tcp", tc.DialAddress(), nil); err != nil {
				t.Fatalf("guardDial(%q) = %v, want admitted", tc.DialAddress(), err)
			}
		})
	}
}

// Production wires guardDial as the dial hook, and the sitemap request and every page request runs it on
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
	dialGuard = func(requested string) auth.DialControl { return recorder.Wrap(guardDial(requested)) }
	t.Cleanup(func() { dialGuard = saved })

	paths := []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"sitemap dial (List)", func(ctx context.Context) error {
			_, err := New(Config{SitemapURL: origin + "/sitemap.xml"}).List(ctx)
			return err
		}},
		{"page dial (Fetch)", func(ctx context.Context) error {
			_, err := New(Config{SitemapURL: origin + "/sitemap.xml"}).Fetch(ctx, origin+"/docs/page")
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

// Every outbound path applies the egress rule's mesh admission end to end: a
// declared host a meshed cell resolves into its mesh egress range is dialled,
// and every case around it is refused (egresstest.MeshCases). DNS is a fake
// and an admitted dial is stopped before it connects.
func TestEveryOutboundPathAppliesTheMeshEgressRule(t *testing.T) {
	egresstest.RunMeshCases(t, []egresstest.MeshPath{
		{Name: "sitemap dial (List)", Call: func(ctx context.Context, origin string) error {
			_, err := New(Config{SitemapURL: origin + "/sitemap.xml"}).List(ctx)
			return err
		}},
		{Name: "page dial (Fetch)", Call: func(ctx context.Context, origin string) error {
			_, err := New(Config{SitemapURL: origin + "/sitemap.xml"}).Fetch(ctx, origin+"/docs/page")
			return err
		}},
	}, ErrBlockedAddress, func(t *testing.T, c egresstest.MeshCase) *egresstest.Recorder {
		recorder := &egresstest.Recorder{}
		savedGuard, savedResolver := dialGuard, dialResolver
		dialGuard = func(requested string) auth.DialControl { return recorder.Wrap(guardDial(requested)) }
		dialResolver = c.Resolver()
		t.Cleanup(func() { dialGuard, dialResolver = savedGuard, savedResolver })
		return recorder
	})
}
