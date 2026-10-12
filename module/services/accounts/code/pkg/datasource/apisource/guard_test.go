package apisource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// guardRefuses is one named case per range the dial guard must refuse: every
// entry of the IANA IPv4 and IPv6 special-purpose registries, the IPv6 space
// outside the 2000::/3 global unicast allocation, and the input shapes the
// guard cannot classify. A case is named after its range, so a change that
// re-admits one range fails a test that says which.
var guardRefuses = []struct{ name, address string }{
	// Admitted at v0.0.94: Go's predicates call each of these global unicast.
	{"shared address space 100.64.0.0/10, low end", "100.64.0.1:443"},
	{"shared address space 100.64.0.0/10, cloud metadata 100.100.100.200", "100.100.100.200:80"},
	{"shared address space 100.64.0.0/10, high end", "100.127.255.255:443"},
	{"NAT64 64:ff9b::/96 wrapping the link-local metadata address", "[64:ff9b::a9fe:a9fe]:80"},
	{"NAT64 64:ff9b::/96 wrapping private 10.0.0.1", "[64:ff9b::a00:1]:443"},
	{"NAT64 64:ff9b::/96 wrapping a public address", "[64:ff9b::808:808]:443"},
	{"local-use NAT64 64:ff9b:1::/48", "[64:ff9b:1::a9fe:a9fe]:80"},
	{"6to4 2002::/16 wrapping the link-local metadata address", "[2002:a9fe:a9fe::1]:80"},
	{"6to4 2002::/16 wrapping a public address", "[2002:808:808::1]:443"},
	{"Teredo 2001::/32", "[2001:0:4136:e378:8000:63bf:3fff:fdd2]:443"},
	{"benchmarking 198.18.0.0/15, low end", "198.18.0.1:443"},
	{"benchmarking 198.18.0.0/15, high end", "198.19.255.255:443"},
	{"IETF protocol assignments 192.0.0.0/24", "192.0.0.170:443"},
	{"reserved 240.0.0.0/4, low end", "240.0.0.1:443"},
	{"reserved 240.0.0.0/4, high end", "255.255.255.254:443"},

	// Refused before v0.0.94, and still refused.
	{"loopback 127.0.0.0/8", "127.0.0.1:80"},
	{"loopback 127.0.0.0/8, high end", "127.255.255.254:80"},
	{"loopback ::1", "[::1]:443"},
	{"private 10.0.0.0/8", "10.1.2.3:80"},
	{"private 172.16.0.0/12, low end", "172.16.0.1:80"},
	{"private 172.16.0.0/12, high end", "172.31.255.255:80"},
	{"private 192.168.0.0/16", "192.168.0.1:80"},
	{"link-local 169.254.0.0/16, cloud metadata", "169.254.169.254:80"},
	{"link-local fe80::/10", "[fe80::1]:443"},
	{"unspecified 0.0.0.0", "0.0.0.0:80"},
	{"unspecified ::", "[::]:443"},
	{"link-local multicast 224.0.0.0/24", "224.0.0.1:80"},
	{"link-local multicast ff02::/16", "[ff02::1]:443"},

	// The rest of the IPv4 special-purpose registry.
	{"this network 0.0.0.0/8", "0.1.2.3:80"},
	{"multicast 224.0.0.0/4", "239.1.1.1:80"},
	{"limited broadcast 255.255.255.255", "255.255.255.255:80"},
	{"documentation 192.0.2.0/24", "192.0.2.1:443"},
	{"documentation 198.51.100.0/24", "198.51.100.1:443"},
	{"documentation 203.0.113.0/24", "203.0.113.1:443"},
	{"AS112 192.31.196.0/24", "192.31.196.1:443"},
	{"AMT 192.52.193.0/24", "192.52.193.1:443"},
	{"6to4 relay anycast 192.88.99.0/24", "192.88.99.1:443"},
	{"AS112 direct delegation 192.175.48.0/24", "192.175.48.1:443"},

	// The rest of the IPv6 special-purpose registry.
	{"discard-only 100::/64", "[100::1]:443"},
	{"dummy prefix 100:0:0:1::/64", "[100:0:0:1::1]:443"},
	{"IETF protocol assignments 2001::/23, unassigned remainder", "[2001:100::1]:443"},
	{"IETF protocol assignments 2001::/23, port control anycast", "[2001:1::1]:443"},
	{"IETF protocol assignments 2001::/23, benchmarking", "[2001:2::1]:443"},
	{"IETF protocol assignments 2001::/23, ORCHIDv2", "[2001:20::1]:443"},
	{"documentation 2001:db8::/32", "[2001:db8::1]:443"},
	{"documentation 3fff::/20", "[3fff::1]:443"},
	{"segment routing 5f00::/16", "[5f00::1]:443"},
	{"unique local fc00::/7", "[fc00::1]:443"},
	{"unique local fc00::/7, fd00::/8 half", "[fd00::1]:443"},
	{"multicast ff00::/8", "[ff0e::1]:443"},
	{"interface-local multicast ff01::/16", "[ff01::1]:443"},

	// IPv6 outside the 2000::/3 global unicast allocation, which Go also calls
	// global unicast and the special-purpose registry does not list.
	{"IPv4-compatible ::/96 wrapping the link-local metadata address", "[::a9fe:a9fe]:80"},
	{"IPv4-compatible ::/96 wrapping private 10.0.0.1", "[::a00:1]:443"},
	{"site-local fec0::/10", "[fec0::1]:443"},
	{"reserved by IETF 4000::/3", "[4001::1]:443"},
	{"reserved by IETF 8000::/1", "[8000::1]:443"},

	// A v4-mapped address is judged as the IPv4 address it wraps.
	{"v4-mapped loopback", "[::ffff:127.0.0.1]:80"},
	{"v4-mapped private", "[::ffff:10.0.0.1]:80"},
	{"v4-mapped link-local metadata", "[::ffff:169.254.169.254]:80"},
	{"v4-mapped shared address space", "[::ffff:100.100.100.200]:80"},
	{"v4-mapped benchmarking", "[::ffff:198.18.0.1]:80"},

	// A zone scopes an address to one interface; nothing public carries one.
	{"zoned link-local", "[fe80::1%eth0]:443"},
	{"zoned unique local", "[fd00::1%lo0]:443"},
	{"zoned global unicast", "[2606:4700::1111%eth0]:443"},

	// Fail closed on anything the guard cannot classify.
	{"unparsable host", "not-an-ip:80"},
	{"hostname instead of an address", "example.com:443"},
	{"empty", ""},
	{"no port", "1.1.1.1"},
	{"bracketed address without a port", "[2606:4700::1111]"},
	{"IPv4 with a leading zero", "01.1.1.1:80"},
}

// guardAdmits are public addresses, several sitting one step outside a refused
// range, so a refused range that widens fails a named test too.
var guardAdmits = []struct{ name, address string }{
	{"public IPv4", "8.8.8.8:443"},
	{"public IPv4, second", "1.1.1.1:80"},
	{"just below shared address space", "100.63.255.255:443"},
	{"just above shared address space", "100.128.0.0:443"},
	{"just below benchmarking", "198.17.255.255:443"},
	{"just above benchmarking", "198.20.0.0:443"},
	{"just above IETF protocol assignments", "192.0.1.1:443"},
	{"just below multicast", "223.255.255.254:443"},
	{"v4-mapped public", "[::ffff:8.8.8.8]:443"},
	{"public IPv6", "[2606:4700:4700::1111]:443"},
	{"public IPv6 just above the 2001::/23 assignments", "[2001:200::1]:443"},
	{"public IPv6 outside 2001::/23", "[2001:4860:4860::8888]:443"},
	{"public IPv6 just above 6to4", "[2003::1]:443"},
	{"public IPv6, RIPE allocation", "[2a00:1450:4001::1]:443"},
}

func TestGuardDial_RefusesEveryNonPublicRange(t *testing.T) {
	for _, tc := range guardRefuses {
		t.Run(tc.name, func(t *testing.T) {
			if err := guardDial("tcp", tc.address, nil); !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("guardDial(%q) = %v, want ErrBlockedAddress", tc.address, err)
			}
		})
	}
}

func TestGuardDial_AdmitsPublicAddresses(t *testing.T) {
	for _, tc := range guardAdmits {
		t.Run(tc.name, func(t *testing.T) {
			if err := guardDial("tcp", tc.address, nil); err != nil {
				t.Fatalf("guardDial(%q) = %v, want admitted", tc.address, err)
			}
		})
	}
}

// Every outbound path a callable source has — the declared operation call and
// each OAuth token exchange — dials through the guard. Each target is a literal
// the v0.0.94 guard admitted: without the guard on that path the dial is
// attempted and fails as a network error or a timeout, never ErrBlockedAddress.
// The loopback target is a live listener, so a path that skipped the guard
// would reach it and the counter would say so.
func TestEveryOutboundDialIsGuarded(t *testing.T) {
	var reached atomic.Int64
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"at","expires_in":60}`))
	}))
	defer live.Close()

	targets := map[string]string{
		"live loopback listener":                strings.TrimPrefix(live.URL, "http://"),
		"shared address space metadata service": "100.100.100.200:80",
		"NAT64 wrapping link-local metadata":    "[64:ff9b::a9fe:a9fe]:80",
		"6to4 wrapping link-local metadata":     "[2002:a9fe:a9fe::1]:80",
		"Teredo":                                "[2001:0:4136:e378:8000:63bf:3fff:fdd2]:80",
		"benchmarking":                          "198.18.0.1:80",
		"IETF protocol assignments":             "192.0.0.170:80",
		"reserved":                              "240.0.0.1:80",
		"IPv4-compatible link-local metadata":   "[::a9fe:a9fe]:80",
	}
	paths := map[string]func(ctx context.Context, origin string) error{
		"operation dial (Do)": func(ctx context.Context, origin string) error {
			_, err := New(Config{BaseURL: origin, CredentialKind: CredentialKindBearer}, "example-token").
				Do(ctx, http.MethodPost, origin+"/v1/items", []byte(`{}`))
			return err
		},
		"operation dial (Fetch)": func(ctx context.Context, origin string) error {
			_, err := New(Config{BaseURL: origin, ResourcePath: "/v1/items", CredentialKind: CredentialKindBearer}, "example-token").
				Fetch(ctx)
			return err
		},
		"token exchange (refresh)": func(ctx context.Context, origin string) error {
			_, err := RefreshOAuth2(ctx, OAuth2Config{TokenURL: origin + "/token"}, "example-refresh", "example-secret")
			return err
		},
		"token exchange (client credentials)": func(ctx context.Context, origin string) error {
			_, err := ClientCredentials(ctx, OAuth2Config{TokenURL: origin + "/token"}, "example-client", "example-secret")
			return err
		},
		"token exchange (authorization code)": func(ctx context.Context, origin string) error {
			_, err := AuthorizationCode(ctx, OAuth2Config{TokenURL: origin + "/token"}, "example-secret",
				"example-code", "example-verifier", "https://app.example.com/callback")
			return err
		},
	}
	for pathName, call := range paths {
		for targetName, hostPort := range targets {
			t.Run(pathName+"/"+targetName, func(t *testing.T) {
				// The guard refuses before connect, so this bound only matters
				// when it is missing and the dial goes out.
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				err := call(ctx, "http://"+hostPort)
				if !errors.Is(err, ErrBlockedAddress) {
					t.Fatalf("dial to %s was not refused by the guard: %v", hostPort, err)
				}
			})
		}
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("live listener was reached %d times", n)
	}
}
