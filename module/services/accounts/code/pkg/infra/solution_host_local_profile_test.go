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
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/infra"

	"github.com/stretchr/testify/require"
)

// A local host admits a named developer as a delivery signer (issue #1063),
// and every other host refuses the same document.
//
// The developer's carrier is a serialized Sigstore bundle v0.3 — e-mail
// certificate, embedded SCT, Rekor entry with inclusion proof, timestamp — and
// is verified through the public VerifyBundle at the production strength:
// admitting a person changes WHO is listed, never how much is checked.

const (
	developer       = "dev@example.test"
	developerIssuer = "https://accounts.example.test"
	hostNamespace   = "platform"
)

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
		hostToken:   "token-minted-by-" + hex.EncodeToString(sum[:4]),
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
				response = map[string]any{
					"authenticated": true,
					"audiences":     request.Spec.Audiences,
					"user": map[string]any{
						"username": "system:serviceaccount:" + hostNamespace + ":accounts",
						"extra": map[string][]string{
							"authentication.kubernetes.io/pod-name": {"accounts-0"},
							"authentication.kubernetes.io/pod-uid":  {"0f0f0f0f-0000-4000-8000-000000000000"},
						},
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

// localPolicy is the local profile a developer's host is given: bound to its
// cluster, listing the developer as a person who speaks for the local domain.
func localPolicy(fingerprint string) *infra.SolutionHostVerificationPolicy {
	return &infra.SolutionHostVerificationPolicy{
		Local: &infra.SolutionHostLocalProfile{ClusterCertificateAuthoritySHA256: fingerprint},
		Signers: []infra.SolutionHostSigner{{
			Name:                   "developer",
			Kind:                   infra.SolutionHostSignerPerson,
			Issuer:                 developerIssuer,
			SubjectAlternativeName: developer,
			Domains:                []string{"local"},
		}},
	}
}

type hostVerifier interface {
	VerifyBundle(context.Context, []byte, json.RawMessage) (string, error)
	ProveLocalCluster(context.Context, infra.SolutionHostClusterAuthority) error
}

func verifierFor(t *testing.T, sigstore *sigstoreWithCT, policy *infra.SolutionHostVerificationPolicy) hostVerifier {
	t.Helper()
	verifier, err := infra.NewSolutionHostKeylessVerifierFrom(sigstore, policy)
	require.NoError(t, err)
	return verifier
}

var bindingPayload = []byte(`{"schema":"codefly/solution-host-binding/v2","generation":1}`)

// The done-when of #1063, accept half: a document signed by a listed developer
// — a serialized bundle, through the public VerifyBundle — is accepted by a
// host on the local profile, attributed to the developer's entry, which speaks
// for the domains the profile grants it.
func TestALocalHostAdmitsAListedDeveloper(t *testing.T) {
	sigstore := newSigstoreWithCT(t)
	local := newCluster(t)
	carrier := sigstore.SignBundleWith(t, developer, developerIssuer, bindingPayload, signing{})

	verifier := verifierFor(t, sigstore, localPolicy(local.fingerprint))
	require.NoError(t, verifier.ProveLocalCluster(context.Background(), local.host(t)))

	signer, err := verifier.VerifyBundle(context.Background(), bindingPayload, carrier)
	require.NoError(t, err)
	require.Equal(t, "developer", signer)
	require.Equal(t, map[string][]string{"developer": {"local"}}, infra.KeylessSignerDomains(verifier))

	// The person is held to the full strength: the same developer's carrier
	// whose certificate no CT log saw is refused on the same proven host.
	unlogged := sigstore.SignBundleWith(t, developer, developerIssuer, bindingPayload, signing{noSCT: true})
	_, err = verifier.VerifyBundle(context.Background(), bindingPayload, unlogged)
	require.ErrorContains(t, err, "SCT")
}

// The done-when, refuse half: the SAME serialized document is refused by a
// host on a deployed profile — both ways a deployed host can be holding a
// policy.
func TestTheSameDocumentIsRefusedByADeployedHost(t *testing.T) {
	sigstore := newSigstoreWithCT(t)
	local := newCluster(t)
	deployed := newCluster(t)
	carrier := sigstore.SignBundleWith(t, developer, developerIssuer, bindingPayload, signing{})

	admitting := verifierFor(t, sigstore, localPolicy(local.fingerprint))
	require.NoError(t, admitting.ProveLocalCluster(context.Background(), local.host(t)))
	_, err := admitting.VerifyBundle(context.Background(), bindingPayload, carrier)
	require.NoError(t, err, "the control: the carrier must be one a local host admits")

	t.Run("under its deployed policy", func(t *testing.T) {
		verifier := verifierFor(t, sigstore, acceptedPolicy())
		require.NoError(t, verifier.ProveLocalCluster(context.Background(), deployed.host(t)),
			"a deployed policy is bound to no cluster, so there is nothing to prove")
		_, err := verifier.VerifyBundle(context.Background(), bindingPayload, carrier)
		require.ErrorContains(t, err, "matched no accepted signer")
	})

	t.Run("handed the local profile", func(t *testing.T) {
		verifier := verifierFor(t, sigstore, localPolicy(local.fingerprint))
		err := verifier.ProveLocalCluster(context.Background(), deployed.host(t))
		require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
		require.ErrorContains(t, err, deployed.fingerprint, "the refusal names the authority this cluster projects")

		_, err = verifier.VerifyBundle(context.Background(), bindingPayload, carrier)
		require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster,
			"a local profile whose cluster was not proven admits nothing")
	})
}

// A person the local profile does not list is refused, and so is the listed
// address certified by another issuer: the entry is the exact pair.
func TestALocalHostRefusesAnUnlistedPerson(t *testing.T) {
	sigstore := newSigstoreWithCT(t)
	local := newCluster(t)
	verifier := verifierFor(t, sigstore, localPolicy(local.fingerprint))
	require.NoError(t, verifier.ProveLocalCluster(context.Background(), local.host(t)))

	for name, carrier := range map[string]json.RawMessage{
		"another person":                      sigstore.SignBundleWith(t, "someone-else@example.test", developerIssuer, bindingPayload, signing{}),
		"the listed address, another issuer":  sigstore.SignBundleWith(t, developer, "https://issuer.attacker.test", bindingPayload, signing{}),
		"an address extending the listed one": sigstore.SignBundleWith(t, developer+".attacker.test", developerIssuer, bindingPayload, signing{}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := verifier.VerifyBundle(context.Background(), bindingPayload, carrier)
			require.ErrorContains(t, err, "matched no accepted signer")
		})
	}
}

// Every person entry that would read as narrow and admit more is refused when
// the policy is validated — at boot, before anything is delivered.
func TestAPersonSignerIsRefusedAtValidate(t *testing.T) {
	const fingerprint = "0000000000000000000000000000000000000000000000000000000000000000"
	person := func(edit func(*infra.SolutionHostSigner)) *infra.SolutionHostVerificationPolicy {
		policy := localPolicy(fingerprint)
		edit(&policy.Signers[0])
		return policy
	}
	for name, policy := range map[string]*infra.SolutionHostVerificationPolicy{
		"a person in a policy that is not local": func() *infra.SolutionHostVerificationPolicy {
			policy := localPolicy(fingerprint)
			policy.Local = nil
			return policy
		}(),
		"a wildcard subject":       person(func(s *infra.SolutionHostSigner) { s.SubjectAlternativeName = "*@example.test" }),
		"a wildcard domain":        person(func(s *infra.SolutionHostSigner) { s.SubjectAlternativeName = "dev@*.example.test" }),
		"a bare domain as prefix":  person(func(s *infra.SolutionHostSigner) { s.SubjectAlternativeName = "@example.test" }),
		"a subject with no domain": person(func(s *infra.SolutionHostSigner) { s.SubjectAlternativeName = "dev" }),
		"a workflow URI as person": person(func(s *infra.SolutionHostSigner) { s.SubjectAlternativeName = testIdentity }),
		"a padded subject":         person(func(s *infra.SolutionHostSigner) { s.SubjectAlternativeName = developer + " " }),
		"a wildcard issuer":        person(func(s *infra.SolutionHostSigner) { s.Issuer = "https://*.example.test" }),
		"a plain-http issuer":      person(func(s *infra.SolutionHostSigner) { s.Issuer = "http://accounts.example.test" }),
		"no issuer":                person(func(s *infra.SolutionHostSigner) { s.Issuer = "" }),
		"a source repository":      person(func(s *infra.SolutionHostSigner) { s.SourceRepositoryURI = testRepository }),
		"a build config":           person(func(s *infra.SolutionHostSigner) { s.BuildConfigURI = testIdentity }),
		"an exact ref":             person(func(s *infra.SolutionHostSigner) { s.SourceRepositoryRef = "refs/heads/main" }),
		"a ref prefix":             person(func(s *infra.SolutionHostSigner) { s.SourceRepositoryRefPrefix = "refs/" }),
		"no domains":               person(func(s *infra.SolutionHostSigner) { s.Domains = nil }),
		"an unknown kind":          person(func(s *infra.SolutionHostSigner) { s.Kind = "human" }),
		"one person listed twice": func() *infra.SolutionHostVerificationPolicy {
			policy := localPolicy(fingerprint)
			twin := policy.Signers[0]
			twin.Name = "developer-again"
			twin.Domains = []string{"other"}
			policy.Signers = append(policy.Signers, twin)
			return policy
		}(),
		"a local profile bound to no cluster": func() *infra.SolutionHostVerificationPolicy {
			policy := localPolicy(fingerprint)
			policy.Local.ClusterCertificateAuthoritySHA256 = ""
			return policy
		}(),
		"a local profile with a malformed fingerprint": func() *infra.SolutionHostVerificationPolicy {
			policy := localPolicy(fingerprint)
			policy.Local.ClusterCertificateAuthoritySHA256 = "SHA256:" + fingerprint
			return policy
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, policy.Validate())
		})
	}
	require.NoError(t, localPolicy(fingerprint).Validate(), "the control: the local profile itself validates")

	mixed := localPolicy(fingerprint)
	mixed.Signers = append(mixed.Signers, acceptedPolicy().Signers...)
	require.NoError(t, mixed.Validate(), "a local profile may list a workflow beside the developer")
}

// The boot path a host takes, from the trust anchor's files: a local profile
// decodes and validates, and in any other cluster it is refused before a single
// carrier is read. And a policy that lists a person without declaring itself
// local does not load at all.
func TestALocalPolicyIsRefusedAtBootOnADeployedHost(t *testing.T) {
	local := newCluster(t)
	deployed := newCluster(t)
	anchor := t.TempDir()
	writeTrustRoot(t, anchor)
	writePolicy(t, anchor, localPolicy(local.fingerprint))

	verifier, err := infra.NewSolutionHostKeylessVerifier(anchor)
	require.NoError(t, err, "a local profile is a valid policy document")
	require.NoError(t, verifier.ProveLocalCluster(context.Background(), local.host(t)),
		"the control: on its own cluster the profile is proven")

	verifier, err = infra.NewSolutionHostKeylessVerifier(anchor)
	require.NoError(t, err)
	err = verifier.ProveLocalCluster(context.Background(), deployed.host(t))
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	require.ErrorContains(t, err, "refuses a local profile")

	err = verifier.ProveLocalCluster(context.Background(), nil)
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster,
		"a local profile on a host with no cluster to prove it against is refused, not waved through")

	notLocal := localPolicy(local.fingerprint)
	notLocal.Local = nil
	writePolicy(t, anchor, notLocal)
	_, err = infra.NewSolutionHostKeylessVerifier(anchor)
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
		"the pin twice":               pemOf(local.authority, local.authority),
		"the pin and a key":           append(pemOf(local.authority), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}})...),
		"the pin and stray bytes":     append(pemOf(local.authority), []byte("not a certificate")...),
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

	_, err := host.ReviewToken(context.Background(), "a-delivery-job-token", "accounts")
	require.NoError(t, err, "the control: before the switch, the bound cluster answers")

	// The switch: the address now reaches a backend under the deployed
	// authority. Established connections end with the old backend, so the next
	// call dials — and handshakes — afresh.
	local.serving.Store(servingCertificateUnder(t, deployed.authority, deployed.authorityKey))
	local.server.CloseClientConnections()
	_, err = host.ReviewToken(context.Background(), "a-delivery-job-token", "accounts")
	require.ErrorIs(t, err, infra.ErrKubernetesUnavailable,
		"a backend under another authority must break authentication, not answer it")
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
