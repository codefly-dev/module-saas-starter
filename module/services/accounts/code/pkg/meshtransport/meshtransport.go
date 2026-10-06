// Package meshtransport reads the one operator assertion that lets accounts
// carry trust-bearing traffic over plaintext HTTP to a routable in-cluster
// address: that every hop inside the cell is wrapped by a mutually
// authenticated mesh (Istio ambient's ztunnel, for example), so the connection
// is protected out of band even though the URL says http.
//
// The hop that needs it here is accounts' own Vault connection. Vault inside a
// cell listens in-mesh without TLS of its own — in-cluster transport security
// is the mesh's, not a service's — so a meshed cell has no https URL to give,
// while the wire carries the AppRole credential, the Ed25519 signing key and
// every value Transit seals. A hostname alone cannot settle it (".svc" says
// nothing about whether a mesh wraps the wire), and neither can a silent
// allowance: the operator states it out loud and can be held to it.
//
// It is fail-closed on two counts. Only the exact value "true" asserts the
// mesh; unset, empty or "false" keeps plaintext refused; any other value fails
// startup rather than being read as either answer, because a misspelled
// assertion must not pass for an absent one nor for a present one. And the
// assertion covers only what a mesh can cover: a Kubernetes Service address
// inside this cluster, which is exactly `<service>.<namespace>.svc` or that
// name followed by the default cluster domain. A plaintext URL to any other
// host — an external name, a bare IP, a short name, or an `svc` label buried in
// somebody else's domain — stays refused with the assertion in place, because
// nothing the operator said covers the wire to it.
//
// This is deliberately the same rule, group, key, value handling and remedy
// sentence that the composed modules apply to their own in-cluster hops. It is
// reimplemented rather than imported because a module never imports another
// module's internals, and the host sits beneath all of them — so the shared
// thing is the contract, and each side owns its copy of the check.
package meshtransport

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Group and Key name the workspace setting the assertion is read from.
const (
	Group = "internal-transport"
	Key   = "mesh-protected"
)

// Source is the workspace-configuration accessor the assertion is read through —
// codefly.For(ctx) in production, a fake in tests.
type Source interface {
	WorkspaceValue(group, key string) (string, error)
}

// Protected reports whether the composition asserted that the cell's mesh
// protects plaintext in-cluster hops.
//
// The value is trimmed first: configuration delivered as a file carries the
// trailing newline the file ends with, and a correct assertion must not be
// refused over a byte nobody can see.
func Protected(source Source) (bool, error) {
	raw, err := source.WorkspaceValue(Group, Key)
	if err != nil {
		// The SDK reports an absent value as an error; absent is "not asserted".
		return false, nil
	}
	switch value := strings.TrimSpace(raw); value {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s/%s is %q: set it to true only when every in-cluster hop is carried by a mutually authenticated mesh, or false (the default) otherwise", Group, Key, raw)
	}
}

// ClusterServiceHost reports whether host (a hostname or host:port) names a
// Kubernetes Service inside this cluster. It accepts exactly two shapes:
//
//	<service>.<namespace>.svc
//	<service>.<namespace>.svc.cluster.local
//
// and nothing else. Both labels before `svc` must be valid DNS-1123 labels.
//
// The suffix is matched against `cluster.local` rather than being accepted as
// "whatever follows svc", because an arbitrary suffix is not evidence of a
// cluster domain: `vault.vault.svc.example.com` resolves on the public
// internet, and admitting it would let the mesh assertion authorize a
// destination the mesh demonstrably does not cover. Nothing in the platform
// supplies an authoritative cluster-domain value to compare against, so the
// trusted suffix is Kubernetes' default and only that; a cell with a custom
// cluster domain uses the unqualified `<service>.<namespace>.svc` form, which
// resolves in-cluster under any domain. If a configuration seam for the cluster
// domain ever exists, this is where it is read — until then, inferring one is
// the bug.
//
// This is deliberately stricter than the matcher the composed modules carry,
// which accepts any `svc` label from the third position on. That difference is
// not drift to be reconciled by loosening: a host must not admit a destination
// on the strength of a suffix it cannot verify, whatever anything above it
// does, and two independent refusals is the intended state.
func ClusterServiceHost(host string) bool {
	hostname := host
	if bare, _, err := net.SplitHostPort(host); err == nil {
		hostname = bare
	}
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(hostname), "."), ".")
	switch len(labels) {
	case 3:
	case 5:
		if labels[3] != "cluster" || labels[4] != "local" {
			return false
		}
	default:
		return false
	}
	if labels[2] != "svc" {
		return false
	}
	return dnsLabel(labels[0]) && dnsLabel(labels[1])
}

// dnsLabel reports whether label is a valid DNS-1123 label, so an empty or
// malformed component cannot ride through on the shape of the name alone.
func dnsLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 {
		return false
	}
	for index := range len(label) {
		character := label[index]
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
		case character == '-' && index != 0 && index != len(label)-1:
		default:
			return false
		}
	}
	return true
}

// Admits reports whether a plaintext http URL may be used: only when the mesh
// is asserted and the URL names an in-cluster Service.
func Admits(protected bool, rawURL string) bool {
	if !protected {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return false
	}
	return ClusterServiceHost(parsed.Host)
}

// Remedy is the sentence every plaintext refusal ends with, so an operator
// reads the one supported way through off the error rather than off a doc.
const Remedy = "use https, or — only for an in-cluster Service address (<service>.<namespace>.svc, optionally followed by cluster.local) when every in-cluster hop is carried by a mutually authenticated mesh (mTLS) — assert it with the workspace setting internal-transport/mesh-protected=true"
