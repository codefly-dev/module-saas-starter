package keyservice

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	ed25519minter "accounts/pkg/auth/ed25519"
	"accounts/pkg/business"
	"accounts/pkg/vaultconnection"
)

// defaultTransitKeyName is the Transit key this deployment seals under when an
// organization has no key of its own, and the key the keyed hash is ALWAYS
// computed under.
//
// The hash cannot move: API-key lookup is BY that value, so a hash computed
// under another key stops matching every key already stored. The envelope key
// can move — per organization, or on a cutover — because every sealed value
// records which key sealed it. Two different questions, which is why they are
// two fields on the sealer rather than one name.
const defaultTransitKeyName = "api-keys"

// VaultSealer is the Vault Transit backend: the AppRole + Transit binding
// accounts has always used, behind the key-service seam and otherwise
// unchanged. Its payload is the Vault ciphertext, which carries its own
// `vault:v<N>:` key-version prefix, so the envelope adds nothing to it.
type VaultSealer struct {
	address    string
	token      string
	connection *vaultconnection.Connection
	client     *http.Client
	// envelopeKey seals and opens. Empty means defaultTransitKeyName, and a
	// value sealed under that key is stored in the pre-per-key payload shape so
	// nothing already written moves.
	envelopeKey string
}

// NewVaultSealer binds the cell's Vault through the `vault` configuration
// group.
func NewVaultSealer(ctx context.Context) (*VaultSealer, error) {
	connection, err := vaultconnection.Load(ctx)
	if err != nil {
		return nil, err
	}
	return &VaultSealer{address: connection.Address, connection: connection, client: connection.Client}, nil
}

// NewVaultSealerDirect binds an explicit address and token, for a local run and
// for tests against a Vault dev server.
func NewVaultSealerDirect(address, token string) *VaultSealer {
	return &VaultSealer{address: address, token: token, client: http.DefaultClient}
}

func (v *VaultSealer) Tag() string { return TagVaultTransit }

// Identity is the tag: see Accepts.
func (v *VaultSealer) Identity() string { return TagVaultTransit + ":" + v.transitKey() }

// transitKey is the Transit key this sealer seals under.
func (v *VaultSealer) transitKey() string {
	if v.envelopeKey == "" {
		return defaultTransitKeyName
	}
	return v.envelopeKey
}

// Accepts requires the tag and the Transit key the payload names.
//
// A Vault ciphertext carries its key VERSION (`vault:v<N>:`) and not its key
// NAME, so the payload has to name the key itself or a per-organization key
// could not be told from the deployment's on read — and Vault would answer a
// foreign key's ciphertext with the same failure it gives a corrupt one.
func (v *VaultSealer) Accepts(envelope Envelope) bool {
	if envelope.Backend != TagVaultTransit {
		return false
	}
	key, _ := splitVaultPayload(envelope.Payload)
	return key == v.transitKey()
}

// splitVaultPayload reads the optional key segment.
//
// The pre-per-key shape is a single base64url ciphertext, which belongs to
// defaultTransitKeyName — that is what every value written before per-organization
// keys existed looks like, and it keeps opening unchanged. base64url contains no
// ":", so a payload carrying one is the keyed shape and the two cannot be
// confused.
func splitVaultPayload(payload string) (key, ciphertext string) {
	if name, rest, found := strings.Cut(payload, ":"); found {
		return name, rest
	}
	return defaultTransitKeyName, payload
}

// Health hits Vault's /v1/sys/health and returns nil only on the canonical
// 200 / 429 / 472 / 473 codes that signal a reachable and un-sealed Vault. Used
// by the /v1/status probe, so it has to be fast enough for the 2s budget.
func (v *VaultSealer) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.address+"/v1/sys/health", nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// Vault docs: 200 standby/active OK, 429 standby (still up), 472/473
	// disaster-recovery secondary. Anything else is unhealthy.
	switch resp.StatusCode {
	case http.StatusOK, http.StatusTooManyRequests, 472, 473:
		return nil
	default:
		return fmt.Errorf("vault unhealthy: HTTP %d", resp.StatusCode)
	}
}

func (v *VaultSealer) MAC(ctx context.Context, plaintext string) (string, error) {
	body, err := json.Marshal(map[string]string{
		"input": base64.StdEncoding.EncodeToString([]byte(plaintext)),
	})
	if err != nil {
		return "", err
	}
	// defaultTransitKeyName, never v.transitKey(): see the constant.
	result, err := v.request(ctx, "/v1/transit/hmac/"+defaultTransitKeyName, string(body))
	if err != nil {
		return "", fmt.Errorf("vault hmac request: %w", err)
	}
	hmac, ok := result["hmac"].(string)
	if !ok || hmac == "" {
		return "", errors.New("vault transit response missing hmac")
	}
	return hmac, nil
}

func (v *VaultSealer) Seal(ctx context.Context, purpose, plaintext string) (string, error) {
	payload, err := json.Marshal(purposeBoundPayload{Purpose: purpose, Value: plaintext})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]string{
		"plaintext": base64.StdEncoding.EncodeToString(payload),
	})
	if err != nil {
		return "", err
	}
	result, err := v.request(ctx, "/v1/transit/encrypt/"+v.transitKey(), string(body))
	if err != nil {
		return "", err
	}
	ciphertext, ok := result["ciphertext"].(string)
	if !ok || ciphertext == "" {
		return "", errors.New("vault transit response missing ciphertext")
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(ciphertext))
	if v.envelopeKey == "" {
		// The deployment's own key keeps the shape it has always written, so a
		// value sealed before per-organization keys existed and one sealed now
		// are byte-identical in form.
		return encoded, nil
	}
	return v.envelopeKey + ":" + encoded, nil
}

func (v *VaultSealer) Open(ctx context.Context, purpose, payload string) (string, error) {
	_, encoded := splitVaultPayload(payload)
	ciphertext, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(ciphertext) == 0 {
		return "", fmt.Errorf("invalid secret envelope: %w", business.ErrInvalidSecretEnvelope)
	}
	body, err := json.Marshal(map[string]string{"ciphertext": string(ciphertext)})
	if err != nil {
		return "", err
	}
	result, err := v.request(ctx, "/v1/transit/decrypt/"+v.transitKey(), string(body))
	if err != nil {
		return "", err
	}
	encoded, ok := result["plaintext"].(string)
	if !ok || encoded == "" {
		return "", errors.New("vault transit response missing plaintext")
	}
	plaintext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode vault plaintext: %w", err)
	}
	// Vault Transit's associated data is carried inside the plaintext rather
	// than beside the ciphertext, so the purpose is checked here. A payload
	// moved between rows decrypts and is then refused.
	return openPurposeBoundPayload(purpose, plaintext)
}

func (v *VaultSealer) request(ctx context.Context, path, body string) (map[string]any, error) {
	data, err := v.requestOnce(ctx, path, body)
	var refused *statusError
	if v.connection != nil && errors.As(err, &refused) && refused.status == http.StatusForbidden {
		// Vault refused the token itself (revoked, or its lease ended early):
		// an AppRole connection logs in again and this call is retried once. A
		// second refusal is a policy answer and is returned as is.
		v.connection.Invalidate()
		return v.requestOnce(ctx, path, body)
	}
	return data, err
}

func (v *VaultSealer) requestOnce(ctx context.Context, path, body string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.address+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	token := v.token
	if v.connection != nil {
		token, err = v.connection.Token()
		if err != nil {
			return nil, err
		}
	}
	req.Header.Set("X-Vault-Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, &statusError{service: "vault", status: resp.StatusCode}
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, err
	}
	return envelope.Data, nil
}

// ReadVaultSigningKey reads the Ed25519 signing keypair from the Vault the
// `vault` group names, and returns the private half.
//
// The key service owns the signing key's CUSTODY — which service holds it, and
// the refusal when that service cannot answer — but not the signing itself: the
// key material stays in this process because three consumers are handed the key
// and sign on their own (the Work Context signer and the delegation minter,
// which take an ed25519.PrivateKey in github.com/codefly-dev/sdk-go and
// github.com/codefly-dev/core, and the OAuth state signer's seed). Signing
// through a key service that never exports the key needs those three to accept a
// crypto.Signer first, which is why no backend here offers a Sign operation:
// an unreachable one would be an interface nothing could satisfy usefully.
//
// Custody of this key is the cell's, never accounts': see module/KEY_ROTATION.md.
func ReadVaultSigningKey(ctx context.Context, connection *vaultconnection.Connection) (ed25519.PrivateKey, error) {
	token, err := connection.Token()
	if err != nil {
		return nil, err
	}
	// The connection already resolved and enforced the mesh assertion for this
	// address; passing it on keeps the loader's own check consistent with the
	// one that admitted the connection rather than re-deriving it from
	// configuration a second time.
	return ed25519minter.LoadKeyFromVault(ctx, ed25519minter.VaultKeyLoaderConfig{
		Address: connection.Address, Token: token, HTTPClient: connection.Client,
		MeshProtected: connection.MeshProtected,
	})
}

// purposeBoundPayload is the plaintext a backend without native associated data
// seals. The purpose travels with the value so a ciphertext copied between rows
// opens and is then refused.
type purposeBoundPayload struct {
	Purpose string `json:"purpose"`
	Value   string `json:"value"`
}

func openPurposeBoundPayload(purpose string, plaintext []byte) (string, error) {
	var payload purposeBoundPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return "", fmt.Errorf("decode secret payload: %w", business.ErrInvalidSecretEnvelope)
	}
	if payload.Purpose != purpose || payload.Value == "" {
		return "", fmt.Errorf("secret purpose mismatch: %w", business.ErrInvalidSecretEnvelope)
	}
	return payload.Value, nil
}

// SealerFor binds another Transit key of the same Vault, for an organization
// whose credentials are sealed under a key of its own.
//
// No boot probe here, unlike the Cloud KMS factory: a Transit key is created on
// first encrypt (when the policy grants it), so probing would either create the
// key as a side effect of reading a binding or fail on a key that is legitimately
// not yet used. The first real seal is the check.
func (v *VaultSealer) SealerFor(_ context.Context, keyRef string) (Sealer, error) {
	if err := validTransitKeyName(keyRef); err != nil {
		return nil, err
	}
	bound := *v
	bound.envelopeKey = keyRef
	return &bound, nil
}

// validTransitKeyName refuses a name that would change the request path rather
// than the key: the name is interpolated into /v1/transit/encrypt/<name>, so a
// slash or a traversal segment would address a different endpoint entirely.
func validTransitKeyName(name string) error {
	if name == "" {
		return errors.New("key service: a Vault transit key name is required")
	}
	if name == defaultTransitKeyName {
		return fmt.Errorf("key service: %q is this deployment's own transit key and cannot be an organization's", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("key service: invalid Vault transit key name %q: letters, digits, dash, underscore and dot only", name)
		}
	}
	return nil
}
