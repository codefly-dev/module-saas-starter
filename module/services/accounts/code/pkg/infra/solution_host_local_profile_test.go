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
	"testing"
	"time"

	"accounts/pkg/infra"

	"github.com/stretchr/testify/require"
)

// A local host admits a named developer as a delivery signer (issue #1063),
// and every other host refuses the same document.
//
// The developer's carrier is signed through the same in-process Sigstore as a
// release's, with an e-mail certificate, an embedded SCT, a Rekor entry and a
// timestamp, and is verified by the production verifier at its production
// strength: admitting a person changes WHO is listed, never how much is
// checked.

const (
	developer       = "dev@example.test"
	developerIssuer = "https://accounts.example.test"
)

// cluster is an API server under its own certificate authority — a cluster as
// a host sees one: the CA the kubelet projects, and a server whose certificate
// that CA issued.
type cluster struct {
	server      *httptest.Server
	authority   *x509.Certificate
	fingerprint string
}

func newCluster(t *testing.T) *cluster {
	t.Helper()
	authorityKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	authority := selfSignedAuthority(t, authorityKey, "cluster-ca")

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serverDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "kube-apiserver"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}, authority, serverKey.Public(), authorityKey)
	require.NoError(t, err)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"gitVersion":"v1.33.0"}`))
	}))
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}},
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	sum := sha256.Sum256(authority.Raw)
	return &cluster{server: server, authority: authority, fingerprint: hex.EncodeToString(sum[:])}
}

func selfSignedAuthority(t *testing.T, key *ecdsa.PrivateKey, name string) *x509.Certificate {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
	}, key.Public(), key)
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

// host is how a host in this cluster reaches its API server: the kubelet
// projects the cluster's CA beside the host's token.
func (c *cluster) host(t *testing.T) *infra.KubernetesClient {
	t.Helper()
	return infra.WithProjectedAuthorityForTest(infra.NewKubernetesClientForTest(t, c.server), pemOf(c.authority))
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

func verifierFor(t *testing.T, sigstore *sigstoreWithCT, policy *infra.SolutionHostVerificationPolicy) interface {
	ProveLocalCluster(context.Context, infra.SolutionHostClusterAuthority) error
} {
	t.Helper()
	verifier, err := infra.NewSolutionHostKeylessVerifierFrom(sigstore, policy)
	require.NoError(t, err)
	return verifier
}

// The done-when of #1063, accept half: a document signed by a listed developer
// is accepted by a host on the local profile, attributed to the developer's
// entry, which speaks for the domains the profile grants it.
func TestALocalHostAdmitsAListedDeveloper(t *testing.T) {
	sigstore := newSigstoreWithCT(t)
	local := newCluster(t)
	payload := []byte(`{"schema":"codefly/solution-host-binding/v2","generation":1}`)
	carrier := sigstore.Sign(t, developer, developerIssuer, payload)

	verifier := verifierFor(t, sigstore, localPolicy(local.fingerprint))
	require.NoError(t, verifier.ProveLocalCluster(context.Background(), local.host(t)))

	signer, err := infra.VerifySignedEntity(verifier, carrier, payload)
	require.NoError(t, err)
	require.Equal(t, "developer", signer)
	require.Equal(t, map[string][]string{"developer": {"local"}}, infra.KeylessSignerDomains(verifier))
}

// The done-when, refuse half: the SAME document is refused by a host on a
// deployed profile — both ways a deployed host can be holding a policy.
//
// Its own deployed policy lists reviewed workflows only, so the developer is
// refused by the ordinary allowlist check. And handed the developer's LOCAL
// profile, a deployed host refuses at boot because its API server does not
// serve under the cluster authority the profile is bound to — and a verifier
// whose cluster was never proven admits nothing, so the document is refused
// even by a boot path that ignored the refusal.
func TestTheSameDocumentIsRefusedByADeployedHost(t *testing.T) {
	sigstore := newSigstoreWithCT(t)
	local := newCluster(t)
	deployed := newCluster(t)
	payload := []byte(`{"schema":"codefly/solution-host-binding/v2","generation":1}`)
	carrier := sigstore.Sign(t, developer, developerIssuer, payload)

	// The control: on its own cluster this exact carrier is admitted.
	admitting := verifierFor(t, sigstore, localPolicy(local.fingerprint))
	require.NoError(t, admitting.ProveLocalCluster(context.Background(), local.host(t)))
	_, err := infra.VerifySignedEntity(admitting, carrier, payload)
	require.NoError(t, err, "the control: the carrier must be one a local host admits")

	t.Run("under its deployed policy", func(t *testing.T) {
		verifier := verifierFor(t, sigstore, acceptedPolicy())
		require.NoError(t, verifier.ProveLocalCluster(context.Background(), deployed.host(t)),
			"a deployed policy is bound to no cluster, so there is nothing to prove")
		_, err := infra.VerifySignedEntity(verifier, carrier, payload)
		require.ErrorContains(t, err, "matched no accepted signer")
	})

	t.Run("handed the local profile", func(t *testing.T) {
		verifier := verifierFor(t, sigstore, localPolicy(local.fingerprint))
		err := verifier.ProveLocalCluster(context.Background(), deployed.host(t))
		require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
		require.ErrorContains(t, err, deployed.fingerprint, "the refusal names the authority this cluster projects")

		_, err = infra.VerifySignedEntity(verifier, carrier, payload)
		require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster,
			"a local profile whose cluster was not proven admits nothing")
	})
}

// A person the local profile does not list is refused, and so is the listed
// address certified by another issuer: the entry is the exact pair.
func TestALocalHostRefusesAnUnlistedPerson(t *testing.T) {
	sigstore := newSigstoreWithCT(t)
	local := newCluster(t)
	payload := []byte(`{"generation":1}`)
	verifier := verifierFor(t, sigstore, localPolicy(local.fingerprint))
	require.NoError(t, verifier.ProveLocalCluster(context.Background(), local.host(t)))

	for name, carrier := range map[string]infra.VerifySignedEntityInput{
		"another person":                     sigstore.Sign(t, "someone-else@example.test", developerIssuer, payload),
		"the listed address, another issuer": sigstore.Sign(t, developer, "https://issuer.attacker.test", payload),
		"an address extending the listed one": sigstore.Sign(t, developer+".attacker.test", developerIssuer, payload),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := infra.VerifySignedEntity(verifier, carrier, payload)
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
// decodes and validates, and on a deployed host it is refused before a single
// carrier is read. And a deployed policy carrying a person fails to decode
// into a running host at all.
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
	require.ErrorContains(t, err, "a deployed host never admits a local profile")

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

// The binding is to the authority the API server PROVES, not to a file in the
// pod. A projected bundle carrying the developer's CA beside the deployed
// cluster's own passes a "does the bundle contain the pin" check while the host
// keeps talking to the deployed API server; the handshake is made against the
// pinned certificate ALONE, so it is refused.
//
// This is the mutation that matters: verify the handshake against the whole
// projected bundle instead, and this test fails while every other one passes.
func TestALocalProfileIsBoundToTheAuthorityTheServerProves(t *testing.T) {
	local := newCluster(t)
	deployed := newCluster(t)
	both := pemOf(local.authority, deployed.authority)

	host := infra.WithProjectedAuthorityForTest(infra.NewKubernetesClientForTest(t, deployed.server), both)
	err := host.ServesUnderAuthority(context.Background(), local.fingerprint)
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster,
		"a bundle naming the local CA does not make the deployed API server the local cluster")

	require.NoError(t, host.ServesUnderAuthority(context.Background(), deployed.fingerprint),
		"the control: the same bundle proves the authority the server really serves under")

	missing := infra.WithProjectedAuthorityForTest(infra.NewKubernetesClientForTest(t, deployed.server), pemOf(deployed.authority))
	err = missing.ServesUnderAuthority(context.Background(), local.fingerprint)
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	require.ErrorContains(t, err, deployed.fingerprint, "the refusal names what this cluster projects, so a recreated cluster's profile is one copy")
}

func writePolicy(t *testing.T, directory string, policy *infra.SolutionHostVerificationPolicy) {
	t.Helper()
	encoded, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "verification_policy.json"), encoded, 0o600))
}
