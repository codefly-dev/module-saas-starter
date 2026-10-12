package keyservice

// One suite, both backends. A backend is only interchangeable if the properties
// every caller already relies on hold identically, and the only way to know
// that is to assert them against each backend from the same place: a suite
// written once per backend drifts, and the drift shows up as a credential that
// cannot be read after a cutover.

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// conformanceSealer is one backend under test, plus the knob that makes its key
// service answer a given status, so the suite can assert how each separates an
// outage from a credential that is genuinely unreadable.
type conformanceSealer struct {
	Sealer
	outage func(status int)
}

func conformanceSealers() map[string]func(*testing.T) conformanceSealer {
	return map[string]func(*testing.T) conformanceSealer{
		"vault": func(t *testing.T) conformanceSealer {
			fake := newFakeVault(t)
			return conformanceSealer{Sealer: fake.sealer(), outage: func(status int) { fake.outage = status }}
		},
		"kms": func(t *testing.T) conformanceSealer {
			fake, sealer := newConformanceKMS(t)
			return conformanceSealer{Sealer: sealer, outage: func(status int) { fake.outage = status }}
		},
	}
}

func TestSealerConformance(t *testing.T) {
	for name, build := range conformanceSealers() {
		t.Run(name, func(t *testing.T) {
			t.Run("round trips", func(t *testing.T) {
				sealer := build(t)
				payload, err := sealer.Seal(t.Context(), "mfa-totp", "JBSWY3DPEHPK3PXP")
				require.NoError(t, err)
				opened, err := sealer.Open(t.Context(), "mfa-totp", payload)
				require.NoError(t, err)
				require.Equal(t, "JBSWY3DPEHPK3PXP", opened)
			})

			t.Run("refuses another purpose", func(t *testing.T) {
				sealer := build(t)
				payload, err := sealer.Seal(t.Context(), "webhook-signing:a", "secret")
				require.NoError(t, err)
				_, err = sealer.Open(t.Context(), "webhook-signing:b", payload)
				require.Error(t, err, "a payload copied to another row must not open")
				require.ErrorIs(t, err, business.ErrInvalidSecretEnvelope)
			})

			t.Run("does not repeat a ciphertext", func(t *testing.T) {
				sealer := build(t)
				first, err := sealer.Seal(t.Context(), "org-idp:acme", "s3cret")
				require.NoError(t, err)
				second, err := sealer.Seal(t.Context(), "org-idp:acme", "s3cret")
				require.NoError(t, err)
				require.NotEqual(t, first, second,
					"two seals of one plaintext must differ, or equal ciphertexts reveal equal secrets")
			})

			t.Run("refuses a corrupt payload", func(t *testing.T) {
				sealer := build(t)
				payload, err := sealer.Seal(t.Context(), "mfa-totp", "seed")
				require.NoError(t, err)
				_, err = sealer.Open(t.Context(), "mfa-totp", payload[:len(payload)-4])
				require.Error(t, err)
			})

			t.Run("keyed hash is deterministic and key-bound", func(t *testing.T) {
				sealer := build(t)
				first, err := sealer.MAC(t.Context(), "sk_live_example")
				require.NoError(t, err)
				again, err := sealer.MAC(t.Context(), "sk_live_example")
				require.NoError(t, err)
				require.Equal(t, first, again, "API-key lookup is by this value, so it cannot move")
				other, err := sealer.MAC(t.Context(), "sk_live_other")
				require.NoError(t, err)
				require.NotEqual(t, first, other)
				require.NotContains(t, first, "sk_live_example", "the hash must not carry the key")
			})

			t.Run("an outage is not an invalid credential", func(t *testing.T) {
				backend := build(t)
				payload, err := backend.Seal(t.Context(), "mfa-totp", "seed")
				require.NoError(t, err)

				backend.outage(http.StatusServiceUnavailable)
				_, err = backend.Open(t.Context(), "mfa-totp", payload)
				require.Error(t, err)
				require.NotErrorIs(t, err, business.ErrInvalidSecretEnvelope,
					"a caller that disabled an endpoint because the key service was briefly down would have destroyed a credential that was never broken")
			})

			t.Run("carries a status without the provider body", func(t *testing.T) {
				for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusServiceUnavailable} {
					backend := build(t)
					payload, err := backend.Seal(t.Context(), "mfa-totp", "seed")
					require.NoError(t, err)

					backend.outage(status)
					_, err = backend.Open(t.Context(), "mfa-totp", payload)
					require.Error(t, err)
					var typed interface{ HTTPStatusCode() int }
					require.True(t, errors.As(err, &typed), "status %d must travel as a typed status", status)
					require.Equal(t, status, typed.HTTPStatusCode())
					require.NotContains(t, err.Error(), "sensitive-provider-body")
				}
			})

			t.Run("tags every envelope it seals", func(t *testing.T) {
				sealer := build(t)
				cipher, err := NewCipher(sealer, nil)
				require.NoError(t, err)
				stored, err := cipher.EncryptSecret(t.Context(), "mfa-totp", "seed")
				require.NoError(t, err)
				require.True(t, strings.HasPrefix(stored, EnvelopePrefix(sealer.Tag())),
					"stored value %q must name the backend that sealed it", stored)
			})
		})
	}
}

func newConformanceKMS(t *testing.T) (*fakeKMS, *KMSSealer) {
	t.Helper()
	fake := newFakeKMS(t)
	config := fake.config()
	config.EnvelopeKey = fake.addSymmetricKey(t, testKeyName("envelope"))
	config.MACKey = fake.addMACKey(t, testKeyName("api-keys"))
	sealer, err := NewKMSSealer(t.Context(), config)
	require.NoError(t, err)
	return fake, sealer
}

func newConformanceKMSSealer(t *testing.T) *KMSSealer {
	t.Helper()
	_, sealer := newConformanceKMS(t)
	return sealer
}

// TestKMSRefusesByName drives every refusal the `kms` backend raises at boot.
// Each is checked by the text an operator will read, because a refusal that
// does not name the key or the key name is one that gets diagnosed by guessing.
func TestKMSRefusesByName(t *testing.T) {
	for name, scenario := range map[string]struct {
		arrange func(*testing.T, *fakeKMS, *KMSConfig)
		expect  string
	}{
		"envelope key is not a resource name": {
			arrange: func(t *testing.T, fake *fakeKMS, config *KMSConfig) {
				config.EnvelopeKey = "envelope"
				config.MACKey = fake.addMACKey(t, testKeyName("api-keys"))
			},
			expect: KMSEnvelopeKeyKey + " must be a Cloud KMS cryptoKey resource name",
		},
		"mac key names a key rather than a version": {
			arrange: func(t *testing.T, fake *fakeKMS, config *KMSConfig) {
				config.EnvelopeKey = fake.addSymmetricKey(t, testKeyName("envelope"))
				fake.addMACKey(t, testKeyName("api-keys"))
				config.MACKey = testKeyName("api-keys")
			},
			expect: "stops every API key already stored from matching",
		},
		"envelope key has the wrong purpose": {
			arrange: func(t *testing.T, fake *fakeKMS, config *KMSConfig) {
				config.MACKey = fake.addMACKey(t, testKeyName("api-keys"))
				fake.addMACKey(t, testKeyName("envelope"))
				config.EnvelopeKey = testKeyName("envelope")
			},
			expect: "has purpose MAC; ENCRYPT_DECRYPT is required",
		},
		"mac key has the wrong algorithm": {
			arrange: func(t *testing.T, fake *fakeKMS, config *KMSConfig) {
				config.EnvelopeKey = fake.addSymmetricKey(t, testKeyName("envelope"))
				config.MACKey = fake.addMACKey(t, testKeyName("api-keys"))
				fake.keys[testKeyName("api-keys")].algorithm = "HMAC_SHA512"
			},
			expect: "the keyed hash requires HMAC_SHA256",
		},
		"mac key version is disabled": {
			arrange: func(t *testing.T, fake *fakeKMS, config *KMSConfig) {
				config.EnvelopeKey = fake.addSymmetricKey(t, testKeyName("envelope"))
				config.MACKey = fake.addMACKey(t, testKeyName("api-keys"))
				fake.keys[testKeyName("api-keys")].versions[1] = "DESTROYED"
			},
			expect: "is DESTROYED, not ENABLED",
		},
		"the workload identity cannot reach the key": {
			arrange: func(t *testing.T, fake *fakeKMS, config *KMSConfig) {
				config.EnvelopeKey = fake.addSymmetricKey(t, testKeyName("envelope"))
				config.MACKey = fake.addMACKey(t, testKeyName("api-keys"))
				fake.outage = http.StatusForbidden
			},
			expect: "cloud kms refused the workload identity on " + testKeyName("envelope"),
		},
		"the pod has no bound service account": {
			arrange: func(t *testing.T, fake *fakeKMS, config *KMSConfig) {
				config.EnvelopeKey = fake.addSymmetricKey(t, testKeyName("envelope"))
				config.MACKey = fake.addMACKey(t, testKeyName("api-keys"))
				fake.metadataStatus = 404
			},
			expect: "the pod has no bound service account",
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeKMS(t)
			config := fake.config()
			scenario.arrange(t, fake, &config)
			_, err := NewKMSSealer(t.Context(), config)
			require.Error(t, err)
			require.Contains(t, err.Error(), scenario.expect)
		})
	}
}

// TestKMSSealBindsAssociatedData is the property the purpose check rests on for
// this backend: the purpose is Cloud KMS's own associated data, so a ciphertext
// moved between rows fails the integrity check rather than decrypting into a
// value some later check has to catch.
func TestKMSSealBindsAssociatedData(t *testing.T) {
	sealer := newConformanceKMSSealer(t)
	payload, err := sealer.Seal(t.Context(), "github-connector:a", "ghp_example")
	require.NoError(t, err)

	_, err = sealer.Open(t.Context(), "github-connector:b", payload)
	require.ErrorIs(t, err, business.ErrInvalidSecretEnvelope)

	opened, err := sealer.Open(t.Context(), "github-connector:a", payload)
	require.NoError(t, err)
	require.Equal(t, "ghp_example", opened)
}

func TestKMSSealRecordsTheKeyVersion(t *testing.T) {
	sealer := newConformanceKMSSealer(t)
	payload, err := sealer.Seal(t.Context(), "mfa-totp", "seed")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(payload, "1:"+keyFingerprint(testKeyName("envelope"))+":"),
		"the payload must name the key version AND the key that sealed it, got %q", payload)
}

func TestEnvelopeParsing(t *testing.T) {
	for name, scenario := range map[string]struct {
		stored  string
		backend string
		payload string
		invalid bool
	}{
		"vault transit": {
			stored:  "cfs1:vault-transit:dmF1bHQ6djE6YWJj",
			backend: TagVaultTransit,
			payload: "dmF1bHQ6djE6YWJj",
		},
		"cloud kms carries its key version and key": {
			stored:  "cfs1:gcp-kms:3:Zmluz2VyAAAAAAAA:Y2lwaGVy",
			backend: TagGCPKMS,
			payload: "3:Zmluz2VyAAAAAAAA:Y2lwaGVy",
		},
		"an unknown version is refused": {stored: "cfs2:vault-transit:abc", invalid: true},
		"an unframed value is refused":  {stored: "vault:v1:abc", invalid: true},
		"an empty payload is refused":   {stored: "cfs1:vault-transit:", invalid: true},
		"a missing backend is refused":  {stored: "cfs1::abc", invalid: true},
		"a bare envelope is refused":    {stored: "cfs1", invalid: true},
		"plaintext is refused":          {stored: "JBSWY3DPEHPK3PXP", invalid: true},
	} {
		t.Run(name, func(t *testing.T) {
			envelope, err := ParseEnvelope(scenario.stored)
			if scenario.invalid {
				require.ErrorIs(t, err, business.ErrInvalidSecretEnvelope)
				return
			}
			require.NoError(t, err)
			require.Equal(t, scenario.backend, envelope.Backend)
			require.Equal(t, scenario.payload, envelope.Payload)
			require.Equal(t, scenario.stored, envelope.String())
		})
	}
}

// TestCipherCutover is the migration contract: during a cutover reads accept
// both backends, writes only the selected one, and the re-seal rewrites what
// the previous backend sealed while leaving what the selected one sealed alone.
func TestCipherCutover(t *testing.T) {
	previous := newFakeVault(t).sealer()
	selected := newConformanceKMSSealer(t)

	before, err := NewCipher(previous, nil)
	require.NoError(t, err)
	stored, err := before.EncryptSecret(t.Context(), "org-idp:acme", "legacy-secret")
	require.NoError(t, err)

	cutover, err := NewCipher(selected, previous)
	require.NoError(t, err)
	require.Equal(t, TagGCPKMS, cutover.SealsWith())
	require.Equal(t, TagVaultTransit, cutover.PreviousTag())

	opened, err := cutover.DecryptSecret(t.Context(), "org-idp:acme", stored)
	require.NoError(t, err)
	require.Equal(t, "legacy-secret", opened)

	fresh, err := cutover.EncryptSecret(t.Context(), "org-idp:acme", "new-secret")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(fresh, EnvelopePrefix(TagGCPKMS)),
		"a cutover seals only under the selected backend")

	resealed, changed, err := cutover.Reseal(t.Context(), "org-idp:acme", stored)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, strings.HasPrefix(resealed, EnvelopePrefix(TagGCPKMS)))
	opened, err = cutover.DecryptSecret(t.Context(), "org-idp:acme", resealed)
	require.NoError(t, err)
	require.Equal(t, "legacy-secret", opened)

	_, changed, err = cutover.Reseal(t.Context(), "org-idp:acme", resealed)
	require.NoError(t, err)
	require.False(t, changed, "re-sealing what is already sealed must be a no-op, so a sweep is restart-safe")
}

// TestCipherRefusesAnUnboundBackend is what keeps the previous binding from
// being withdrawn too early: a value nothing can open is named as that rather
// than handed to the wrong backend.
func TestCipherRefusesAnUnboundBackend(t *testing.T) {
	stored := Envelope{Backend: TagVaultTransit, Payload: "dmF1bHQ6djE6YWJj"}.String()

	cipher, err := NewCipher(newConformanceKMSSealer(t), nil)
	require.NoError(t, err)
	_, err = cipher.DecryptSecret(t.Context(), "mfa-totp", stored)
	require.Error(t, err)
	require.Contains(t, err.Error(), `sealed by "vault-transit", which this deployment does not bind`)
}

func TestCipherRefusesTheSameBackendTwice(t *testing.T) {
	sealer := newFakeVault(t).sealer()
	_, err := NewCipher(sealer, newFakeVault(t).sealer())
	require.Error(t, err)
	require.Contains(t, err.Error(), "a cutover has two distinct backends or none")
}

// TestCandidateHashesCoverBothKeysDuringACutover is the one part of the
// migration a re-seal cannot do: a keyed hash has no plaintext to re-key, so a
// presented key is looked up under both keys until the keys issued under the
// outgoing one are gone.
func TestCandidateHashesCoverBothKeysDuringACutover(t *testing.T) {
	vault := newFakeVault(t).sealer()
	kms := newConformanceKMSSealer(t)

	legacy, err := vault.MAC(t.Context(), "sk_live_example")
	require.NoError(t, err)

	cutover, err := NewCipher(kms, vault)
	require.NoError(t, err)
	candidates, err := cutover.CandidateHashes(t.Context(), "sk_live_example")
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	require.Contains(t, candidates, legacy)

	current, err := cutover.HashKey(t.Context(), "sk_live_example")
	require.NoError(t, err)
	require.Equal(t, current, candidates[0], "a newly issued key is stored under the selected backend")

	settled, err := NewCipher(kms, nil)
	require.NoError(t, err)
	candidates, err = settled.CandidateHashes(t.Context(), "sk_live_example")
	require.NoError(t, err)
	require.Equal(t, []string{current}, candidates,
		"once the previous backend is withdrawn there is one hash to look up")
}

func TestCipherRequiresAPurposeAndAPlaintext(t *testing.T) {
	cipher, err := NewCipher(newFakeVault(t).sealer(), nil)
	require.NoError(t, err)

	_, err = cipher.EncryptSecret(t.Context(), "", "value")
	require.Error(t, err)
	_, err = cipher.EncryptSecret(t.Context(), "mfa-totp", "")
	require.Error(t, err)
	_, err = cipher.DecryptSecret(t.Context(), "", "cfs1:vault-transit:abc")
	require.Error(t, err)
}

func TestParseBackend(t *testing.T) {
	for _, backend := range Backends {
		parsed, err := ParseBackend(string(backend))
		require.NoError(t, err)
		require.Equal(t, backend, parsed)
	}
	_, err := ParseBackend("")
	require.Error(t, err)
	_, err = ParseBackend("consul")
	require.Error(t, err)
	require.Contains(t, err.Error(), "use vault or kms")
}

// kmsSealerOn builds a sealer over one named envelope key in an existing fake,
// so a cutover between two Cloud KMS keys can be driven.
func kmsSealerOn(t *testing.T, fake *fakeKMS, envelopeKey string) *KMSSealer {
	t.Helper()
	config := fake.config()
	config.EnvelopeKey = fake.addSymmetricKey(t, envelopeKey)
	if _, registered := fake.keys[testKeyName("api-keys")]; !registered {
		fake.addMACKey(t, testKeyName("api-keys"))
	}
	config.MACKey = testKeyName("api-keys") + "/cryptoKeyVersions/1"
	sealer, err := NewKMSSealer(t.Context(), config)
	require.NoError(t, err)
	return sealer
}

// A value sealed by a DIFFERENT Cloud KMS key is a configuration answer, not a
// broken credential. Cloud KMS answers both with 400, so without the key
// recorded in the envelope an operator who mistyped KEY_SERVICE_KMS_ENVELOPE_KEY
// would have every source marked permanently unreadable and every user told to
// re-enter their credential.
func TestKMSOpenRefusesAForeignKeyWithoutCondemningTheCredential(t *testing.T) {
	fake := newFakeKMS(t)
	sealedByA := kmsSealerOn(t, fake, testKeyName("envelope-a"))
	readerOnB := kmsSealerOn(t, fake, testKeyName("envelope-b"))

	payload, err := sealedByA.Seal(t.Context(), "github-connector:s1", "ghp_example")
	require.NoError(t, err)

	_, err = readerOnB.Open(t.Context(), "github-connector:s1", payload)
	require.Error(t, err)
	require.NotErrorIs(t, err, business.ErrInvalidSecretEnvelope,
		"a wrong key name must not be reported as a credential that needs re-entering")
	require.Contains(t, err.Error(), "sealed by a different Cloud KMS key")
	require.Contains(t, err.Error(), KMSEnvelopeKeyKey)
}

// The boot probe: every static check passes on a key that is well-formed and
// simply is not the one holding this deployment's data, so the constructor
// proves the key can seal and open before anything depends on it.
func TestNewKMSSealerRefusesAKeyThatCannotRoundTrip(t *testing.T) {
	fake := newFakeKMS(t)
	config := fake.config()
	config.EnvelopeKey = fake.addSymmetricKey(t, testKeyName("envelope"))
	config.MACKey = fake.addMACKey(t, testKeyName("api-keys"))
	// The key answers metadata but refuses the data-plane operations, which is
	// what a role granted viewer-but-not-encrypter looks like.
	fake.failOperations = true

	_, err := NewKMSSealer(t.Context(), config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot seal")
}

// The recorded key version is written on the seal path, so a payload that does
// not carry one was not written by this backend. Reading past it would make the
// recorded version decorative.
func TestKMSPayloadVersionIsValidated(t *testing.T) {
	sealer := newConformanceKMSSealer(t)
	fingerprint := keyFingerprint(testKeyName("envelope"))
	for name, payload := range map[string]string{
		"no version":      "" + fingerprint + ":Y2lwaGVy",
		"version is text": "garbage:" + fingerprint + ":Y2lwaGVy",
		"version is zero": "0:" + fingerprint + ":Y2lwaGVy",
		"no key":          "1:Y2lwaGVy",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := sealer.Open(t.Context(), "mfa-totp", payload)
			require.ErrorIs(t, err, business.ErrInvalidSecretEnvelope)
		})
	}
}

// A re-key inside one backend is a real cutover: two Cloud KMS keys share the
// `gcp-kms` tag, so routing on the tag alone could not express it.
func TestCipherCutoverBetweenTwoCloudKMSKeys(t *testing.T) {
	fake := newFakeKMS(t)
	outgoing := kmsSealerOn(t, fake, testKeyName("envelope-old"))
	incoming := kmsSealerOn(t, fake, testKeyName("envelope-new"))
	require.NotEqual(t, outgoing.Identity(), incoming.Identity(),
		"two Cloud KMS keys must be two backend identities")

	before, err := NewCipher(outgoing, nil)
	require.NoError(t, err)
	stored, err := before.EncryptSecret(t.Context(), "org-idp:acme", "legacy-secret")
	require.NoError(t, err)

	cutover, err := NewCipher(incoming, outgoing)
	require.NoError(t, err)
	opened, err := cutover.DecryptSecret(t.Context(), "org-idp:acme", stored)
	require.NoError(t, err, "a value sealed by the outgoing key must still open")
	require.Equal(t, "legacy-secret", opened)

	resealed, changed, err := cutover.Reseal(t.Context(), "org-idp:acme", stored)
	require.NoError(t, err)
	require.True(t, changed, "a value under the outgoing KEY must be re-sealed, not skipped on a tag match")
	_, changed, err = cutover.Reseal(t.Context(), "org-idp:acme", resealed)
	require.NoError(t, err)
	require.False(t, changed)
}

// The migration sweeps ask "is this an envelope at all", which must be true for
// a framing this build cannot open — otherwise the sweep treats another
// backend's value as plaintext and re-seals the envelope string itself.
func TestIsEnvelopeFramingAcceptsFramingsThisBuildCannotOpen(t *testing.T) {
	for _, stored := range []string{
		"cfs1:vault-transit:abc",
		"cfs1:gcp-kms:1:fp:abc",
		"cfs2:vault-transit:mfa-totp:abc", // the per-purpose split in flight on #1019
		"cfs9:some-future-service:abc",
	} {
		require.True(t, IsEnvelopeFraming(stored), "%q is an application envelope", stored)
	}
	for _, plaintext := range []string{
		"JBSWY3DPEHPK3PXP", "whsec_abc", "", "cfs1", "cfs1:", "cfs1:vault-transit:", "cfs0:x:y", "vault:v1:abc",
	} {
		require.False(t, IsEnvelopeFraming(plaintext), "%q is not an application envelope", plaintext)
	}
}

// The `kms-wrapped` signing backend: a hosted cell runs with no secrets store at
// all, because the signing key is a ciphertext in configuration that only the
// cell's Cloud KMS key and its workload identity can turn back into a key.
func TestKMSWrappedSigningKeyRoundTrips(t *testing.T) {
	fake := newFakeKMS(t)
	config := fake.config()
	config.SigningWrapKey = fake.addSymmetricKey(t, testKeyName("signing-wrap"))

	wrap, err := NewKMSKeyWrap(t.Context(), config)
	require.NoError(t, err)

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	wrapped := wrapSigningSeed(t, wrap, seed)

	private, err := wrap.UnwrapSigningKey(t.Context(), wrapped)
	require.NoError(t, err)
	require.Equal(t, ed25519.NewKeyFromSeed(seed), private)

	// The same ciphertext must always yield the same key: every replica and
	// every restart has to sign identically, and the `kid` the gateway pinned
	// cannot move.
	again, err := wrap.UnwrapSigningKey(t.Context(), wrapped)
	require.NoError(t, err)
	require.Equal(t, private, again)
}

// A wrapped key is not interchangeable with a sealed credential: the associated
// data differs, so a stored credential's ciphertext cannot be presented as a
// signing key.
func TestKMSWrappedSigningKeyIsBoundToItsOwnPurpose(t *testing.T) {
	fake := newFakeKMS(t)
	config := fake.config()
	config.SigningWrapKey = fake.addSymmetricKey(t, testKeyName("signing-wrap"))
	wrap, err := NewKMSKeyWrap(t.Context(), config)
	require.NoError(t, err)

	// Sealed under a stored-credential purpose on the very same key.
	payload, err := wrap.wrap.Seal(t.Context(), "mfa-totp",
		base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize)))
	require.NoError(t, err)

	_, err = wrap.UnwrapSigningKey(t.Context(),
		Envelope{Backend: TagGCPKMS, Payload: payload}.String())
	require.Error(t, err, "a credential sealed under another purpose must not unwrap as a signing key")
}

func TestKMSWrappedSigningKeyRefusesByName(t *testing.T) {
	fake := newFakeKMS(t)
	config := fake.config()
	config.SigningWrapKey = fake.addSymmetricKey(t, testKeyName("signing-wrap"))
	wrap, err := NewKMSKeyWrap(t.Context(), config)
	require.NoError(t, err)

	t.Run("a value that is not an envelope", func(t *testing.T) {
		_, err := wrap.UnwrapSigningKey(t.Context(), "just-a-base64-seed")
		require.Error(t, err)
		require.Contains(t, err.Error(), SigningKeyWrappedKey)
		require.Contains(t, err.Error(), KMSSigningWrapKeyKey, "the refusal must say how to produce the value")
	})

	t.Run("wrapped by a different key", func(t *testing.T) {
		other := fake.config()
		other.SigningWrapKey = fake.addSymmetricKey(t, testKeyName("someone-elses-wrap"))
		otherWrap, err := NewKMSKeyWrap(t.Context(), other)
		require.NoError(t, err)
		foreign := wrapSigningSeed(t, otherWrap, make([]byte, ed25519.SeedSize))

		_, err = wrap.UnwrapSigningKey(t.Context(), foreign)
		require.Error(t, err)
		require.Contains(t, err.Error(), "wrapped by a different Cloud KMS key")
	})

	t.Run("the wrapped value is the wrong size", func(t *testing.T) {
		short := wrapSigningSeed(t, wrap, []byte("too-short"))
		_, err := wrap.UnwrapSigningKey(t.Context(), short)
		require.Error(t, err)
		require.Contains(t, err.Error(), "Ed25519 seed")
	})

	t.Run("the wrap key is not a resource name", func(t *testing.T) {
		bad := fake.config()
		bad.SigningWrapKey = "signing-wrap"
		_, err := NewKMSKeyWrap(t.Context(), bad)
		require.Error(t, err)
		require.Contains(t, err.Error(), KMSSigningWrapKeyKey)
	})
}

// wrapSigningSeed produces what the cell's provisioning would: the base64 seed,
// sealed by the wrapping key under the signing-key purpose, in the stored
// envelope framing.
func wrapSigningSeed(t *testing.T, wrap *KMSKeyWrap, seed []byte) string {
	t.Helper()
	payload, err := wrap.wrap.Seal(t.Context(), signingKeyWrapPurpose,
		base64.StdEncoding.EncodeToString(seed))
	require.NoError(t, err)
	return Envelope{Backend: TagGCPKMS, Payload: payload}.String()
}
