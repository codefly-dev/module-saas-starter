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
// inside the cluster (a host whose name has an "svc" label, as in
// <service>.<namespace>.svc or <service>.<namespace>.svc.cluster.local). A
// plaintext URL to any other host — an external name, a bare IP, a short name —
// stays refused with the assertion in place, because nothing the operator said
// covers the wire to it.
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
// Kubernetes Service inside the cluster: <service>.<namespace>.svc, optionally
// followed by the cluster domain. The "svc" label may not be one of the first
// two, since "svc.example.com" and "vault.svc.example.com" are somebody else's
// hosts and are externally routable despite carrying the label.
func ClusterServiceHost(host string) bool {
	hostname := host
	if bare, _, err := net.SplitHostPort(host); err == nil {
		hostname = bare
	}
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(hostname), "."), ".")
	for index, label := range labels {
		if index < 2 || label != "svc" {
			continue
		}
		for _, earlier := range labels[:index] {
			if earlier == "" {
				return false
			}
		}
		return true
	}
	return false
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
const Remedy = "use https, or — only for an in-cluster Service address (<service>.<namespace>.svc[.<cluster domain>]) when every in-cluster hop is carried by a mutually authenticated mesh (mTLS) — assert it with the workspace setting internal-transport/mesh-protected=true"
