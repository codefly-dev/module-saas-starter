package infra

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// The local profile: how a developer's own identity is admitted on the host of
// a local cluster, and how every other host refuses it (issue #1063).
//
// A local run signs with the developer's own platform key. A person is its
// own signer kind because that key may be admitted only by a policy bound to
// the developer's cluster.
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
//   - Heuristics about the cluster — a `+k3s` server version, a node's
//     provider ID. Refused: they are conventions, not properties. A deployed
//     cell may run k3s, and a version string proves nothing about whose
//     cluster it is.
//   - The kube-system namespace's UID. Unforgeable through an authenticated API
//     server, but reading it needs a cluster-scoped grant this host does not
//     otherwise hold, to learn something the next option establishes without
//     any grant.
//   - THE CLUSTER'S CERTIFICATE AUTHORITY, held for the life of the process,
//     AND THE CLUSTER'S ACCEPTANCE OF THIS POD'S OWN TOKEN. Chosen.
//
// What the host can know about its own deployment without the composition is
// two things the KUBELET puts in the pod: the certificate authority of the API
// server (the service account's `ca.crt`) and this pod's own service-account
// token, which the cluster's API server minted for it. So a local policy pins
// the SHA-256 of its cluster's CA certificate, and at boot, before any carrier
// is admitted, the host requires:
//
//  1. the projected CA bundle is EXACTLY the pinned certificate — one
//     certificate, that fingerprint, nothing else. A bundle carrying any other
//     root is refused rather than reasoned about: a bundle holding the
//     developer's CA beside a deployed cluster's own is the shape every
//     confusion between the two needs, and refusing it removes the case
//     instead of claiming to withstand it;
//  2. the API server the host reaches, over a transport whose ONLY root is the
//     pinned certificate and which follows no redirect, authenticates this
//     pod's OWN projected token (a TokenReview of the host's own credential) as
//     exactly THIS pod — the namespace, service account, pod name and pod UID
//     the token's own claims name, the namespace also matching the
//     kubelet-projected namespace file. A genuine API
//     server accepts only tokens its own cluster minted, so an endpoint that
//     merely holds a certificate under the pinned authority — the developer's
//     real cluster, relayed to a deployed pod — refuses the deployed pod's token;
//  3. and from then on EVERY call this host makes to the cluster — each
//     TokenReview that authorises a delivery, each pod read — goes over that
//     pinned, redirect-refusing transport, for the life of the process. A
//     backend or DNS switch after boot to a server under any other authority
//     breaks authentication rather than silently continuing under a proof
//     made against someone else.
//
// The cost, stated rather than hidden: a local profile is valid in exactly one
// cluster. Recreating the cluster mints a new CA, so the profile must be
// rewritten with the new fingerprint, and the boot refusal names the
// fingerprints it observed so that rewrite is one copy.
//
// WHAT IS GUARANTEED, AND WHAT IS NOT. Guaranteed: a host whose pod carries
// the CA bundle and the service-account token of any cluster other than the
// pinned one refuses the profile at boot — whatever its workspace environment,
// its coordinate or its KUBERNETES_SERVICE_HOST/PORT say, because a steered
// address must still present a chain to the pinned authority and accept this
// pod's token. NOT guaranteed, precisely:
//
//   - the binding is to ONE CLUSTER, not to the word "local". Nothing a host
//     can observe distinguishes a developer's cluster from a production one by
//     kind, so whoever writes a deployed host's trust anchor could deliberately
//     pin THAT cluster's CA and admit a person there. That is no new authority —
//     the anchor's writer can already pin any signing key — and it
//     cannot happen by accident: a copied profile names the wrong authority;
//   - an attacker who can REPLACE this pod's projected CA bundle with exactly
//     the pinned certificate (pod-spec control, or a write to the namespace's
//     `kube-root-ca.crt` ConfigMap the kubelet projects, which the control plane
//     reconciles back), AND steer KUBERNETES_SERVICE_HOST/PORT, AND hold the
//     pinned cluster's CA private key to run a fake API server that accepts any
//     token, defeats the check. Such an attacker already controls every
//     Kubernetes decision this host makes — TokenReview, pod reads — so the
//     local profile adds no capability it did not have; the composition holds
//     none of the three by itself;
//   - a CA is not a cluster boundary when it is shared or cloned. Clusters
//     sharing one authority are one cluster to this check, and anyone holding
//     that authority's key can run a server the check accepts and answer its
//     TokenReview as it likes — whether or not the genuine servers accept each
//     other's tokens.

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

// SolutionHostClusterAuthority is how a local policy is bound to a cluster: the
// client this host uses for every cluster call proves it reaches the cluster
// whose certificate authority has this fingerprint, and from then on trusts
// nothing else.
type SolutionHostClusterAuthority interface {
	BindToAuthority(ctx context.Context, fingerprint string) error
}

// BindToAuthority binds this client — the one every TokenReview and pod read
// goes through — to the single certificate authority whose SHA-256 is
// fingerprint, and proves the API server it reaches is that cluster's.
//
// In order, each refusing the boot:
//
//  1. the projected CA bundle must be EXACTLY one certificate with that
//     fingerprint;
//  2. over a transport trusting only that certificate and following no
//     redirect, the API server must authenticate this pod's own projected
//     token as exactly this pod: the namespace, service account, pod name and
//     pod UID the token names;
//  3. only then does that transport replace this client's, for the life of the
//     process — so it is the transport every later cluster call uses.
//
// Nothing is changed when any step fails.
func (c *KubernetesClient) BindToAuthority(ctx context.Context, fingerprint string) error {
	pinned, err := exactlyTheAuthority(c.authority, fingerprint)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(pinned)
	candidate := *c
	candidate.http = newKubernetesHTTPClient(roots)
	if err := candidate.reviewOwnToken(ctx); err != nil {
		return fmt.Errorf("%w: the API server at %s, reached under certificate authority %s alone, did not accept this pod's own token, "+
			"so it is not the cluster this pod runs in: %w", ErrSolutionHostLocalPolicyOutsideItsCluster, c.endpoint, fingerprint, err)
	}
	c.http = candidate.http
	return nil
}

// reviewOwnToken asks the API server whether it authenticates the token the
// kubelet projected into THIS pod, and requires the identity it answers with to
// be THIS pod's: the same namespace, service account, pod name and pod UID the
// token itself names.
//
// The expected identity comes from platform-owned inputs only: the projected
// token's own `kubernetes.io` claims, which the cluster wrote and the kubelet
// projected, and the kubelet-projected namespace file — never from workspace
// environment, which the composition delivers. The claims are read, not
// verified: verifying them is what the TokenReview asks the API server to do,
// and this compares its answer against them. That catches an answer about
// some OTHER credential — another service account, another pod, a token with
// no pod — rather than about the one presented. It does not, and cannot, stop a
// control plane that is itself malicious and copies the expected claims back;
// that limit is the CA binding's, stated where the binding is.
//
// No audience is sent, deliberately and only here: a review with none is
// checked against the API server's OWN audiences, which are exactly what the
// host's in-cluster token is minted for. Every review of a CALLER's token
// still names and checks the host's audience (reviewToken).
func (c *KubernetesClient) reviewOwnToken(ctx context.Context) error {
	token, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return fmt.Errorf("%w: cannot read this service's own token: %w", ErrKubernetesUnavailable, err)
	}
	namespace, err := os.ReadFile(c.namespacePath)
	if err != nil {
		return fmt.Errorf("%w: cannot read this service's namespace: %w", ErrKubernetesUnavailable, err)
	}
	own, err := boundTokenIdentity(strings.TrimSpace(string(token)))
	if err != nil {
		return err
	}
	if projected := strings.TrimSpace(string(namespace)); own.Namespace != projected {
		return fmt.Errorf("%w: this pod's own token names namespace %q, and the kubelet projected namespace %q",
			ErrTokenRejected, own.Namespace, projected)
	}
	body, err := json.Marshal(tokenReviewRequest{
		APIVersion: "authentication.k8s.io/v1",
		Kind:       "TokenReview",
		Spec:       tokenReviewRequestSpec{Token: strings.TrimSpace(string(token))},
	})
	if err != nil {
		return err
	}
	var reviewed tokenReviewResponse
	if err := c.post(ctx, "/apis/authentication.k8s.io/v1/tokenreviews", body, &reviewed); err != nil {
		return err
	}
	if !reviewed.Status.Authenticated {
		return fmt.Errorf("%w: this pod's own token was refused: %s", ErrTokenRejected, reviewed.Status.Error)
	}
	account, reviewedNamespace, err := parseServiceAccountUsername(reviewed.Status.User.Username)
	if err != nil {
		return err
	}
	answered := ReviewedToken{
		ServiceAccount: account,
		Namespace:      reviewedNamespace,
		PodName:        firstClaim(reviewed.Status.User.Extra, podNameClaim),
		PodUID:         firstClaim(reviewed.Status.User.Extra, podUIDClaim),
	}
	if answered != own {
		return fmt.Errorf("%w: the API server authenticated this pod's own token as %s/%s (pod %q, uid %q), "+
			"and the token names %s/%s (pod %q, uid %q); an answer about another identity is not an answer about this pod",
			ErrTokenRejected, answered.Namespace, answered.ServiceAccount, answered.PodName, answered.PodUID,
			own.Namespace, own.ServiceAccount, own.PodName, own.PodUID)
	}
	return nil
}

// boundTokenIdentity reads the identity a projected service-account token
// names in its `kubernetes.io` claims, requiring every part of it: a token that
// names no pod is not the bound token the kubelet projects.
//
// The payload is DECODED, not verified — see reviewOwnToken for why that is
// the right division of labour.
func boundTokenIdentity(token string) (ReviewedToken, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ReviewedToken{}, fmt.Errorf("%w: this pod's own token is not a JWT", ErrTokenRejected)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ReviewedToken{}, fmt.Errorf("%w: this pod's own token has an undecodable payload: %w", ErrTokenRejected, err)
	}
	var claims struct {
		Kubernetes struct {
			Namespace string `json:"namespace"`
			Pod       struct {
				Name string `json:"name"`
				UID  string `json:"uid"`
			} `json:"pod"`
			ServiceAccount struct {
				Name string `json:"name"`
			} `json:"serviceaccount"`
		} `json:"kubernetes.io"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ReviewedToken{}, fmt.Errorf("%w: this pod's own token has unreadable claims: %w", ErrTokenRejected, err)
	}
	identity := ReviewedToken{
		ServiceAccount: claims.Kubernetes.ServiceAccount.Name,
		Namespace:      claims.Kubernetes.Namespace,
		PodName:        claims.Kubernetes.Pod.Name,
		PodUID:         claims.Kubernetes.Pod.UID,
	}
	if identity.ServiceAccount == "" || identity.Namespace == "" || identity.PodName == "" || identity.PodUID == "" {
		return ReviewedToken{}, fmt.Errorf("%w: this pod's own token does not name its namespace, service account, pod and pod UID, "+
			"so it is not the bound token the kubelet projects", ErrTokenRejected)
	}
	return identity, nil
}

// exactlyTheAuthority returns the projected bundle's one certificate when it is
// exactly one certificate with this fingerprint, and otherwise refuses naming
// every fingerprint the bundle holds — the value a recreated local cluster's
// profile needs.
func exactlyTheAuthority(bundle []byte, fingerprint string) (*x509.Certificate, error) {
	var certificates []*x509.Certificate
	var seen []string
	// Every byte is accounted for: whitespace between blocks, and blocks that
	// decode. pem.Decode alone SKIPS anything before a block it can decode —
	// junk, or a malformed block whose END it cannot find — so each step
	// requires the remainder to START with a block and the decoded span to hold
	// exactly one BEGIN line. "Exactly the pinned certificate" then means
	// exactly that, rather than "one certificate survived parsing".
	rest := bytes.TrimLeft(bundle, " \t\r\n")
	for len(rest) > 0 {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN ")) {
			return nil, fmt.Errorf("%w: the projected certificate authority bundle carries bytes that are not a certificate",
				ErrSolutionHostLocalPolicyOutsideItsCluster)
		}
		block, next := pem.Decode(rest)
		if block == nil || bytes.Count(rest[:len(rest)-len(next)], []byte("-----BEGIN ")) != 1 {
			return nil, fmt.Errorf("%w: the projected certificate authority bundle holds a malformed PEM block",
				ErrSolutionHostLocalPolicyOutsideItsCluster)
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, fmt.Errorf("%w: the projected certificate authority bundle holds a %q block, and it may hold exactly one certificate",
				ErrSolutionHostLocalPolicyOutsideItsCluster, block.Type)
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: the projected certificate authority bundle holds a certificate that does not parse: %w",
				ErrSolutionHostLocalPolicyOutsideItsCluster, err)
		}
		sum := sha256.Sum256(certificate.Raw)
		certificates = append(certificates, certificate)
		seen = append(seen, hex.EncodeToString(sum[:]))
		rest = bytes.TrimLeft(next, " \t\r\n")
	}
	if len(certificates) == 1 && seen[0] == fingerprint {
		return certificates[0], nil
	}
	sort.Strings(seen)
	observed := "no certificate at all"
	if len(seen) > 0 {
		observed = strings.Join(seen, ", ")
	}
	return nil, fmt.Errorf("%w: a local profile requires the projected certificate authority bundle to be exactly the one certificate with SHA-256 %s; "+
		"this cluster projects %s", ErrSolutionHostLocalPolicyOutsideItsCluster, fingerprint, observed)
}
