package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
)

// Mesh egress: the one admission beyond IsPublicDestination, and the one rule
// every guarded egress path applies.
//
// On a cell whose ambient mesh carries egress (Istio ambient with DNS capture,
// for example), the mesh answers DNS for each host the deployment declares as
// external reach with an address it allocated itself — Istio's auto-allocated
// ServiceEntry addresses, 240.240.0.0/16 by default — and routes a connection
// to that address through its egress waypoint to the real host. Such an address
// is reserved space (240.0.0.0/4), so IsPublicDestination refuses it, rightly:
// on its own it says nothing about where the connection goes. Every guarded
// path then refuses a declared host the cell exists to reach.
//
// The rule, for the concrete address a connection is about to reach:
//
//	admitted ⇔ IsPublicDestination(address)
//	         ∨ (the deployment asserted its mesh (internal-transport/mesh-protected=true)
//	            ∧ the client asked for a HOSTNAME, and that host:port is declared
//	            ∧ address lies in the configured mesh egress range)
//
// Everything else stays refused exactly as before: an IP literal (never a
// declared host, whatever range it is in), an undeclared hostname resolving
// into the mesh range, and loopback, link-local, metadata, private and every
// other non-public address whatever name produced it. A declared host that
// resolves OUTSIDE the mesh range is judged by IsPublicDestination alone.
//
// Why both conditions. The range alone is not enough: any name a tenant
// controls can answer with a mesh address, and the mesh then routes it to
// whichever declared host owns that address — so the requested name must be
// one the deployment declared. The name alone is not enough either: the mesh
// range is the only non-public space a declared host is expected to resolve
// into, so a declared name answering with 10.0.0.1 is refused.
//
// It is judged on the address actually dialled (GuardedDialContext resolves,
// then dials each address with the Control hook judging that address), so DNS
// rebinding cannot pass a check made on an earlier answer.
//
// The configuration is the deployment's, read once at boot
// (ConfigureMeshEgress). The declared hosts mirror the environment's egress
// declaration for this service — the same list the cell renders its egress
// ServiceEntries from — because the platform does not yet deliver that
// declaration to the service it describes.

// DialControl is the shape of a net.Dialer Control hook.
type DialControl = func(network, address string, c syscall.RawConn) error

// Resolver is the DNS seam of GuardedDialContext. net.DefaultResolver in
// production; a fake in tests.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DefaultMeshEgressRange is the range a meshed deployment's declared hosts are
// admitted in when it names none: Istio ambient's default IPv4 auto-allocation
// block for ServiceEntry addresses.
const DefaultMeshEgressRange = "240.240.0.0/16"

// meshEgressPortDefault is the port a declared host named without one is
// reached on, as the environment's egress declaration defaults it.
const meshEgressPortDefault = "443"

// meshEgressRangeBounds is where a configured mesh range may lie: the address
// blocks Istio auto-allocates from (IPv4 reserved 240.0.0.0/4 and IPv6
// benchmarking 2001:2::/48). Nothing else is used there — no pod, node,
// Service or metadata address — so admitting a declared host in them can reach
// only what the mesh allocated. A range anywhere else (private, carrier NAT,
// link-local, public) is refused at boot.
var meshEgressRangeBounds = []netip.Prefix{
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:2::/48"),
}

// meshEgressHostPattern is a bare DNS host name of at least two labels,
// lower-case, no wildcard — the shape the environment's egress declaration
// accepts.
var meshEgressHostPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`)

// ErrMeshEgressWithoutMesh reports declared mesh egress on a deployment that
// did not assert its mesh. It refuses startup.
var ErrMeshEgressWithoutMesh = errors.New("mesh egress is configured, but the deployment does not assert a mesh (internal-transport/mesh-protected is not true)")

// ErrInvalidMeshEgress reports a declared host or a range that cannot be
// admitted as written. It refuses startup.
var ErrInvalidMeshEgress = errors.New("invalid mesh egress entry")

type meshEgressPolicy struct {
	hosts  map[string]struct{} // "host:port"
	ranges []netip.Prefix
}

// meshEgress is the process-wide policy, installed once at boot. Nil — every
// deployment that declares no mesh egress — admits nothing beyond
// IsPublicDestination.
var meshEgress atomic.Pointer[meshEgressPolicy]

// ConfigureMeshEgress installs the mesh egress admission every guarded egress
// path consults. meshed is the deployment's mesh assertion; hosts is a comma-
// or whitespace-separated list of `host` or `host:port` entries (port 443 when
// omitted); ranges is a comma- or whitespace-separated list of CIDR prefixes,
// DefaultMeshEgressRange when empty.
//
// Nothing is installed when hosts is empty. Hosts or ranges without the mesh
// assertion are refused, as is any malformed entry: a partially-understood
// admission is never installed.
func ConfigureMeshEgress(meshed bool, hosts, ranges string) error {
	declared, err := parseMeshEgressHosts(hosts)
	if err != nil {
		return err
	}
	prefixes, err := parseMeshEgressRanges(ranges)
	if err != nil {
		return err
	}
	if !meshed && (len(declared) > 0 || len(prefixes) > 0) {
		return ErrMeshEgressWithoutMesh
	}
	if len(declared) == 0 {
		meshEgress.Store(nil)
		return nil
	}
	if len(prefixes) == 0 {
		prefixes = []netip.Prefix{netip.MustParsePrefix(DefaultMeshEgressRange)}
	}
	meshEgress.Store(&meshEgressPolicy{hosts: declared, ranges: prefixes})
	return nil
}

func splitEntries(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
}

func parseMeshEgressHosts(value string) (map[string]struct{}, error) {
	entries := splitEntries(value)
	declared := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		host, port := entry, meshEgressPortDefault
		if name, p, err := net.SplitHostPort(entry); err == nil {
			host, port = name, p
		}
		number, err := strconv.ParseUint(port, 10, 16)
		switch {
		case !meshEgressHostPattern.MatchString(host):
			return nil, fmt.Errorf("%w %q: write a bare lower-case DNS host name, optionally :port — no scheme, path, wildcard or IP address", ErrInvalidMeshEgress, entry)
		case isIPLiteralHost(host):
			return nil, fmt.Errorf("%w %q: an IP address is never a declared host", ErrInvalidMeshEgress, entry)
		case err != nil || number == 0 || strconv.FormatUint(number, 10) != port:
			return nil, fmt.Errorf("%w %q: the port must be a number from 1 to 65535", ErrInvalidMeshEgress, entry)
		}
		declared[net.JoinHostPort(host, port)] = struct{}{}
	}
	return declared, nil
}

// isIPLiteralHost reports whether host would be dialled as an address rather
// than resolved: an IP literal, or a name whose last label is numeric, which
// resolvers may read as an IPv4 address in one of its legacy forms.
func isIPLiteralHost(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	labels := strings.Split(host, ".")
	last := labels[len(labels)-1]
	_, err := strconv.ParseUint(last, 0, 64)
	return err == nil
}

func parseMeshEgressRanges(value string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, entry := range splitEntries(value) {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil || prefix.Masked() != prefix || prefix.Addr().Zone() != "" || prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("%w %q: write a CIDR prefix in canonical form, for example %s", ErrInvalidMeshEgress, entry, DefaultMeshEgressRange)
		}
		if !withinMeshEgressBounds(prefix) {
			return nil, fmt.Errorf("%w %q: a mesh egress range must lie inside 240.0.0.0/4 or 2001:2::/48, the blocks a mesh allocates egress addresses from", ErrInvalidMeshEgress, entry)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func withinMeshEgressBounds(prefix netip.Prefix) bool {
	for _, bound := range meshEgressRangeBounds {
		if prefix.Bits() >= bound.Bits() && bound.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

// canonicalRequest is requested ("host:port", as a client asked for it) as the
// key a declared host is stored under, and false when it names no declarable
// host: unparseable, or an IP literal.
func canonicalRequest(requested string) (string, bool) {
	host, port, err := net.SplitHostPort(requested)
	if err != nil {
		return "", false
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || isIPLiteralHost(host) {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

// meshEgressAdmits reports whether the mesh egress admission covers a dial to
// address for a client that asked for requested.
func meshEgressAdmits(requested string, address netip.Addr) bool {
	policy := meshEgress.Load()
	if policy == nil || !address.IsValid() || address.Zone() != "" {
		return false
	}
	key, ok := canonicalRequest(requested)
	if !ok {
		return false
	}
	if _, declared := policy.hosts[key]; !declared {
		return false
	}
	address = address.Unmap()
	for _, prefix := range policy.ranges {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// AdmitsDestination is the egress rule: whether a connection a client asked to
// make to requested ("host:port") may reach address, the concrete address it
// resolved to. See the comment at the top of this file.
func AdmitsDestination(requested string, address netip.Addr) bool {
	return IsPublicDestination(address) || meshEgressAdmits(requested, address)
}

// AdmitsDestinationDialAddress is AdmitsDestination for a dialer's "ip:port".
// An address it cannot parse is refused.
func AdmitsDestinationDialAddress(requested, address string) bool {
	parsed, ok := parseDialAddress(address)
	return ok && AdmitsDestination(requested, parsed)
}

// GuardedDialContext returns a DialContext that resolves the requested host
// with resolver and dials each answer in turn as an IP literal, with control
// building the Control hook for the dial from the "host:port" the client asked
// for. Because what is dialled is the literal, the hook judges the exact
// address the connection reaches — a later DNS answer cannot replace it — while
// still knowing which host was asked for, which the mesh egress admission
// needs and a bare Control hook never sees.
func GuardedDialContext(dialer net.Dialer, resolver Resolver, control func(requested string) DialControl) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		var answers []netip.Addr
		if literal, err := netip.ParseAddr(host); err == nil {
			answers = []netip.Addr{literal}
		} else {
			answers, err = resolver.LookupNetIP(ctx, lookupNetwork(network), host)
			if err != nil {
				return nil, err
			}
			if len(answers) == 0 {
				return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
			}
		}
		d := dialer
		d.Control = control(address)
		var dialErr error
		for _, answer := range answers {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(answer.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = errors.Join(dialErr, err)
		}
		return nil, dialErr
	}
}

func lookupNetwork(network string) string {
	switch network {
	case "tcp4", "udp4":
		return "ip4"
	case "tcp6", "udp6":
		return "ip6"
	default:
		return "ip"
	}
}
