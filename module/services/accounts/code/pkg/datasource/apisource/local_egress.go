package apisource

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"syscall"
)

// The local egress allowance: how a composition booted on a developer's machine
// reaches a stub OAuth provider and API running beside it (issue #1087), and
// why no deployed composition can use it.
//
// guardDial refuses every address auth.IsPublicDestinationDialAddress does not
// classify as public. That is right for every deployed host, and it also means
// a local host cannot reach an httptest-style provider on its own machine. The
// allowance is a separate admission in front of that guard, not a change to it:
// an address it does not list is judged by guardDial, and so by the shared
// classifier, exactly as before. The public-address policy is reviewable on its
// own, and this file adds nothing to what it refuses or admits.
//
// What it admits, and only that:
//
//   - entries the composition lists explicitly, as `ip:port` — one loopback IP
//     literal and one port. No hostname (the guard sees the resolved IP, so the
//     entry is written as what is dialed), no range, no CIDR, no wildcard, no
//     zone, no IPv4-mapped spelling, no port 0;
//   - loopback addresses only. A stub "beside" a local host is on the same
//     machine, and loopback is the one address class that never leaves the
//     dialing process's own network namespace. Even a host that somehow passed
//     the environment check below could reach only its own namespace — never
//     the metadata service, the cluster network, or another tenant.
//
// It is refused at boot, rather than ignored, outside a local runtime: a
// deployed composition that sets it is a composition whose operator believes
// the host can reach something it cannot, and the place to say so is startup.
// Whether the runtime is local is the caller's answer (see work.go,
// configureLocalEgress); this package only refuses a non-empty allowance it is
// told is not local.

// ErrLocalEgressOutsideLocal reports a non-empty allowance on a runtime that
// is not local. It refuses startup.
var ErrLocalEgressOutsideLocal = errors.New("apisource: the local egress allowance is only permitted in a local runtime")

// ErrInvalidLocalEgress reports an allowance entry that is not exactly one
// loopback IP literal and one port.
var ErrInvalidLocalEgress = errors.New("apisource: invalid local egress entry")

// localEgress is the process-wide allowance, installed once at boot by
// ConfigureLocalEgress. Nil — every deployed host — admits nothing beyond
// guardDial.
var localEgress atomic.Pointer[map[netip.AddrPort]struct{}]

// ConfigureLocalEgress parses value, a comma- or whitespace-separated list of
// `ip:port` entries, and installs it as the allowance every callable-source
// client built afterwards consults. An empty value installs no allowance.
// A non-empty value is refused unless local is true, and a malformed entry is
// refused whole: a partially-understood allowance is not installed.
func ConfigureLocalEgress(value string, local bool) error {
	allowed, err := parseLocalEgress(value)
	if err != nil {
		return err
	}
	if len(allowed) > 0 && !local {
		return ErrLocalEgressOutsideLocal
	}
	if len(allowed) == 0 {
		localEgress.Store(nil)
		return nil
	}
	localEgress.Store(&allowed)
	return nil
}

func parseLocalEgress(value string) (map[netip.AddrPort]struct{}, error) {
	entries := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	allowed := make(map[netip.AddrPort]struct{}, len(entries))
	for _, entry := range entries {
		target, err := netip.ParseAddrPort(entry)
		switch {
		case err != nil:
			return nil, fmt.Errorf("%w %q: write exactly one IP literal and port, such as 127.0.0.1:18080 or [::1]:18080", ErrInvalidLocalEgress, entry)
		case target.String() != entry:
			// A non-canonical spelling (a leading-zero port, an uncompressed
			// IPv6 literal) is refused rather than normalised, so the entry an
			// operator reads is the address that is admitted.
			return nil, fmt.Errorf("%w %q: write it as %s", ErrInvalidLocalEgress, entry, target)
		case target.Addr().Zone() != "" || target.Addr().Is4In6():
			return nil, fmt.Errorf("%w %q: no zone or IPv4-mapped spelling", ErrInvalidLocalEgress, entry)
		case !target.Addr().IsLoopback():
			return nil, fmt.Errorf("%w %q: only a loopback address may be listed", ErrInvalidLocalEgress, entry)
		case target.Port() == 0:
			return nil, fmt.Errorf("%w %q: name the port the stub listens on", ErrInvalidLocalEgress, entry)
		}
		allowed[target] = struct{}{}
	}
	return allowed, nil
}

// withLocalEgress wraps next, the dial guard, with the installed allowance: an
// address it lists exactly is admitted, and every other address is handed to
// next unchanged, so the public-address classifier still judges it. With no
// allowance installed it returns next itself.
func withLocalEgress(next func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	allowed := localEgress.Load()
	if allowed == nil {
		return next
	}
	return func(network, address string, c syscall.RawConn) error {
		if target, err := netip.ParseAddrPort(address); err == nil {
			target = netip.AddrPortFrom(target.Addr().Unmap(), target.Port())
			if _, ok := (*allowed)[target]; ok {
				return nil
			}
		}
		return next(network, address, c)
	}
}
