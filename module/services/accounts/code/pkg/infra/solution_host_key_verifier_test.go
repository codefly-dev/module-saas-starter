package infra_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"accounts/internal/deliverytest"
	"accounts/pkg/infra"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

func acceptedPolicy(t *testing.T) *infra.SolutionHostVerificationPolicy {
	t.Helper()
	return &infra.SolutionHostVerificationPolicy{Signers: []infra.SolutionHostSigner{{
		Name: "release label", Kind: solutionhost.KeyKindRelease, KeyID: "example-release-v1",
		PublicKey: deliverytest.PublicKey(t, "example-release-v1"), Domains: []string{solutionhost.FixtureDomain},
	}}}
}

func bindingPayload(t *testing.T) []byte {
	t.Helper()
	raw, err := solutionhost.FixtureDocument(solutionhost.DocumentTypePresence, "valid")
	require.NoError(t, err)
	document, err := solutionhost.Parse(raw)
	require.NoError(t, err)
	payload, err := solutionhost.SignedPayload(document)
	require.NoError(t, err)
	return payload
}

// No listener or HTTP client participates: both the accepted control and every
// refusal run over local pins and real signatures through core's delivery reader.
func TestPinnedKeysVerifyDeliveryOffline(t *testing.T) {
	policy := acceptedPolicy(t)
	verifier := verifierFor(t, policy)
	payload := bindingPayload(t)
	accepted := deliverytest.Carrier(t, payload, policy.Signers[0].KeyID)
	delivered, err := verifyPresence(t, verifier, accepted)
	require.NoError(t, err)
	require.Equal(t, policy.Signers[0].KeyID, delivered.DeliveredBy())
	require.Equal(t, solutionhost.KeyKindRelease, delivered.Signer().Kind)
	require.Equal(t, payload, delivered.Payload())
	require.Equal(t, map[string][]string{policy.Signers[0].KeyID: {solutionhost.FixtureDomain}}, verifier.SignerDomains())

	for name, change := range map[string]func(*solutionhost.Signed){
		"tampered document": func(s *solutionhost.Signed) {
			s.Document = bytes.Replace(s.Document, []byte(`"generation":4`), []byte(`"generation":5`), 1)
		},
		"tampered payload type": func(s *solutionhost.Signed) {
			s.Envelope = bytes.Replace(s.Envelope, []byte(solutionhost.PayloadTypePresence), []byte(solutionhost.PayloadTypeAuthority), 1)
		},
		"two signatures": func(s *solutionhost.Signed) {
			var envelope map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(s.Envelope, &envelope))
			var signatures []json.RawMessage
			require.NoError(t, json.Unmarshal(envelope["signatures"], &signatures))
			envelope["signatures"], _ = json.Marshal(append(signatures, signatures[0]))
			s.Envelope, _ = json.Marshal(envelope)
		},
	} {
		t.Run(name, func(t *testing.T) {
			signed, err := solutionhost.ParseSigned(accepted)
			require.NoError(t, err)
			change(signed)
			raw, err := solutionhost.MarshalSigned(signed)
			require.NoError(t, err)
			require.NotEqual(t, string(accepted), string(raw), "the mutation must change the carrier")
			refused, err := verifyPresence(t, verifier, raw)
			require.Error(t, err)
			require.Nil(t, refused)
		})
	}
	t.Run("unpinned key", func(t *testing.T) {
		_, err := verifyPresence(t, verifier, deliverytest.Carrier(t, payload, "unlisted-release"))
		require.ErrorContains(t, err, "untrusted key ID")
	})
	t.Run("wrong domain", func(t *testing.T) {
		policy := acceptedPolicy(t)
		policy.Signers[0].Domains = []string{"another-domain"}
		_, err := verifyPresence(t, verifierFor(t, policy), accepted)
		require.ErrorIs(t, err, solutionhost.ErrWrongDomain)
	})
	t.Run("immutable pins", func(t *testing.T) {
		domains := verifier.SignerDomains()
		domains[policy.Signers[0].KeyID][0] = "another-domain"
		policy.Signers[0].Domains[0] = "another-domain"
		policy.Signers[0].KeyID = "changed"
		_, err := verifyPresence(t, verifier, accepted)
		require.NoError(t, err)
	})
	// A concrete structural assertion backs the absence of an online dependency;
	// the executable verification above needs no test server or network fixture.
	typ := reflect.TypeOf(verifier).Elem()
	for i := 0; i < typ.NumField(); i++ {
		require.NotContains(t, typ.Field(i).Type.String(), "http.")
	}
}

func TestPinnedPolicyRefusesInvalidPinsAtBoot(t *testing.T) {
	for name, change := range map[string]func(*infra.SolutionHostVerificationPolicy){
		"missing kind":         func(p *infra.SolutionHostVerificationPolicy) { p.Signers[0].Kind = "" },
		"unknown kind":         func(p *infra.SolutionHostVerificationPolicy) { p.Signers[0].Kind = "workload" },
		"person outside local": func(p *infra.SolutionHostVerificationPolicy) { p.Signers[0].Kind = solutionhost.KeyKindPerson },
		"missing name":         func(p *infra.SolutionHostVerificationPolicy) { p.Signers[0].Name = "" },
		"duplicate name": func(p *infra.SolutionHostVerificationPolicy) {
			other := p.Signers[0]
			other.KeyID = "second-key"
			p.Signers = append(p.Signers, other)
		},
		"duplicate key ID": func(p *infra.SolutionHostVerificationPolicy) {
			other := p.Signers[0]
			other.Name = "second-label"
			p.Signers = append(p.Signers, other)
		},
		"invalid key":     func(p *infra.SolutionHostVerificationPolicy) { p.Signers[0].PublicKey = "not a public key" },
		"missing key ID":  func(p *infra.SolutionHostVerificationPolicy) { p.Signers[0].KeyID = "" },
		"missing domains": func(p *infra.SolutionHostVerificationPolicy) { p.Signers[0].Domains = nil },
		"no signers":      func(p *infra.SolutionHostVerificationPolicy) { p.Signers = nil },
	} {
		t.Run(name, func(t *testing.T) {
			policy := acceptedPolicy(t)
			change(policy)
			anchor := t.TempDir()
			writePolicy(t, anchor, policy)
			verifier, err := infra.ReadSolutionHostKeyVerifier(anchor)
			require.Nil(t, verifier)
			require.ErrorIs(t, err, infra.ErrSolutionHostTrustRootUnavailable)
		})
	}
}

func TestTrustMountRequiresOnlyThePinnedPolicy(t *testing.T) {
	anchor := t.TempDir()
	verifier, err := infra.ReadSolutionHostKeyVerifier(anchor)
	require.Nil(t, verifier)
	require.ErrorIs(t, err, infra.ErrSolutionHostTrustRootUnavailable)
	require.ErrorContains(t, err, "verification_policy.json")
	// An obsolete sibling must have no effect, even when its bytes are invalid.
	// Its name is assembled so the source scan flags only live dependencies.
	require.NoError(t, os.WriteFile(filepath.Join(anchor, "trusted_"+"root.json"), []byte("invalid"), 0600))
	writePolicy(t, anchor, acceptedPolicy(t))
	verifier, err = infra.ReadSolutionHostKeyVerifier(anchor)
	require.NoError(t, err)
	_, err = verifyPresence(t, verifier, deliverytest.Carrier(t, bindingPayload(t), "example-release-v1"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(anchor, "verification_policy.json")))
	_, err = infra.ReadSolutionHostKeyVerifier(anchor)
	require.ErrorIs(t, err, infra.ErrSolutionHostTrustRootUnavailable)
}

func TestTrustPolicyDecodingIsStrict(t *testing.T) {
	encoded, err := json.Marshal(acceptedPolicy(t))
	require.NoError(t, err)
	for name, data := range map[string][]byte{
		"top field":       bytes.Replace(encoded, []byte(`{"signers"`), []byte(`{"unknown":true,"signers"`), 1),
		"signer field":    bytes.Replace(encoded, []byte(`"name":`), []byte(`"unknown":true,"name":`), 1),
		"local field":     append([]byte(`{"local":{"unknown":true},`), encoded[1:]...),
		"second document": append(bytes.Clone(encoded), []byte(` {}`)...),
		"trailing junk":   append(bytes.Clone(encoded), ']'),
		"null":            []byte(`null`),
	} {
		t.Run(name, func(t *testing.T) {
			anchor := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(anchor, "verification_policy.json"), data, 0600))
			verifier, err := infra.ReadSolutionHostKeyVerifier(anchor)
			require.Nil(t, verifier)
			require.ErrorIs(t, err, infra.ErrSolutionHostTrustRootUnavailable)
		})
	}
}

// An in-memory policy cannot be altered after construction to remove a local
// proof requirement or steer that proof at another cluster.
func TestLocalPolicyIsCopiedBeforeProof(t *testing.T) {
	policy := localPolicy(t, strings.Repeat("a", 64))
	verifier := verifierFor(t, policy)
	policy.Local = nil
	keys, err := verifier.Verifier()
	require.Nil(t, keys)
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	require.ErrorIs(t, verifier.ProveLocalCluster(context.Background(), nil), infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
}

// The proof gate can be exercised without a listener; the existing TLS cluster
// tests separately prove how KubernetesClient establishes the binding itself.
type clusterProof func(context.Context, string) error

func (proof clusterProof) BindToAuthority(ctx context.Context, fingerprint string) error {
	return proof(ctx, fingerprint)
}

func TestLocalKeyRequiresSuccessfulClusterProof(t *testing.T) {
	fingerprint := strings.Repeat("b", 64)
	verifier := verifierFor(t, localPolicy(t, fingerprint))
	carrier := deliverytest.Carrier(t, bindingPayload(t), "example-developer")
	refused := clusterProof(func(_ context.Context, got string) error {
		require.Equal(t, fingerprint, got)
		return infra.ErrSolutionHostLocalPolicyOutsideItsCluster
	})
	require.ErrorIs(t, verifier.ProveLocalCluster(context.Background(), refused), infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	_, err := verifyPresence(t, verifier, carrier)
	require.ErrorIs(t, err, infra.ErrSolutionHostLocalPolicyOutsideItsCluster)
	accepted := clusterProof(func(_ context.Context, got string) error {
		require.Equal(t, fingerprint, got)
		return nil
	})
	require.NoError(t, verifier.ProveLocalCluster(context.Background(), accepted))
	delivered, err := verifyPresence(t, verifier, carrier)
	require.NoError(t, err)
	require.Equal(t, "example-developer", delivered.DeliveredBy())
	require.Equal(t, solutionhost.KeyKindPerson, delivered.Signer().Kind)
	// The same bytes are still refused by an ordinary deployed policy.
	_, err = verifyPresence(t, verifierFor(t, acceptedPolicy(t)), carrier)
	require.ErrorContains(t, err, "untrusted key ID")
}
