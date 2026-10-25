package egresstest

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"accounts/pkg/auth"
)

// The mesh egress cases every guarded egress path is held to
// (auth/mesh_egress.go): a host the deployment declares, resolved by a meshed
// cell into its mesh egress range, is admitted; everything around it is
// refused exactly as before.

// DeclaredHost is the one host the mesh cases declare, on port 443.
const DeclaredHost = "api.declared.example"

// UndeclaredHost is a host no case declares.
const UndeclaredHost = "api.undeclared.example"

// MeshCase is one dial: the host and port a client asks for, the one address
// DNS answers with, whether the deployment asserts its mesh, and whether the
// rule admits the dial.
type MeshCase struct {
	Name     string
	Meshed   bool
	Host     string
	Port     int
	Answer   string
	Admitted bool
}

// Configuration is what a case installs with auth.ConfigureMeshEgress: the
// mesh assertion and the declared host, which every deployment carries from
// its environment's egress declaration and which is inert without the
// assertion. The range is left to its default, 240.240.0.0/16.
func (c MeshCase) Configuration() (meshed bool, hosts, ranges string) {
	return c.Meshed, DeclaredHost, ""
}

// Requested is the "host:port" the client asks for.
func (c MeshCase) Requested() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// Dialed is the "ip:port" the dial reaches.
func (c MeshCase) Dialed() string { return net.JoinHostPort(c.Answer, strconv.Itoa(c.Port)) }

// Resolver answers the case's host with its one address.
func (c MeshCase) Resolver() Resolver {
	return Resolver{c.Host: {netip.MustParseAddr(c.Answer)}}
}

// MeshCases is the table. Each guarded path runs it end to end through its own
// client, with Resolver standing in for DNS.
var MeshCases = []MeshCase{
	{"declared host in the mesh range, meshed", true, DeclaredHost, 443, "240.240.0.5", true},
	{"declared host in the mesh range, not meshed", false, DeclaredHost, 443, "240.240.0.5", false},
	{"undeclared host in the mesh range, meshed", true, UndeclaredHost, 443, "240.240.0.5", false},
	{"declared host on an undeclared port, meshed", true, DeclaredHost, 8443, "240.240.0.5", false},
	{"declared host in reserved space outside the mesh range", true, DeclaredHost, 443, "240.241.0.5", false},
	{"declared host in an IPv6 range not configured", true, DeclaredHost, 443, "2001:2::5", false},
	{"declared host resolving private", true, DeclaredHost, 443, "10.0.0.5", false},
	{"declared host resolving loopback", true, DeclaredHost, 443, "127.0.0.1", false},
	{"declared host resolving cloud metadata", true, DeclaredHost, 443, "169.254.169.254", false},
	{"declared host resolving carrier NAT", true, DeclaredHost, 443, "100.64.0.5", false},
	{"IP literal in the mesh range, meshed", true, "240.240.0.5", 443, "240.240.0.5", false},
	{"declared host resolving public, meshed", true, DeclaredHost, 443, "93.184.216.34", true},
	{"undeclared host resolving public, meshed", true, UndeclaredHost, 443, "93.184.216.34", true},
	{"undeclared host resolving public, not meshed", false, UndeclaredHost, 443, "93.184.216.34", true},
}

// Resolver is a fake DNS: a host answers with the addresses listed for it, and
// any other host is not found. It never consults the network.
type Resolver map[string][]netip.Addr

// LookupNetIP implements auth.Resolver.
func (r Resolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if answers, ok := r[host]; ok {
		return answers, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// Install installs the case's mesh egress configuration for the test and
// removes it afterwards.
func (c MeshCase) Install(t *testing.T) {
	t.Helper()
	meshed, hosts, ranges := c.Configuration()
	if err := auth.ConfigureMeshEgress(meshed, hosts, ranges); err != nil {
		t.Fatalf("ConfigureMeshEgress: %v", err)
	}
	t.Cleanup(func() { _ = auth.ConfigureMeshEgress(false, "", "") })
}

// MeshPath is one outbound path of a client. Call makes the request against
// origin, an https origin naming the case's host and port.
type MeshPath struct {
	Name string
	Call func(ctx context.Context, origin string) error
}

// RunMeshCases runs every MeshCase through every path, end to end through the
// path's real client. setup points the package's resolver at the case's fake
// DNS and wraps the package's guard in the returned Recorder, restoring both on
// cleanup; refused is the error that guard refuses with. The guard's verdict on
// the resolved address is what is asserted; an admitted dial is stopped by the
// recorder before it connects, so nothing leaves the process.
func RunMeshCases(t *testing.T, paths []MeshPath, refused error, setup func(t *testing.T, c MeshCase) *Recorder) {
	t.Helper()
	for _, c := range MeshCases {
		for _, path := range paths {
			t.Run(c.Name+"/"+path.Name, func(t *testing.T) {
				c.Install(t)
				recorder := setup(t, c)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				err := path.Call(ctx, "https://"+c.Requested())
				got := recorder.Take()
				if len(got) == 0 {
					t.Fatalf("the dial did not run the guard (call error %v)", err)
				}
				for _, v := range got {
					if v.Address != c.Dialed() {
						t.Fatalf("the guard judged %q, want the resolved address %q", v.Address, c.Dialed())
					}
					if c.Admitted && v.Err != nil {
						t.Fatalf("guard(%q for %q) = %v, want admitted", v.Address, c.Requested(), v.Err)
					}
					if !c.Admitted && !errors.Is(v.Err, refused) {
						t.Fatalf("guard(%q for %q) = %v, want %v", v.Address, c.Requested(), v.Err, refused)
					}
				}
				// An admitted dial ends in the recorder's stop, which a client
				// reports in its own words (some report every transport failure
				// as one opaque error), so only a refusal is asserted on the call.
				if !c.Admitted && !errors.Is(err, refused) {
					t.Fatalf("call error = %v, want %v", err, refused)
				}
			})
		}
	}
}
