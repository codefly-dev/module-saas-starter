package business

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"

	"github.com/codefly-dev/core/solutionhost"
)

// The rules core states about a manifest and leaves to the host, because only
// the host knows the other bindings it holds and where its cell runs things:
//
//  1. A workload is one (namespace, ServiceAccount) by the cell's rule, and
//     two live manifests on this host never both declare it — so a reviewed
//     token matches at most one manifest. The second is refused as a conflict
//     naming the binding that holds the workload.
//  2. A signer speaks for its domain's bindings only: a manifest may not ask
//     for an authority principal, or deploy a release publisher, that a live
//     binding of another ownership domain holds. The first domain to hold one
//     keeps it. And one principal is one live binding even within a domain —
//     the registry omits a principal two manifests ask for — so a second
//     claim is a conflict naming the holder.
//  3. An endpoint's address is the in-cluster Service of the workload it
//     names, by the rule — "<scheme>://<service>.<namespace>.svc.cluster.local:<port>"
//     — never a free host. A native run's endpoints are its own processes, on
//     the loopback.
//
// They are checked at receipt and again by each pass, against the bindings as
// they stand.

// checkManifestAgainstHost holds a live candidate to the three rules against
// holders, the other bindings' manifests as they stand.
func (rule ModuleNamespaceRule) checkManifestAgainstHost(candidate *solutionhost.ModuleManifest, holders []*solutionhost.ModuleManifest) error {
	if candidate.Removed {
		return nil
	}
	for _, endpoint := range candidate.Endpoints {
		if err := rule.checkEndpointAddress(candidate.Alias, endpoint); err != nil {
			return fmt.Errorf("%w: binding %q endpoint %s/%s: %w", ErrSolutionDeliveryRefused, candidate.Binding, endpoint.Service, endpoint.Name, err)
		}
	}
	for _, held := range holders {
		if held.Removed || held.Binding == candidate.Binding || held.Host.Coordinate != candidate.Host.Coordinate {
			continue
		}
		if rule.Namespace(held.Alias) == rule.Namespace(candidate.Alias) {
			for _, workload := range candidate.Workloads {
				if _, taken := held.WorkloadNamed(workload.Service); taken {
					return fmt.Errorf("%w: workload %s/%s is held by binding %q, and one workload runs as one manifest",
						ErrSolutionDeliveryConflict, rule.Namespace(candidate.Alias), workload.Service, held.Binding)
				}
			}
		}
		if candidate.Authority != nil && held.Authority != nil && held.Authority.Principal == candidate.Authority.Principal {
			if held.Host.Domain == candidate.Host.Domain {
				// The registry would omit both, so the second is refused here
				// rather than taking the first's authority down with it.
				return fmt.Errorf("%w: principal %q is held by binding %q, and one principal is one live binding",
					ErrSolutionDeliveryConflict, candidate.Authority.Principal, held.Binding)
			}
			return fmt.Errorf("%w: principal %q is held by binding %q of domain %q, and this manifest speaks for domain %q",
				ErrSolutionDeliveryDomainNotGranted, candidate.Authority.Principal, held.Binding, held.Host.Domain, candidate.Host.Domain)
		}
		if held.Host.Domain == candidate.Host.Domain {
			continue
		}
		if held.Release.Publisher == candidate.Release.Publisher {
			return fmt.Errorf("%w: release publisher %q is held by binding %q of domain %q, and this manifest speaks for domain %q",
				ErrSolutionDeliveryDomainNotGranted, candidate.Release.Publisher, held.Binding, held.Host.Domain, candidate.Host.Domain)
		}
	}
	return nil
}

// checkEndpointAddress holds one endpoint's address to the cell's rule: an
// http(s) origin with a port and nothing else, whose host is the workload's
// in-cluster Service — or, natively, the loopback.
func (r ModuleNamespaceRule) checkEndpointAddress(module string, endpoint solutionhost.Endpoint) error {
	address, err := url.Parse(endpoint.Address)
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.User != nil ||
		(address.Path != "" && address.Path != "/") || address.RawQuery != "" || address.Fragment != "" {
		return fmt.Errorf("address %q is not an http(s) origin", endpoint.Address)
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("address %q names no port", endpoint.Address)
	}
	host := address.Hostname()
	if r.native {
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return fmt.Errorf("address %q is not this run's loopback", endpoint.Address)
	}
	if want := endpoint.Service + "." + r.Namespace(module) + ".svc.cluster.local"; host != want {
		return fmt.Errorf("address %q is not the workload's in-cluster Service %s", endpoint.Address, want)
	}
	return nil
}

// standingManifests is every binding's manifest as it stands once the given
// replacements apply: an applied manifest is replaced by its binding's
// replacement, and a replacement for a binding nothing applied yet stands as
// well. A withdrawal stands as withdrawn and holds nothing.
func standingManifests(applied []*solutionhost.ModuleManifest, replacements map[string]*solutionhost.ModuleManifest) []*solutionhost.ModuleManifest {
	standing := make([]*solutionhost.ModuleManifest, 0, len(applied)+len(replacements))
	seen := make(map[string]bool, len(applied))
	for _, manifest := range applied {
		seen[manifest.Binding] = true
		if replacement, replaced := replacements[manifest.Binding]; replaced {
			standing = append(standing, replacement)
			continue
		}
		standing = append(standing, manifest)
	}
	bindings := make([]string, 0, len(replacements))
	for binding := range replacements {
		if !seen[binding] {
			bindings = append(bindings, binding)
		}
	}
	slices.Sort(bindings)
	for _, binding := range bindings {
		standing = append(standing, replacements[binding])
	}
	return standing
}
