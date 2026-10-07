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

// transitKeyName is the Transit key every purpose is sealed and hashed under.
// It is a single name on purpose: the keyed hash behind API-key lookup must stay
// comparable with every hash already stored, so the key it is computed under
// cannot move.
const transitKeyName = "api-keys"

// VaultSealer is the Vault Transit backend: the AppRole + Transit binding
// accounts has always used, behind the key-service seam and otherwise
// unchanged. Its payload is the Vault ciphertext, which carries its own
// `vault:v<N>:` key-version prefix, so the envelope adds nothing to it.
type VaultSealer struct {
	address    string
	token      string
	connection *vaultconnection.Connection
	client     *http.Client
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
func (v *VaultSealer) Identity() string { return TagVaultTransit }

// Accepts needs only the tag: the Transit key is the `api-keys` constant rather
// than a configured name, so one deployment has exactly one Vault envelope key
// and nothing to disambiguate.
func (v *VaultSealer) Accepts(envelope Envelope) bool { return envelope.Backend == TagVaultTransit }

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
	result, err := v.request(ctx, "/v1/transit/hmac/"+transitKeyName, string(body))
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
	result, err := v.request(ctx, "/v1/transit/encrypt/"+transitKeyName, string(body))
	if err != nil {
		return "", err
	}
	ciphertext, ok := result["ciphertext"].(string)
	if !ok || ciphertext == "" {
		return "", errors.New("vault transit response missing ciphertext")
	}
	return base64.RawURLEncoding.EncodeToString([]byte(ciphertext)), nil
}

func (v *VaultSealer) Open(ctx context.Context, purpose, payload string) (string, error) {
	ciphertext, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || len(ciphertext) == 0 {
		return "", fmt.Errorf("invalid secret envelope: %w", business.ErrInvalidSecretEnvelope)
	}
	body, err := json.Marshal(map[string]string{"ciphertext": string(ciphertext)})
	if err != nil {
		return "", err
	}
	result, err := v.request(ctx, "/v1/transit/decrypt/"+transitKeyName, string(body))
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
