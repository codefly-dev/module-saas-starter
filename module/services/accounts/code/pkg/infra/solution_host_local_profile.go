package infra

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The local profile: how a developer's own identity is admitted on the host of
// a local cluster, and how every other host refuses it (issue #1063).
//
// A local run signs delivery with the DEVELOPER'S OIDC identity through the
// same Sigstore keyless flow a release uses (codefly-dev/cli#918). A person's
// certificate carries no repository and no workflow, so the workload rule —
// name a repository or a build config — cannot admit it. A person is therefore
// its own signer kind, and it is valid only in a policy that declares itself
// local.
//
// THE QUESTION THIS FILE ANSWERS is how a host that is NOT the developer's
// cluster refuses a local policy, without trusting anything the composition
// controls. The candidates, and why each lost or won:
//
//   - A setting ("this host is local"), a coordinate prefix, or anything in
//     workspace environment. Refused: workspace environment is delivered by
//     the COMPOSITION, so a composition could declare itself local. That is
//     the hole SolutionHostTrustAnchorPath was made a constant to close, and
//     the deleted `local` trust policy died of the same thing — its gate was a
//     string an operator declared.
//   - The trusted root. A local profile and a deployed one use the SAME root:
//     the developer signs through the public-good Fulcio and Rekor, exactly as
//     the release workflow does, so the root cannot tell them apart.
//   - Heuristics about the cluster — a `+k3s` server version, a node's
//     provider ID. Refused: they are conventions, not properties. A deployed
//     cell may run k3s, and a version string proves nothing about whose
//     cluster it is.
//   - The kube-system namespace's UID. Unforgeable through an authenticated API
//     server, but reading it needs a cluster-scoped grant this host does not
//     otherwise hold, to learn something the next option establishes without
//     any grant.
//   - THE CLUSTER'S CERTIFICATE AUTHORITY, proven by a TLS handshake. Chosen.
//
// What the host can know about its own deployment that the composition cannot
// forge is WHICH API SERVER it reviews tokens with. The kubelet projects that
// API server's certificate authority into the pod beside the host's own token,
// and a TLS handshake against that authority ALONE proves the server holds a
// key the authority certified — which only that cluster's control plane does.
// So a local policy pins the SHA-256 of its cluster's CA certificate, and the
// host proves at boot, before any carrier is admitted:
//
//  1. the projected CA bundle contains a certificate with exactly that
//     fingerprint, AND
//  2. the API server this host reviews tokens with completes a TLS handshake
//     whose ONLY root is that certificate.
//
// The second step is the one that cannot be skipped. A CA bundle is a file in
// the pod, and a bundle carrying both the developer's CA and the deployed
// cluster's own would pass step 1 while the host keeps talking to the deployed
// API server. Verifying the handshake against the pinned certificate alone
// refuses that, and it refuses a redirected KUBERNETES_SERVICE_HOST too: a
// server presenting a certificate under the developer's CA needs the private
// key of the developer's cluster.
//
// The cost, stated rather than hidden: a local profile is valid in exactly one
// cluster. Recreating the cluster mints a new CA, so the profile must be
// rewritten with the new fingerprint, and the boot refusal names the
// fingerprints it observed so that rewrite is one copy.
//
// THE RESIDUAL RISK, precisely: the binding is to ONE CLUSTER, not to the word
// "local". Nothing a host can observe distinguishes a developer's cluster from
// a production one by kind, so whoever writes a deployed host's trust anchor
// could deliberately pin THAT cluster's CA and admit a person there. That is
// no new authority — the anchor's writer can already list any workload
// identity it likes — and it cannot happen by accident: a local profile copied
// to another cluster names the wrong authority and refuses the boot. The
// composition can do neither: it writes neither the anchor nor the projected
// CA, and it cannot make a server hold a key the pinned authority certified.

// ErrSolutionHostLocalPolicyOutsideItsCluster reports a local verification
// policy on a host that is not running in the cluster the policy is bound to —
// a deployed host given a local profile, or a local host whose cluster was
// recreated. It refuses the boot; the same error refuses a carrier on a
// verifier whose cluster was never proven.
var ErrSolutionHostLocalPolicyOutsideItsCluster = errors.New("solution host local policy is not bound to this cluster")

// SolutionHostLocalProfile declares a verification policy LOCAL and binds it to
// one cluster.
type SolutionHostLocalProfile struct {
	// ClusterCertificateAuthoritySHA256 is the lowercase hex SHA-256 of the DER
	// bytes of the local cluster's API-server certificate authority — the
	// certificate Kubernetes projects into every pod as the service account's
	// `ca.crt`. Required: a local declaration naming no cluster would bind to
	// every cluster, which is the property the declaration exists to remove.
	ClusterCertificateAuthoritySHA256 string `json:"clusterCertificateAuthoritySha256"`
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (profile *SolutionHostLocalProfile) validate() error {
	if !sha256Hex.MatchString(profile.ClusterCertificateAuthoritySHA256) {
		return fmt.Errorf("verification policy declares itself local with cluster authority %q, which is not a lowercase hex SHA-256: "+
			"a local profile is valid in exactly one cluster, the one whose certificate authority it pins, and a profile naming no cluster would bind to every one",
			profile.ClusterCertificateAuthoritySHA256)
	}
	return nil
}

// SolutionHostClusterAuthority is the one question a local policy asks of the
// cluster: does the API server this host reviews tokens with serve under the
// certificate authority with this fingerprint?
type SolutionHostClusterAuthority interface {
	ServesUnderAuthority(ctx context.Context, fingerprint string) error
}

// ProveLocalCluster proves a local policy is on its own cluster, and from then
// on lets it admit carriers. A deployed policy is bound to no cluster: this
// does nothing for it, and it never needed to.
func (v *keylessBundleVerifier) ProveLocalCluster(ctx context.Context, cluster SolutionHostClusterAuthority) error {
	if v.policy.Local == nil {
		return nil
	}
	if cluster == nil {
		return fmt.Errorf("%w: the verification policy is a local profile and this host has no cluster to prove it against",
			ErrSolutionHostLocalPolicyOutsideItsCluster)
	}
	if err := cluster.ServesUnderAuthority(ctx, v.policy.Local.ClusterCertificateAuthoritySHA256); err != nil {
		return fmt.Errorf("the verification policy on the trust anchor is a local profile bound to the cluster whose certificate authority is %s, "+
			"and this host is not running in it — a deployed host never admits a local profile: %w",
			v.policy.Local.ClusterCertificateAuthoritySHA256, err)
	}
	v.localClusterProven.Store(true)
	return nil
}

// ServesUnderAuthority proves the API server this client reviews tokens with
// presents a certificate chaining to the ONE certificate authority, out of the
// projected CA bundle, whose SHA-256 is fingerprint.
//
// Any HTTP answer is proof — the claim is the server's identity, which the
// completed handshake established, not what it is willing to tell an
// anonymous caller. No credential is sent: the question needs none.
func (c *KubernetesClient) ServesUnderAuthority(ctx context.Context, fingerprint string) error {
	pinned, observed := certificateWithFingerprint(c.authority, fingerprint)
	if pinned == nil {
		return fmt.Errorf("%w: no certificate authority this cluster projects has SHA-256 %s; it projects %s",
			ErrSolutionHostLocalPolicyOutsideItsCluster, fingerprint, observed)
	}
	pool := x509.NewCertPool()
	pool.AddCert(pinned)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/version", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: the API server at %s did not prove it serves under the certificate authority %s: %w",
			ErrSolutionHostLocalPolicyOutsideItsCluster, c.endpoint, fingerprint, err)
	}
	_ = response.Body.Close()
	return nil
}

// certificateWithFingerprint finds the certificate in a PEM bundle whose DER
// SHA-256 is fingerprint, and otherwise reports every fingerprint the bundle
// does hold — the value a recreated local cluster's profile needs.
func certificateWithFingerprint(bundle []byte, fingerprint string) (*x509.Certificate, string) {
	var seen []string
	for rest := bundle; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(certificate.Raw)
		actual := hex.EncodeToString(sum[:])
		if actual == fingerprint {
			return certificate, ""
		}
		seen = append(seen, actual)
	}
	if len(seen) == 0 {
		return nil, "no certificate at all"
	}
	sort.Strings(seen)
	return nil, strings.Join(seen, ", ")
}
