package auth_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/auth/egresstest"
)

func configureMeshEgress(t *testing.T, meshed bool, hosts, ranges string) {
	t.Helper()
	if err := auth.ConfigureMeshEgress(meshed, hosts, ranges); err != nil {
		t.Fatalf("ConfigureMeshEgress(%t, %q, %q) = %v", meshed, hosts, ranges, err)
	}
	t.Cleanup(func() { _ = auth.ConfigureMeshEgress(false, "", "") })
}

// The rule itself holds the mesh cases every guarded path is held to.
func TestAdmitsDestinationHoldsTheMeshCases(t *testing.T) {
	for _, c := range egresstest.MeshCases {
		t.Run(c.Name, func(t *testing.T) {
			c.Install(t)
			if got := auth.AdmitsDestinationDialAddress(c.Requested(), c.Dialed()); got != c.Admitted {
				t.Fatalf("AdmitsDestinationDialAddress(%q, %q) = %t, want %t", c.Requested(), c.Dialed(), got, c.Admitted)
			}
		})
	}
}

// With mesh egress installed, every vector the public classifier refuses is
// still refused for an undeclared host, an IP literal, and — except for the
// mesh range itself — a declared host.
func TestMeshEgressAdmitsNothingButTheMeshRangeForADeclaredHost(t *testing.T) {
	configureMeshEgress(t, true, egresstest.DeclaredHost, "")
	meshRange := netip.MustParsePrefix(auth.DefaultMeshEgressRange)
	for _, v := range egresstest.Refused {
		address := netip.MustParseAddr(v.Host)
		for _, requested := range []string{egresstest.UndeclaredHost + ":443", v.DialAddress()} {
			if auth.AdmitsDestination(requested, address) {
				t.Errorf("%s: admitted for %q", v.Name, requested)
			}
		}
		if got := auth.AdmitsDestination(egresstest.DeclaredHost+":443", address); got != meshRange.Contains(address.Unmap()) {
			t.Errorf("%s: declared host admitted = %t", v.Name, got)
		}
	}
}

// The requested host is compared as DNS compares it (case-insensitive, the
// root's trailing dot optional), and the dialled address as the address it
// is: a v4-mapped spelling is the IPv4 address, a zoned one is refused.
func TestMeshEgressComparesTheRequestAndTheAddressCanonically(t *testing.T) {
	configureMeshEgress(t, true, egresstest.DeclaredHost, "")
	mesh := netip.MustParseAddr("240.240.0.5")
	for _, requested := range []string{"API.Declared.Example:443", "api.declared.example.:443"} {
		if !auth.AdmitsDestination(requested, mesh) {
			t.Errorf("%q refused", requested)
		}
	}
	if !auth.AdmitsDestination(egresstest.DeclaredHost+":443", netip.MustParseAddr("::ffff:240.240.0.5")) {
		t.Error("v4-mapped mesh address refused")
	}
	if auth.AdmitsDestination(egresstest.DeclaredHost+":443", netip.MustParseAddr("2001:2::5%eth0")) {
		t.Error("zoned address admitted")
	}
	for _, requested := range []string{"", egresstest.DeclaredHost, "[240.240.0.5]:443", "api.declared.example.240:443"} {
		if auth.AdmitsDestination(requested, mesh) {
			t.Errorf("%q admitted", requested)
		}
	}
}

// A configured range replaces the default, and an IPv6 range is admitted only
// when named.
func TestMeshEgressRangeIsTheConfiguredOne(t *testing.T) {
	configureMeshEgress(t, true, "api.declared.example, hooks.declared.example:8443", "240.240.1.0/24 2001:2::/48")
	declared := egresstest.DeclaredHost + ":443"
	for address, want := range map[string]bool{
		"240.240.0.5": false, // the default, not configured here
		"240.240.1.5": true,
		"2001:2::5":   true,
	} {
		if got := auth.AdmitsDestination(declared, netip.MustParseAddr(address)); got != want {
			t.Errorf("%s admitted = %t, want %t", address, got, want)
		}
	}
	if !auth.AdmitsDestination("hooks.declared.example:8443", netip.MustParseAddr("240.240.1.5")) {
		t.Error("a host declared with its port refused on that port")
	}
	if auth.AdmitsDestination("hooks.declared.example:443", netip.MustParseAddr("240.240.1.5")) {
		t.Error("a host declared on 8443 admitted on 443")
	}
}

// Without the mesh assertion nothing may be declared, and a meshed deployment
// that declares no host installs nothing.
func TestConfigureMeshEgressRefusesWhatItCannotAdmit(t *testing.T) {
	t.Cleanup(func() { _ = auth.ConfigureMeshEgress(false, "", "") })
	for _, tc := range []struct {
		name          string
		meshed        bool
		hosts, ranges string
		want          error
	}{
		{"hosts without the mesh", false, "api.declared.example", "", auth.ErrMeshEgressWithoutMesh},
		{"range without the mesh", false, "", auth.DefaultMeshEgressRange, auth.ErrMeshEgressWithoutMesh},
		{"IP literal host", true, "240.240.0.5", "", auth.ErrInvalidMeshEgress},
		{"numeric last label", true, "api.example.240", "", auth.ErrInvalidMeshEgress},
		{"bracketed IPv6 host", true, "[2001:2::5]:443", "", auth.ErrInvalidMeshEgress},
		{"wildcard host", true, "*.declared.example", "", auth.ErrInvalidMeshEgress},
		{"upper-case host", true, "API.declared.example", "", auth.ErrInvalidMeshEgress},
		{"single label", true, "localhost", "", auth.ErrInvalidMeshEgress},
		{"URL", true, "https://api.declared.example", "", auth.ErrInvalidMeshEgress},
		{"port zero", true, "api.declared.example:0", "", auth.ErrInvalidMeshEgress},
		{"port out of range", true, "api.declared.example:70000", "", auth.ErrInvalidMeshEgress},
		{"port with a leading zero", true, "api.declared.example:0443", "", auth.ErrInvalidMeshEgress},
		{"private range", true, "api.declared.example", "10.0.0.0/8", auth.ErrInvalidMeshEgress},
		{"carrier NAT range", true, "api.declared.example", "100.64.0.0/10", auth.ErrInvalidMeshEgress},
		{"link-local range", true, "api.declared.example", "169.254.0.0/16", auth.ErrInvalidMeshEgress},
		{"everything", true, "api.declared.example", "0.0.0.0/0", auth.ErrInvalidMeshEgress},
		{"wider than reserved space", true, "api.declared.example", "224.0.0.0/3", auth.ErrInvalidMeshEgress},
		{"non-canonical prefix", true, "api.declared.example", "240.240.0.1/16", auth.ErrInvalidMeshEgress},
		{"not a prefix", true, "api.declared.example", "240.240.0.5", auth.ErrInvalidMeshEgress},
		{"unique local IPv6", true, "api.declared.example", "fd00::/8", auth.ErrInvalidMeshEgress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := auth.ConfigureMeshEgress(tc.meshed, tc.hosts, tc.ranges); !errors.Is(err, tc.want) {
				t.Fatalf("ConfigureMeshEgress = %v, want %v", err, tc.want)
			}
			// A refused configuration installs nothing.
			if auth.AdmitsDestination(egresstest.DeclaredHost+":443", netip.MustParseAddr("240.240.0.5")) {
				t.Fatal("a refused configuration admitted the mesh range")
			}
		})
	}
	if err := auth.ConfigureMeshEgress(true, "", ""); err != nil {
		t.Fatalf("meshed with no hosts = %v, want nothing installed", err)
	}
	if auth.AdmitsDestination(egresstest.DeclaredHost+":443", netip.MustParseAddr("240.240.0.5")) {
		t.Fatal("meshed with no declared host admitted the mesh range")
	}
}

// GuardedDialContext resolves the requested host, builds the hook from the
// host:port the client asked for, and dials each answer as a literal, so the
// hook judges the address actually dialled. A refused answer falls through to
// the next one; an IP literal is never resolved.
func TestGuardedDialContextJudgesEachResolvedAddress(t *testing.T) {
	stop := errors.New("stopped")
	var requests, judged []string
	dial := auth.GuardedDialContext(net.Dialer{}, egresstest.Resolver{
		"api.example.com": {netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("93.184.216.34")},
	}, func(requested string) auth.DialControl {
		requests = append(requests, requested)
		return func(_, address string, _ syscall.RawConn) error {
			judged = append(judged, address)
			return stop
		}
	})
	if _, err := dial(context.Background(), "tcp", "api.example.com:443"); !errors.Is(err, stop) {
		t.Fatalf("dial = %v, want the hook's refusal", err)
	}
	if len(requests) != 1 || requests[0] != "api.example.com:443" {
		t.Fatalf("hook built for %q, want the requested host:port once", requests)
	}
	if len(judged) != 2 || judged[0] != "10.0.0.5:443" || judged[1] != "93.184.216.34:443" {
		t.Fatalf("hook judged %q, want each resolved address in order", judged)
	}

	judged = nil
	if _, err := dial(context.Background(), "tcp", "240.240.0.5:443"); !errors.Is(err, stop) {
		t.Fatalf("literal dial = %v, want the hook's refusal", err)
	}
	if len(judged) != 1 || judged[0] != "240.240.0.5:443" {
		t.Fatalf("literal judged %q, want the literal itself", judged)
	}
	if _, err := dial(context.Background(), "tcp", "unknown.example.com:443"); err == nil {
		t.Fatal("an unresolvable host dialled")
	}
}

// The Client ID Metadata Document fetch applies the rule end to end.
func TestTheMetadataFetchAppliesTheMeshEgressRule(t *testing.T) {
	egresstest.RunMeshCases(t, []egresstest.MeshPath{
		{Name: "metadata document fetch", Call: func(ctx context.Context, origin string) error {
			return auth.FetchMetadataDocumentForTest(ctx, origin+"/client")
		}},
	}, auth.ErrClientMetadataUnreachable, func(t *testing.T, c egresstest.MeshCase) *egresstest.Recorder {
		recorder := &egresstest.Recorder{}
		t.Cleanup(auth.WrapMetadataDialGuardForTest(recorder.Wrap))
		t.Cleanup(auth.SetMetadataResolverForTest(c.Resolver()))
		return recorder
	})
}
