package keyservice

// GCP Cloud KMS, the first `kms` backend.
//
// LABELLED STOPGAP — this file names a cloud vendor, and a service should not.
//
// Everywhere else in this host, "where a dependency lives" is resolved by the
// platform: accounts declares that it needs a `vault` and reads its address
// through the Codefly projection, so it never hardcodes one. This file breaks
// that rule on purpose and visibly: the Cloud KMS endpoint and the metadata
// server are hardcoded below, and the resource-name grammar, purposes and
// algorithms are Google's. Adding AWS or Azure therefore means editing accounts,
// which is the opposite of what a key-service seam is for.
//
// It is here because there is nothing to resolve yet. Codefly can express "this
// cell runs a Vault" (`kind: cell-vault`, module/deployment/README.md) but has no
// equivalent for "this cell has a key service", so there is no projection for
// accounts to read and no agent that owns the driver — the way `service-vault`
// owns Vault's. The gap is filed against the CLI, and when a key-service
// dependency kind exists this driver moves below the seam and nothing above it
// changes: the envelope framing, the purpose binding, the cutover and the
// re-seal sweep are all vendor-neutral already.
//
// Do not add a second cloud's driver here. Adding one would make this the place
// cloud drivers live, which is the state this comment exists to prevent.
//
// A hosted cell already runs a managed key service, so accounts does not need a
// stateful secrets store to hold two keys. What the configuration carries is key
// NAMES — values, no files, no stored credential — and the workload
// authenticates as itself: on GCP the metadata server issues the access token
// against the pod's bound service account, so nothing is provisioned to the pod
// and there is nothing to rotate.
//
// This backend holds the ENVELOPE key and the keyed hash, not the signing key:
// signing through a non-exportable key needs the Work Context signer and the
// delegation minter to accept a crypto.Signer first, so there is deliberately no
// Sign operation here rather than one nothing can reach (see ReadVaultSigningKey).
//
// Two keys, each configured in the shape its own rotation semantics demand.
// That asymmetry is deliberate, because getting it wrong is silent:
//
//   - the ENVELOPE key is named as a cryptoKey. Encryption uses the key's
//     primary version and the ciphertext records which version sealed it, so
//     rotation never makes an existing ciphertext unreadable.
//   - the MAC key is named as a cryptoKeyVERSION. The keyed hash behind API-key
//     lookup is what a presented key is looked up by, so it must stay
//     byte-identical for the life of the key: moving to another version
//     silently stops every API key already stored from matching. Pinning the
//     version in configuration makes that a deliberate edit with a stated
//     consequence instead of a side effect of rotating a key. (Vault Transit
//     has the same hazard and hides it better — `transit/hmac` follows the
//     key's latest version, so rotating the Transit key breaks API-key lookup
//     there too.)
//
// On "refuses a key that is exportable": Cloud KMS has no export operation, so
// a GCP key cannot be exportable and there is no such state to refuse. What is
// checkable, and is checked at boot, is the key's purpose, its algorithm and
// that the version this backend will use is ENABLED — plus reachability, since
// a key the workload identity cannot read answers 403 and is named as that
// rather than surfacing later as a failed seal. A backend for a cloud that does
// expose key material (AWS KMS distinguishes an EXTERNAL key origin) adds that
// refusal to its own constructor; nothing above this file changes.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"accounts/pkg/business"
)

const (
	kmsAPIBase      = "https://cloudkms.googleapis.com/v1/"
	kmsMetadataBase = "http://metadata.google.internal/computeMetadata/v1/"

	purposeEncryptDecrypt = "ENCRYPT_DECRYPT"
	purposeMAC            = "MAC"

	algorithmSymmetric  = "GOOGLE_SYMMETRIC_ENCRYPTION"
	algorithmEd25519    = "EC_SIGN_ED25519"
	algorithmHMACSHA256 = "HMAC_SHA256"

	versionStateEnabled = "ENABLED"
)

var (
	cryptoKeyName        = regexp.MustCompile(`^projects/[^/:]+/locations/[^/:]+/keyRings/[^/:]+/cryptoKeys/[^/:]+$`)
	cryptoKeyVersionName = regexp.MustCompile(`^projects/[^/:]+/locations/[^/:]+/keyRings/[^/:]+/cryptoKeys/[^/:]+/cryptoKeyVersions/[1-9][0-9]*$`)
)

// KMSConfig names the keys. Every field is a resource name read from
// configuration as a value.
type KMSConfig struct {
	// EnvelopeKey is the symmetric cryptoKey that seals stored credentials.
	EnvelopeKey string
	// MACKey is the HMAC cryptoKeyVERSION behind API-key lookup.
	MACKey string
	// SigningWrapKey is the symmetric cryptoKey that unwraps the Ed25519 signing
	// key. A key of its own rather than the envelope key: destroying an envelope
	// key version would otherwise take the host's ability to boot with it, and
	// the two rotate on unrelated schedules.
	SigningWrapKey string
	// apiBase and metadataBase are unexported and set only by this package's
	// own tests, so the conformance suite can run the real request and response
	// handling against a fake while production has one hard-coded endpoint
	// nobody can redirect from configuration.
	apiBase      string
	metadataBase string
}

// kmsClient is the authenticated transport: one access token from the workload
// identity, refreshed before it expires, and no other credential anywhere.
type kmsClient struct {
	http         *http.Client
	apiBase      string
	metadataBase string

	mu      sync.Mutex
	token   string
	expires time.Time
	now     func() time.Time
}

func newKMSClient(config KMSConfig) *kmsClient {
	client := &kmsClient{
		http:         &http.Client{Timeout: 10 * time.Second},
		apiBase:      config.apiBase,
		metadataBase: config.metadataBase,
		now:          time.Now,
	}
	if client.apiBase == "" {
		client.apiBase = kmsAPIBase
	}
	if client.metadataBase == "" {
		client.metadataBase = kmsMetadataBase
	}
	return client
}

// accessToken returns the workload identity's current token.
//
// On GCP the identity needs no file: the metadata server answers for the
// service account bound to the pod, over link-local http that never leaves the
// node. The token is held in memory and refreshed a minute before it expires;
// nothing is written to disk and nothing is delivered to the pod.
func (c *kmsClient) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.expires) {
		return c.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.metadataBase+"instance/service-accounts/default/token", nil)
	if err != nil {
		return "", err
	}
	// Required by the metadata server, and it is also what makes the request
	// un-forgeable by a browser or a confused proxy: the header cannot be set
	// cross-origin.
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("key service: the workload identity token is unavailable from the metadata server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("key service: the metadata server refused a workload identity token (HTTP %d); the pod has no bound service account", resp.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&payload); err != nil {
		return "", fmt.Errorf("key service: parse workload identity token: %w", err)
	}
	if payload.AccessToken == "" {
		return "", errors.New("key service: the metadata server returned no access token")
	}
	lifetime := time.Duration(payload.ExpiresIn) * time.Second
	if lifetime > time.Minute {
		lifetime -= time.Minute
	}
	c.token, c.expires = payload.AccessToken, c.now().Add(lifetime)
	return c.token, nil
}

// call performs one Cloud KMS request. body nil means GET.
func (c *kmsClient) call(ctx context.Context, path string, body any, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	method, reader := http.MethodGet, io.Reader(nil)
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		method, reader = http.MethodPost, bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("key service: cloud kms request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		// Cloud KMS error bodies quote the resource and the caller's identity;
		// neither is secret, but the body can also echo request fields, so only
		// the status travels — as with the Vault backend.
		status := &statusError{service: "cloud kms", status: resp.StatusCode}
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("key service: cloud kms refused the workload identity on %s (HTTP %d): grant the pod's service account the matching Cloud KMS role on that key: %w", resourceOf(path), resp.StatusCode, status)
		default:
			return fmt.Errorf("key service: cloud kms returned %d for %s: %w", resp.StatusCode, resourceOf(path), status)
		}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// resourceOf strips the operation suffix so a refusal names the key rather than
// the verb that happened to reach it.
func resourceOf(path string) string {
	if resource, _, found := strings.Cut(path, ":"); found {
		return resource
	}
	return path
}

type kmsCryptoKey struct {
	Purpose         string `json:"purpose"`
	VersionTemplate struct {
		Algorithm string `json:"algorithm"`
	} `json:"versionTemplate"`
	Primary struct {
		Name  string `json:"name"`
		State string `json:"state"`
	} `json:"primary"`
}

type kmsCryptoKeyVersion struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Algorithm string `json:"algorithm"`
}

func versionOrdinal(name string) int {
	ordinal, err := strconv.Atoi(name[strings.LastIndexByte(name, '/')+1:])
	if err != nil {
		return 0
	}
	return ordinal
}

// KMSSealer is the Cloud KMS stored-credential family: a symmetric key for
// seal/open, with the purpose carried as Cloud KMS's own additional
// authenticated data, and a pinned HMAC key version for the keyed hash.
type KMSSealer struct {
	client      *kmsClient
	envelopeKey string
	macVersion  string
	fingerprint string
}

// NewKMSSealer validates both keys against the workload identity before
// returning, so a misnamed key, a key of the wrong purpose or algorithm, and a
// key the pod cannot reach each refuse at boot — while a deployment can still
// be fixed — rather than surfacing on the first MFA enrolment or API-key
// lookup.
func NewKMSSealer(ctx context.Context, config KMSConfig) (*KMSSealer, error) {
	if !cryptoKeyName.MatchString(config.EnvelopeKey) {
		return nil, fmt.Errorf("key service: KEY_SERVICE_KMS_ENVELOPE_KEY must be a Cloud KMS cryptoKey resource name (projects/<project>/locations/<location>/keyRings/<ring>/cryptoKeys/<key>), got %q", config.EnvelopeKey)
	}
	if !cryptoKeyVersionName.MatchString(config.MACKey) {
		return nil, fmt.Errorf("key service: KEY_SERVICE_KMS_MAC_KEY must be a Cloud KMS cryptoKeyVersion resource name (…/cryptoKeys/<key>/cryptoKeyVersions/<n>), got %q — the keyed hash behind API-key lookup is pinned to one version because moving it stops every API key already stored from matching", config.MACKey)
	}
	client := newKMSClient(config)
	sealer, err := newKMSEnvelope(ctx, client, config.EnvelopeKey)
	if err != nil {
		return nil, err
	}
	var macKey kmsCryptoKeyVersion
	if err := client.call(ctx, config.MACKey, nil, &macKey); err != nil {
		return nil, err
	}
	if macKey.Algorithm != algorithmHMACSHA256 {
		return nil, fmt.Errorf("key service: cloud kms key version %s is %s; the keyed hash requires %s", config.MACKey, macKey.Algorithm, algorithmHMACSHA256)
	}
	if macKey.State != versionStateEnabled {
		return nil, fmt.Errorf("key service: cloud kms key version %s is %s, not %s", config.MACKey, macKey.State, versionStateEnabled)
	}
	sealer.macVersion = config.MACKey
	return sealer, nil
}

// newKMSEnvelope validates a symmetric key and proves it works, returning a
// sealer with no MAC key bound. Shared by the envelope key and the key-wrapping
// key, because both need exactly these checks.
func newKMSEnvelope(ctx context.Context, client *kmsClient, keyName string) (*KMSSealer, error) {
	if err := requireCryptoKey(ctx, client, keyName, purposeEncryptDecrypt, algorithmSymmetric); err != nil {
		return nil, err
	}
	sealer := &KMSSealer{client: client, envelopeKey: keyName, fingerprint: keyFingerprint(keyName)}
	// A seal/open round trip at boot, because every static check above passes on
	// a key that is perfectly well-formed and simply is not the one that sealed
	// this deployment's data. Doing it here makes a wrong key name a startup
	// refusal, while the deployment can still be fixed, rather than a
	// per-credential 400 discovered during a sync.
	payload, err := sealer.Seal(ctx, kmsProbePurpose, kmsProbePlaintext)
	if err != nil {
		return nil, fmt.Errorf("key service: cloud kms key %s cannot seal: %w", keyName, err)
	}
	opened, err := sealer.Open(ctx, kmsProbePurpose, payload)
	if err != nil {
		return nil, fmt.Errorf("key service: cloud kms key %s cannot open what it sealed: %w", keyName, err)
	}
	if opened != kmsProbePlaintext {
		return nil, fmt.Errorf("key service: cloud kms key %s round-tripped a different value", keyName)
	}
	return sealer, nil
}

// The boot probe. It is sealed and immediately opened, never stored, so the
// value is arbitrary; the purpose is distinct from every real one so the probe
// cannot be confused with a credential.
const (
	kmsProbePurpose   = "key-service-boot-probe"
	kmsProbePlaintext = "boot-probe"
)

func requireCryptoKey(ctx context.Context, client *kmsClient, name, purpose, algorithm string) error {
	var key kmsCryptoKey
	if err := client.call(ctx, name, nil, &key); err != nil {
		return err
	}
	if key.Purpose != purpose {
		return fmt.Errorf("key service: cloud kms key %s has purpose %s; %s is required", name, key.Purpose, purpose)
	}
	if key.VersionTemplate.Algorithm != algorithm {
		return fmt.Errorf("key service: cloud kms key %s is %s; %s is required", name, key.VersionTemplate.Algorithm, algorithm)
	}
	return nil
}

func (k *KMSSealer) Tag() string { return TagGCPKMS }

// Identity names the tag and the key, so two Cloud KMS keys are two backends.
func (k *KMSSealer) Identity() string { return TagGCPKMS + ":" + k.fingerprint }

// Accepts requires the key as well as the tag.
//
// Cloud KMS answers a ciphertext from a FOREIGN key with the same 400 it answers
// a corrupt one, so routing on the tag alone makes a re-key — or a typo in
// KEY_SERVICE_KMS_ENVELOPE_KEY — indistinguishable from a credential that is
// genuinely unreadable, and callers act on that difference: a datasource sync
// turns an unreadable credential into a permanent "reconnect this source"
// instruction. Naming the key in the envelope is what lets the two be told
// apart, and it is what makes a key-to-key cutover expressible at all.
func (k *KMSSealer) Accepts(envelope Envelope) bool {
	if envelope.Backend != TagGCPKMS {
		return false
	}
	sealed, err := parseKMSPayload(envelope.Payload)
	return err == nil && sealed.key == k.fingerprint
}

// kmsPayload is this backend's share of the envelope: the key version that
// sealed the value, a fingerprint of the key itself, and the ciphertext.
type kmsPayload struct {
	version    int
	key        string
	ciphertext string
}

func parseKMSPayload(payload string) (kmsPayload, error) {
	version, rest, found := strings.Cut(payload, ":")
	if !found {
		return kmsPayload{}, fmt.Errorf("cloud kms payload names no key version: %w", business.ErrInvalidSecretEnvelope)
	}
	key, ciphertext, found := strings.Cut(rest, ":")
	if !found || key == "" || ciphertext == "" {
		return kmsPayload{}, fmt.Errorf("cloud kms payload names no key: %w", business.ErrInvalidSecretEnvelope)
	}
	// The version is written on the seal path, so a payload that does not carry
	// a number there was not written by this backend and is refused rather than
	// read past: accepting it would make the recorded version decorative.
	ordinal, err := strconv.Atoi(version)
	if err != nil || ordinal < 1 {
		return kmsPayload{}, fmt.Errorf("cloud kms payload has an invalid key version %q: %w", version, business.ErrInvalidSecretEnvelope)
	}
	return kmsPayload{version: ordinal, key: key, ciphertext: ciphertext}, nil
}

// keyFingerprint identifies a cryptoKey in an envelope without storing its
// resource name, which would put the project and key-ring layout in every
// credential row. Truncated to 12 base64url characters: it only has to
// distinguish the handful of keys one deployment binds, and it is compared, never
// resolved.
func keyFingerprint(name string) string {
	sum := sha256.Sum256([]byte(name))
	return base64.RawURLEncoding.EncodeToString(sum[:9])
}

// Seal encrypts under the key's primary version and records that version in the
// payload, so the stored value names both the backend and the key version that
// sealed it. The purpose is Cloud KMS's additional authenticated data: a
// ciphertext moved between rows fails to decrypt at all rather than decrypting
// and being refused afterwards.
func (k *KMSSealer) Seal(ctx context.Context, purpose, plaintext string) (string, error) {
	var result struct {
		Name       string `json:"name"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := k.client.call(ctx, k.envelopeKey+":encrypt", map[string]string{
		"plaintext":                   base64.StdEncoding.EncodeToString([]byte(plaintext)),
		"additionalAuthenticatedData": base64.StdEncoding.EncodeToString([]byte(purpose)),
	}, &result); err != nil {
		return "", err
	}
	if result.Ciphertext == "" {
		return "", errors.New("key service: cloud kms encrypt returned no ciphertext")
	}
	ordinal := versionOrdinal(result.Name)
	if ordinal == 0 {
		return "", fmt.Errorf("key service: cloud kms encrypt named no key version")
	}
	return strconv.Itoa(ordinal) + ":" + k.fingerprint + ":" + result.Ciphertext, nil
}

func (k *KMSSealer) Open(ctx context.Context, purpose, payload string) (string, error) {
	sealed, err := parseKMSPayload(payload)
	if err != nil {
		return "", err
	}
	// A value sealed under a DIFFERENT Cloud KMS key is a configuration answer,
	// never a broken credential. Cloud KMS cannot tell us apart from a corrupt
	// ciphertext — both are 400 — so the envelope's own key fingerprint is what
	// separates them, and this refusal deliberately does NOT wrap
	// business.ErrInvalidSecretEnvelope: callers treat that sentinel as
	// permanent and tell the user to re-enter the credential, which would turn
	// a mistyped key name into destroyed credentials across every tenant.
	if sealed.key != k.fingerprint {
		return "", fmt.Errorf("key service: this secret was sealed by a different Cloud KMS key (envelope names key %s, %s names key %s): bind the sealing key as the previous key-service backend until nothing references it, rather than re-entering credentials",
			sealed.key, KMSEnvelopeKeyKey, k.fingerprint)
	}
	var result struct {
		Plaintext string `json:"plaintext"`
	}
	if err := k.client.call(ctx, k.envelopeKey+":decrypt", map[string]string{
		"ciphertext":                  sealed.ciphertext,
		"additionalAuthenticatedData": base64.StdEncoding.EncodeToString([]byte(purpose)),
	}, &result); err != nil {
		// The key is the one that sealed this value, so a 400 here really is a
		// failed integrity check: a corrupt ciphertext, or associated data that
		// does not match. Those are indistinguishable and both are the
		// invalid-envelope answer every caller already handles.
		//
		// Every other status is an outage or a permission answer and must NOT
		// read as an invalid envelope: a caller that disabled a webhook endpoint
		// or dropped a source credential because Cloud KMS was briefly
		// unavailable would have destroyed something that was never broken.
		var status *statusError
		if errors.As(err, &status) && status.status == http.StatusBadRequest {
			return "", fmt.Errorf("%w: %w", business.ErrInvalidSecretEnvelope, err)
		}
		return "", err
	}
	plaintext, err := base64.StdEncoding.DecodeString(result.Plaintext)
	if err != nil || len(plaintext) == 0 {
		return "", fmt.Errorf("decode cloud kms plaintext: %w", business.ErrInvalidSecretEnvelope)
	}
	return string(plaintext), nil
}

func (k *KMSSealer) MAC(ctx context.Context, plaintext string) (string, error) {
	if !k.macKeyBound() {
		return "", errors.New("key service: this sealer is bound to an organization's key and may not compute the keyed hash; API-key lookup is by that hash, so it is always the deployment's own")
	}
	var result struct {
		MAC string `json:"mac"`
	}
	if err := k.client.call(ctx, k.macVersion+":macSign", map[string]string{
		"data": base64.StdEncoding.EncodeToString([]byte(plaintext)),
	}, &result); err != nil {
		return "", err
	}
	if result.MAC == "" {
		return "", errors.New("key service: cloud kms macSign returned no mac")
	}
	// Framed like an envelope so a hash this backend produced is recognisable
	// and names the key version and key behind it. Only hashes written by THIS
	// backend are self-describing: the Vault backend returns Vault's own
	// `vault:v<N>:` form, so an audit of what is still looked up under Vault
	// cannot be done by reading hashes alone — CandidateHashes is what keeps a
	// cutover working, not this framing.
	return Envelope{
		Backend: TagGCPKMS,
		Payload: strconv.Itoa(versionOrdinal(k.macVersion)) + ":" + k.fingerprint + ":" + result.MAC,
	}.String(), nil
}

// The associated data every wrapped signing key is bound to. A wrapped key is
// not interchangeable with a sealed credential: binding the purpose means a
// stored credential's ciphertext cannot be presented as a signing key, and a
// wrapped signing key cannot be opened through the stored-credential path.
const signingKeyWrapPurpose = "signing-key-wrap"

// KMSKeyWrap is a Cloud KMS symmetric key used only to unwrap the Ed25519
// signing key.
//
// This is the weaker of the two `kms` shapes and exists because the key material
// is still needed in process: the Work Context signer, the delegation minter and
// the OAuth state signer are each HANDED the key rather than being given a
// signer to call, so until they accept an injected signer the key cannot stay
// inside the key service. What this does buy is the rest of the goal — no
// secrets store to run, unseal, back up and credential, and nothing stored that
// is a credential on its own: the wrapped key is inert without the Cloud KMS key
// and the workload identity that reaches it.
type KMSKeyWrap struct {
	wrap *KMSSealer
	// name is kept for refusals, which have to say WHICH key could not unwrap.
	name string
}

// NewKMSKeyWrap validates the wrapping key the same way the envelope key is
// validated, including the boot round trip.
func NewKMSKeyWrap(ctx context.Context, config KMSConfig) (*KMSKeyWrap, error) {
	if !cryptoKeyName.MatchString(config.SigningWrapKey) {
		return nil, fmt.Errorf("key service: %s must be a Cloud KMS cryptoKey resource name (projects/<project>/locations/<location>/keyRings/<ring>/cryptoKeys/<key>), got %q", KMSSigningWrapKeyKey, config.SigningWrapKey)
	}
	wrap, err := newKMSEnvelope(ctx, newKMSClient(config), config.SigningWrapKey)
	if err != nil {
		return nil, err
	}
	return &KMSKeyWrap{wrap: wrap, name: config.SigningWrapKey}, nil
}

// UnwrapSigningKey turns the configured ciphertext into the Ed25519 private key.
//
// The wrapped value is an envelope in the same framing as a stored credential,
// so it records the Cloud KMS key that produced it and a value wrapped by a
// different key is refused by name rather than read as corrupt.
func (w *KMSKeyWrap) UnwrapSigningKey(ctx context.Context, wrapped string) (ed25519.PrivateKey, error) {
	envelope, err := ParseEnvelope(strings.TrimSpace(wrapped))
	if err != nil {
		return nil, fmt.Errorf("%s is not a key-service envelope: %w — wrap the Ed25519 seed with the Cloud KMS key named in %s, under the associated data %q", SigningKeyWrappedKey, err, KMSSigningWrapKeyKey, signingKeyWrapPurpose)
	}
	if !w.wrap.Accepts(envelope) {
		return nil, fmt.Errorf("%s was wrapped by a different Cloud KMS key than %s names (%s): bind the key that wrapped it, or re-wrap the seed with this one", SigningKeyWrappedKey, KMSSigningWrapKeyKey, w.name)
	}
	encoded, err := w.wrap.Open(ctx, signingKeyWrapPurpose, envelope.Payload)
	if err != nil {
		return nil, fmt.Errorf("unwrap the signing key with cloud kms key %s: %w", w.name, err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("key service: the unwrapped signing key is not base64: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("key service: the unwrapped signing key is %d bytes, want an %d-byte Ed25519 seed", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// SealerFor binds another Cloud KMS key, for an organization whose credentials
// are sealed under a key of its own.
//
// The key is validated and round-tripped exactly as the deployment's own is:
// purpose, algorithm, and a seal/open probe. An organization pointed at a key
// that is well-formed and simply not usable must fail when the binding is read,
// not at the first credential that needs it.
func (k *KMSSealer) SealerFor(ctx context.Context, keyRef string) (Sealer, error) {
	if !cryptoKeyName.MatchString(keyRef) {
		return nil, fmt.Errorf("key service: an organization's Cloud KMS key must be a cryptoKey resource name (projects/<project>/locations/<location>/keyRings/<ring>/cryptoKeys/<key>), got %q", keyRef)
	}
	if keyRef == k.envelopeKey {
		return nil, fmt.Errorf("key service: %s is this deployment's own envelope key and cannot be an organization's", keyRef)
	}
	sealer, err := newKMSEnvelope(ctx, k.client, keyRef)
	if err != nil {
		return nil, err
	}
	// The keyed hash never moves off the deployment's key, so a per-organization
	// sealer carries no MAC key and MAC on it is a programming error rather than
	// a silent hash under the wrong key.
	return sealer, nil
}

// MAC refuses on a sealer bound to an organization's key.
//
// API-key lookup is BY the hash, so a hash computed under an organization's key
// could never be found: the lookup happens before the organization is known. The
// deployment's own sealer is the only one that may compute it, and this refuses
// rather than quietly producing a value nothing will ever match.
func (k *KMSSealer) macKeyBound() bool { return k.macVersion != "" }
