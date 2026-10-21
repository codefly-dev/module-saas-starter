package infra_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"accounts/internal/deliverytest"
	"accounts/pkg/infra"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// A local host admits a named developer as a delivery signer (issue #1063),
// and every other host refuses the same document.
//
// The developer's DSSE envelope is checked with the same core verifier as a
// release. Admitting a person changes the pins, never the signature checks.
const hostNamespace = "platform"

// cluster is a Kubernetes control plane as a host sees one: a certificate
// authority, an API server whose serving certificate that authority issued,
// and the service-account tokens it minted — the only tokens a genuine API
// server authenticates.
type cluster struct {
	server       *httptest.Server
	authority    *x509.Certificate
	authorityKey *ecdsa.PrivateKey
	fingerprint  string
	// hostToken is the token this cluster minted for a host pod running in it.
	hostToken string
	// serving is the certificate the API server presents; swappable, so a test
	// can move the endpoint to another backend after a proof.
	serving atomic.Pointer[tls.Certificate]
	// acceptAnyToken makes this a FAKE API server — one run by whoever holds
	// the authority's key — that authenticates every token it is shown.
	acceptAnyToken atomic.Bool
	// redirectReviews answers every TokenReview with a redirect to a path that
	// authenticates anything.
	redirectReviews atomic.Bool
	// answerAs, when set, is the identity the API server answers an
	// authenticated review with, instead of the identity the token names.
	answerAs atomic.Pointer[podIdentity]
}

// podIdentity is what a bound service-account token names and what a
// TokenReview answers with.
type podIdentity struct {
	namespace, serviceAccount, pod, uid string
}

// hostPod is the host's own pod in every test cluster.
var hostPod = podIdentity{namespace: hostNamespace, serviceAccount: "accounts", pod: "accounts-0", uid: "0f0f0f0f-0000-4000-8000-000000000000"}

// boundToken is a projected service-account token as the kubelet writes it: a
// JWT whose `kubernetes.io` claims name the pod. Its signature is not the
// test's concern — the API server is what verifies a token — so it is opaque,
// and nonce makes each cluster's token its own.
func boundToken(identity podIdentity, nonce string) string {
	claims, err := json.Marshal(map[string]any{
		"iss": "https://kubernetes.default.svc.cluster.local",
		"sub": "system:serviceaccount:" + identity.namespace + ":" + identity.serviceAccount,
		"jti": nonce,
		"kubernetes.io": map[string]any{
			"namespace":      identity.namespace,
			"pod":            map[string]string{"name": identity.pod, "uid": identity.uid},
			"serviceaccount": map[string]string{"name": identity.serviceAccount, "uid": "5a5a5a5a-0000-4000-8000-000000000000"},
		},
	})
	if err != nil {
		panic(err)
	}
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(claims) + "." + nonce
}

func newCluster(t *testing.T) *cluster {
	t.Helper()
	authorityKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	authority := selfSignedAuthority(t, authorityKey, "cluster-ca")
	sum := sha256.Sum256(authority.Raw)
	c := &cluster{
		authority: authority, authorityKey: authorityKey,
		fingerprint: hex.EncodeToString(sum[:]),
		hostToken:   boundToken(hostPod, "minted-by-"+hex.EncodeToString(sum[:4])),
	}
	c.serving.Store(servingCertificateUnder(t, authority, authorityKey))

	mux := http.NewServeMux()
	review := func(always bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Spec struct {
					Token     string   `json:"token"`
					Audiences []string `json:"audiences"`
				} `json:"spec"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			response := map[string]any{"authenticated": false, "error": "token not minted by this cluster"}
			if always || c.acceptAnyToken.Load() || request.Spec.Token == c.hostToken {
				answer := hostPod
				if override := c.answerAs.Load(); override != nil {
					answer = *override
				}
				extra := map[string][]string{}
				if answer.pod != "" {
					extra["authentication.kubernetes.io/pod-name"] = []string{answer.pod}
				}
				if answer.uid != "" {
					extra["authentication.kubernetes.io/pod-uid"] = []string{answer.uid}
				}
				response = map[string]any{
					"authenticated": true,
					"audiences":     request.Spec.Audiences,
					"user": map[string]any{
						"username": "system:serviceaccount:" + answer.namespace + ":" + answer.serviceAccount,
						"extra":    extra,
					},
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": response})
		}
	}
	mux.HandleFunc("/apis/authentication.k8s.io/v1/tokenreviews", func(w http.ResponseWriter, r *http.Request) {
		if c.redirectReviews.Load() {
			http.Redirect(w, r, "/redirected/tokenreviews", http.StatusTemporaryRedirect)
			return
		}
		review(false)(w, r)
	})
	mux.HandleFunc("/redirected/tokenreviews", review(true))

	c.server = httptest.NewUnstartedServer(mux)
	c.server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Per connection, so a swapped certificate is what the NEXT handshake
		// presents. (GetCertificate would not do: a client dialling an IP sends
		// no SNI, and Go then serves Certificates[0], which httptest fills.)
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{*c.serving.Load()}}, nil
		},
	}
	c.server.StartTLS()
	t.Cleanup(c.server.Close)
	return c
}

// servingCertificateUnder issues an API-server certificate for the loopback
// address under an authority.
func servingCertificateUnder(t *testing.T, authority *x509.Certificate, authorityKey *ecdsa.PrivateKey) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "kube-apiserver"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}, authority, key.Public(), authorityKey)
	require.NoError(t, err)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func selfSignedAuthority(t *testing.T, key *ecdsa.PrivateKey, name string) *x509.Certificate {
	t.Helper()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	require.NoError(t, err)
	parsed, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return parsed
}

func pemOf(certificates ...*x509.Certificate) []byte {
	var out []byte
	for _, certificate := range certificates {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})...)
	}
	return out
}

// host is a host pod running in this cluster, unmodified: the kubelet projected
// this cluster's CA and a token this cluster minted, and the API address is the
// cluster's own.
func (c *cluster) host(t *testing.T) *infra.KubernetesClient {
	t.Helper()
	return infra.NewKubernetesClientInCluster(t, c.server.URL, pemOf(c.authority), c.hostToken, hostNamespace)
}

// hostForControl binds an unmodified host in this cluster.
func (c *cluster) hostForControl(t *testing.T) error {
	t.Helper()
	return c.host(t).BindToAuthority(context.Background(), c.fingerprint)
}

// localPolicy is the local profile a developer's host is given: bound to its
// cluster, listing the developer as a person who speaks for the local domain.
func localPolicy(t *testing.T, fingerprint string) *infra.SolutionHostVerificationPolicy {
	return &infra.SolutionHostVerificationPolicy{
		Local:   &infra.SolutionHostLocalProfile{ClusterCertificateAuthoritySHA256: fingerprint},
		Signers: []infra.SolutionHostSigner{{Name: "developer", Kind: solutionhost.KeyKindPerson, KeyID: "example-developer", PublicKey: deliverytest.PublicKey(t, "example-developer"), Domains: []string{verifierTestDomain}}},
	}
}

type hostVerifier interface {
	Verifier() (*solutionhost.Verifier, error)
	SignerDomains() map[string][]string
	ProveLocalCluster(context.Context, infra.SolutionHostClusterAuthority) error
}

func verifierFor(t *testing.T, policy *infra.SolutionHostVerificationPolicy) hostVerifier {
	t.Helper()
	verifier, err := infra.NewSolutionHostKeyVerifierFrom(policy)
	require.NoError(t, err)
	return verifier
}

func verifyPresence(t *testing.T, verifier hostVerifier, carrier []byte) (*solutionhost.Delivered, error) {
	t.Helper()
	keys, err := verifier.Verifier()
	if err != nil {
		return nil, err
	}
	signed, err := solutionhost.ParseSigned(carrier)
	if err != nil {
		return nil, err
	}
	return solutionhost.VerifyDelivered(context.Background(), signed, keys)
}

func TestALocalHostAdmitsAListedDeveloper(t *testing.T) {
	local := newCluster(t)
	carrier := deliverytest.Carrier(t, bindingPayload(t), "example-developer")
	verifier := verifierFor(t, localPolicy(t, local.fingerprint))
	_, err := verifyPresence(t, verifier, carrier)
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	require.NoError(t, verifier.ProveLocalCluster(context.Background(), local.host(t)))
	delivered, err := verifyPresence(t, verifier, carrier)
	require.NoError(t, err)
	require.Equal(t, "example-developer", delivered.Signer().KeyID)
	require.Equal(t, solutionhost.KeyKindPerson, delivered.Signer().Kind)
	require.Equal(t, map[string][]string{"example-developer": {verifierTestDomain}}, verifier.SignerDomains())
}

func TestTheSameDocumentIsRefusedByADeployedHost(t *testing.T) {
	local, deployed := newCluster(t), newCluster(t)
	carrier := deliverytest.Carrier(t, bindingPayload(t), "example-developer")
	admitting := verifierFor(t, localPolicy(t, local.fingerprint))
	require.NoError(t, admitting.ProveLocalCluster(context.Background(), local.host(t)))
	_, err := verifyPresence(t, admitting, carrier)
	require.NoError(t, err)
	t.Run("under its deployed policy", func(t *testing.T) {
		verifier := verifierFor(t, acceptedPolicy(t))
		require.NoError(t, verifier.ProveLocalCluster(context.Background(), deployed.host(t)))
		_, err := verifyPresence(t, verifier, carrier)
		require.ErrorContains(t, err, "untrusted key ID")
	})
	t.Run("handed the local profile", func(t *testing.T) {
		verifier := verifierFor(t, localPolicy(t, local.fingerprint))
		err := verifier.ProveLocalCluster(context.Background(), deployed.host(t))
		require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
		require.ErrorContains(t, err, deployed.fingerprint)
		_, err = verifyPresence(t, verifier, carrier)
		require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	})
}

func TestALocalHostRefusesAnUnlistedPerson(t *testing.T) {
	local := newCluster(t)
	verifier := verifierFor(t, localPolicy(t, local.fingerprint))
	require.NoError(t, verifier.ProveLocalCluster(context.Background(), local.host(t)))
	_, err := verifyPresence(t, verifier, deliverytest.Carrier(t, bindingPayload(t), "another-developer"))
	require.ErrorContains(t, err, "untrusted key ID")
}

func TestAPersonSignerIsRefusedAtValidate(t *testing.T) {
	fingerprint := strings.Repeat("0", 64)
	for name, edit := range map[string]func(*infra.SolutionHostVerificationPolicy){
		"not local":  func(p *infra.SolutionHostVerificationPolicy) { p.Local = nil },
		"no cluster": func(p *infra.SolutionHostVerificationPolicy) { p.Local.ClusterCertificateAuthoritySHA256 = "" },
		"malformed cluster": func(p *infra.SolutionHostVerificationPolicy) {
			p.Local.ClusterCertificateAuthoritySHA256 = "SHA256:" + fingerprint
		},
	} {
		t.Run(name, func(t *testing.T) {
			policy := localPolicy(t, fingerprint)
			edit(policy)
			require.Error(t, policy.Validate())
		})
	}
	mixed := localPolicy(t, fingerprint)
	mixed.Signers = append(mixed.Signers, acceptedPolicy(t).Signers...)
	require.NoError(t, mixed.Validate())
}

// The boot path a host takes, from the trust anchor's files: a local profile
// decodes and validates, and in any other cluster it is refused before a single
// carrier is read. And a policy that lists a person without declaring itself
// local does not load at all.
func TestALocalPolicyIsRefusedAtBootOnADeployedHost(t *testing.T) {
	local := newCluster(t)
	deployed := newCluster(t)
	anchor := t.TempDir()
	writePolicy(t, anchor, localPolicy(t, local.fingerprint))

	verifier, err := infra.ReadSolutionHostKeyVerifier(anchor)
	require.NoError(t, err, "a local profile is a valid policy document")
	require.NoError(t, verifier.ProveLocalCluster(context.Background(), local.host(t)),
		"the control: on its own cluster the profile is proven")

	verifier, err = infra.ReadSolutionHostKeyVerifier(anchor)
	require.NoError(t, err)
	err = verifier.ProveLocalCluster(context.Background(), deployed.host(t))
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	require.ErrorContains(t, err, "refuses a local profile")

	err = verifier.ProveLocalCluster(context.Background(), nil)
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster,
		"a local profile on a host with no cluster to prove it against is refused, not waved through")

	notLocal := localPolicy(t, local.fingerprint)
	notLocal.Local = nil
	writePolicy(t, anchor, notLocal)
	_, err = infra.ReadSolutionHostKeyVerifier(anchor)
	require.ErrorIs(t, err, infra.ErrSolutionHostTrustRootUnavailable)
	require.ErrorContains(t, err, "admitted only by a policy that declares itself local")
}

// ATTACK (review finding 1): a deployed pod handed the local profile, its CA
// bundle carrying the developer's authority BESIDE the deployed cluster's, and
// its API address steered to a server under the developer's authority that
// authenticates every token. The bundle is refused for not being exactly the
// pinned certificate, before any server is asked anything.
//
// Mutation: accept a bundle that merely CONTAINS the pin, and this test fails.
func TestAMixedAuthorityBundleIsRefusedAtBoot(t *testing.T) {
	local := newCluster(t)
	deployed := newCluster(t)
	local.acceptAnyToken.Store(true)

	steered := infra.NewKubernetesClientInCluster(t, local.server.URL,
		pemOf(local.authority, deployed.authority), deployed.hostToken, hostNamespace)
	err := steered.BindToAuthority(context.Background(), local.fingerprint)
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	require.ErrorContains(t, err, "exactly the one certificate")

	for name, bundle := range map[string][]byte{
		"the pin twice":           pemOf(local.authority, local.authority),
		"the pin and a key":       append(pemOf(local.authority), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}})...),
		"the pin and stray bytes": append(pemOf(local.authority), []byte("not a certificate")...), // FOLLOW-UP (review round 2, P3): material BEFORE the pin, which
		// pem.Decode alone skips. Mutation: let the parser skip it, and these fail.
		"junk before the pin": append([]byte("not a certificate\n"), pemOf(local.authority)...),
		"an undecodable certificate block before the pin": append(
			[]byte("-----BEGIN CERTIFICATE-----\n!!not base64!!\n-----END CERTIFICATE-----\n"), pemOf(local.authority)...),
		"an unterminated block before the pin": append(
			[]byte("-----BEGIN CERTIFICATE-----\nMIIB\n"), pemOf(local.authority)...),
		"a certificate block with headers": []byte(strings.Replace(string(pemOf(local.authority)),
			"-----BEGIN CERTIFICATE-----\n", "-----BEGIN CERTIFICATE-----\nProc-Type: 4,ENCRYPTED\n\n", 1)),

		"no certificate":              nil,
		"only the deployed authority": pemOf(deployed.authority),
	} {
		t.Run(name, func(t *testing.T) {
			client := infra.WithProjectedAuthorityForTest(
				infra.NewKubernetesClientInCluster(t, local.server.URL, pemOf(local.authority), local.hostToken, hostNamespace), bundle)
			require.ErrorIs(t, client.BindToAuthority(context.Background(), local.fingerprint),
				infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
		})
	}
}

// ATTACK (review finding 1, the relay): even with the bundle replaced by exactly
// the developer's authority, a deployed pod steered at the developer's GENUINE
// API server is refused — that server presents the pinned chain, but it did
// not mint this pod's token and does not authenticate it.
//
// Mutation: skip the review of the host's own token, and this test fails.
func TestAnEndpointUnderThePinThatDidNotMintThisPodsTokenIsRefused(t *testing.T) {
	local := newCluster(t)
	deployed := newCluster(t)

	relayed := infra.NewKubernetesClientInCluster(t, local.server.URL, pemOf(local.authority), deployed.hostToken, hostNamespace)
	err := relayed.BindToAuthority(context.Background(), local.fingerprint)
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	require.ErrorContains(t, err, "did not accept this pod's own token")

	wrongNamespace := infra.NewKubernetesClientInCluster(t, local.server.URL, pemOf(local.authority), local.hostToken, "elsewhere")
	require.ErrorIs(t, wrongNamespace.BindToAuthority(context.Background(), local.fingerprint),
		infra.ErrSolutionHostLocalPolicyOutsideItsCluster,
		"a token the cluster authenticates in another namespace is not this pod's")

	require.NoError(t, local.host(t).BindToAuthority(context.Background(), local.fingerprint),
		"the control: the same server accepts the token it minted for a pod in it")
}

// FOLLOW-UP (review round 2, P3): the API server's answer must be about THIS
// pod. An authenticated review naming another service account, another pod,
// another pod UID, or no pod at all — in the right namespace — is refused: it
// is an answer about some other credential, not the one presented.
//
// Mutation: compare only the namespace (round 1's check), and this test fails.
func TestTheOwnTokenReviewMustNameThisPod(t *testing.T) {
	for name, answer := range map[string]podIdentity{
		"another service account": {namespace: hostNamespace, serviceAccount: "delivery", pod: hostPod.pod, uid: hostPod.uid},
		"another pod":             {namespace: hostNamespace, serviceAccount: hostPod.serviceAccount, pod: "accounts-1", uid: hostPod.uid},
		"another pod UID":         {namespace: hostNamespace, serviceAccount: hostPod.serviceAccount, pod: hostPod.pod, uid: "1e1e1e1e-0000-4000-8000-000000000000"},
		"no pod name":             {namespace: hostNamespace, serviceAccount: hostPod.serviceAccount, uid: hostPod.uid},
		"no pod UID":              {namespace: hostNamespace, serviceAccount: hostPod.serviceAccount, pod: hostPod.pod},
		"another namespace":       {namespace: "elsewhere", serviceAccount: hostPod.serviceAccount, pod: hostPod.pod, uid: hostPod.uid},
	} {
		t.Run(name, func(t *testing.T) {
			local := newCluster(t)
			local.answerAs.Store(&answer)
			err := local.host(t).BindToAuthority(context.Background(), local.fingerprint)
			require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
			require.ErrorIs(t, err, infra.ErrTokenRejected)
		})
	}

	// The expected identity is the token's own: a pod whose projected token
	// names no pod is not running with the bound token the kubelet projects —
	// refused even when the API server's answer agrees with it, pod-less too.
	local := newCluster(t)
	podless := podIdentity{namespace: hostNamespace, serviceAccount: "accounts"}
	unbound := infra.NewKubernetesClientInCluster(t, local.server.URL, pemOf(local.authority),
		boundToken(podless, "unbound"), hostNamespace)
	local.acceptAnyToken.Store(true)
	local.answerAs.Store(&podless)
	require.ErrorIs(t, unbound.BindToAuthority(context.Background(), local.fingerprint), infra.ErrTokenRejected)

	require.NoError(t, newCluster(t).hostForControl(t),
		"the control: an answer naming exactly this pod binds")
}

// ATTACK (review finding 2): after a successful binding, the endpoint moves to
// a backend under ANOTHER authority — a DNS or load-balancer switch, no file or
// environment change. Every later cluster call the host makes, starting with
// the TokenReview that authorises a delivery, must fail rather than continue
// under a proof made against someone else. The client here was deliberately
// CONSTRUCTED trusting both authorities, so the test proves the binding
// replaces the long-lived client's roots instead of relying on construction.
//
// Mutation: keep the proof's transport private to the proof, and this test
// fails.
func TestABindingHoldsForEveryLaterClusterCall(t *testing.T) {
	local := newCluster(t)
	deployed := newCluster(t)
	local.acceptAnyToken.Store(true)

	host := infra.WithInitialRootsForTest(local.host(t), pemOf(local.authority, deployed.authority))
	require.NoError(t, host.BindToAuthority(context.Background(), local.fingerprint))

	// A projected token as a module mint presents it: ReviewToken is the
	// execution reviewer BindExecution asks, and it reads the token's lifetime.
	token := infra.ProjectedTokenForTest(10 * time.Minute)
	_, err := host.ReviewToken(context.Background(), token, "accounts")
	require.NoError(t, err, "the control: before the switch, the bound cluster answers")

	// The switch: the address now reaches a backend under the deployed
	// authority. Established connections end with the old backend, so the next
	// call dials — and handshakes — afresh.
	local.serving.Store(servingCertificateUnder(t, deployed.authority, deployed.authorityKey))
	local.server.CloseClientConnections()
	_, err = host.ReviewToken(context.Background(), token, "accounts")
	require.ErrorIs(t, err, infra.ErrKubernetesUnavailable,
		"a backend under another authority must break authentication, not answer it")
	// The pod read a module mint makes after the review runs on the same bound
	// client (#1056): it breaks too, rather than reading a pod from elsewhere.
	_, err = host.ReadPod(context.Background(), "acme-prod", "worker-7c9f")
	require.ErrorIs(t, err, infra.ErrKubernetesUnavailable,
		"a pod read under another authority must fail, not answer")
}

// ATTACK (review finding 2, redirects): the API server answers a TokenReview
// with a redirect to a path that authenticates anything. The host follows no
// redirect — not during the binding, and not on any later call.
//
// Mutation: let the transport follow redirects, and this test fails.
func TestTheClusterClientFollowsNoRedirect(t *testing.T) {
	local := newCluster(t)
	local.redirectReviews.Store(true)

	err := local.host(t).BindToAuthority(context.Background(), local.fingerprint)
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)

	_, err = local.host(t).ReviewToken(context.Background(), "anything", "accounts")
	require.ErrorIs(t, err, infra.ErrKubernetesUnavailable, "a redirected TokenReview is not an answer")
}

func writePolicy(t *testing.T, directory string, policy *infra.SolutionHostVerificationPolicy) {
	t.Helper()
	encoded, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "verification_policy.json"), encoded, 0o600))
}
