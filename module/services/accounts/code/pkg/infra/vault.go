package infra

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/codefly-dev/core/wool"
)

// VaultClient provides transit encryption and hashing via HashiCorp Vault.
type VaultClient struct {
	address    string
	token      string
	transitKey string
}

func NewVaultClient(ctx context.Context) (*VaultClient, error) {
	w := wool.Get(ctx).In("NewVaultClient")

	address, err := codefly.For(ctx).Service("vault").Configuration("vault", "address")
	if err != nil {
		return nil, w.Wrapf(err, "failed to get vault address")
	}

	token, err := codefly.For(ctx).Service("vault").Secret("vault", "token")
	if err != nil {
		return nil, w.Wrapf(err, "failed to get vault token")
	}

	return NewVaultClientDirect(address, token), nil
}

// NewVaultClientDirect creates a vault client with explicit address and token (for local/test use).
func NewVaultClientDirect(address, token string) *VaultClient {
	return &VaultClient{
		address:    address,
		token:      token,
		transitKey: "api-keys",
	}
}

// Health hits Vault's /v1/sys/health endpoint and returns nil only on
// the canonical 200 / 429 / 472 / 473 codes that signal a reachable
// + un-sealed vault. Used by the /v1/status probe — needs to be
// fast enough for the 2s budget.
func (v *VaultClient) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.address+"/v1/sys/health", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Vault docs: 200 standby/active OK, 429 standby (still up),
	// 472/473 disaster-recovery secondary. Anything else = unhealthy.
	switch resp.StatusCode {
	case http.StatusOK, http.StatusTooManyRequests, 472, 473:
		return nil
	default:
		return fmt.Errorf("vault unhealthy: HTTP %d", resp.StatusCode)
	}
}

// HashKey hashes an API key using vault transit HMAC.
// Falls back to local SHA-256 if vault is unavailable.
func (v *VaultClient) HashKey(ctx context.Context, plaintext string) (string, error) {
	input := base64.StdEncoding.EncodeToString([]byte(plaintext))
	body := fmt.Sprintf(`{"input":"%s"}`, input)

	result, err := v.request(ctx, http.MethodPost,
		fmt.Sprintf("/v1/transit/hmac/%s", v.transitKey), body)
	if err != nil {
		// Fallback to local SHA-256
		h := sha256.Sum256([]byte(plaintext))
		return hex.EncodeToString(h[:]), nil
	}

	hmac, ok := result["hmac"].(string)
	if !ok {
		h := sha256.Sum256([]byte(plaintext))
		return hex.EncodeToString(h[:]), nil
	}
	return hmac, nil
}

func (v *VaultClient) request(ctx context.Context, method, path, body string) (map[string]interface{}, error) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, v.address+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", v.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("vault %s %s returned %d: %s", method, path, resp.StatusCode, string(respBody))
	}

	var envelope struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, err
	}
	return envelope.Data, nil
}
