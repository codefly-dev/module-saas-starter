package ed25519minter

// Vault-backed Ed25519 keypair loading.
//
// In production, the JWT signing key MUST persist across api restarts —
// an ephemeral key invalidates every active session on every deploy.
// The key lives in Vault KV v2 at secret/data/jwt-signing-key as a JSON
// envelope:
//
//	{
//	  "data": {
//	    "data": {
//	      "private_key": "<base64 std Ed25519 seed>",
//	      "public_key":  "<base64 std Ed25519 public key>"
//	    }
//	  }
//	}
//
// The vault service used by the dev fixture already seeds this path on
// first boot (see services/vault start). For deployed environments a
// one-shot seeding script writes the keypair once and Vault persists it.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// VaultKeyLoaderConfig is the minimum input to fetch the signing key.
type VaultKeyLoaderConfig struct {
	// Address is the Vault base URL, e.g. "http://localhost:8200".
	Address string
	// Token is the Vault auth token with read permission on the secret.
	Token string
	// SecretPath is the KV v2 path. Defaults to "secret/data/jwt-signing-key".
	SecretPath string
	// HTTPClient is used for the HTTP GET. Defaults to a 5s-timeout client.
	HTTPClient *http.Client
	// AllowInsecureHTTP permits fetching the key over cleartext http from a
	// non-loopback host. Both the Vault token and the returned private key then
	// travel unprotected at the app layer, so this may be set only when the
	// operator knows the connection is protected out of band — e.g. an mTLS
	// service mesh wraps the hop. The safety of a given path is a deployment
	// fact the operator asserts; it cannot be inferred from the address.
	AllowInsecureHTTP bool
}

// LoadKeyFromVault fetches the Ed25519 keypair from Vault KV v2.
// Returns the private key ready to pass into New(...).
func LoadKeyFromVault(ctx context.Context, cfg VaultKeyLoaderConfig) (ed25519.PrivateKey, error) {
	if cfg.Address == "" {
		return nil, fmt.Errorf("ed25519minter: vault address is required")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("ed25519minter: vault token is required")
	}
	if err := validateVaultAddress(cfg.Address, cfg.AllowInsecureHTTP); err != nil {
		return nil, err
	}
	if cfg.SecretPath == "" {
		cfg.SecretPath = "secret/data/jwt-signing-key"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		cfg.Address+"/v1/"+cfg.SecretPath, nil)
	if err != nil {
		return nil, fmt.Errorf("ed25519minter: build vault request: %w", err)
	}
	req.Header.Set("X-Vault-Token", cfg.Token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ed25519minter: fetch vault key: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ed25519minter: read vault response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ed25519minter: vault http %d: %s", resp.StatusCode, string(body))
	}

	var envelope struct {
		Data struct {
			Data struct {
				PrivateKey string `json:"private_key"`
				PublicKey  string `json:"public_key"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("ed25519minter: parse vault response: %w", err)
	}
	if envelope.Data.Data.PrivateKey == "" {
		return nil, fmt.Errorf("ed25519minter: vault response missing private_key")
	}

	seed, err := base64.StdEncoding.DecodeString(envelope.Data.Data.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("ed25519minter: decode private_key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("ed25519minter: wrong seed size: got %d want %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// validateVaultAddress rejects fetching the signing key over cleartext http
// where the X-Vault-Token and the returned Ed25519 private key would travel the
// wire in the clear. Over http:// two destinations stay allowed:
//
//   - loopback, always, so the dev fixture (http://localhost:8200) keeps
//     working — traffic to 127.0.0.0/8 or ::1 never leaves the host; and
//   - any host, when allowInsecureHTTP is set — the operator's explicit
//     assertion that this connection is protected out of band (e.g. an mTLS
//     service mesh wraps the hop, as with the in-cluster dev-vault).
//
// The out-of-band case is an operator decision, not something derivable from the
// address: a hostname suffix like ".svc" says nothing about whether the peer is
// actually enrolled in the mesh, and an ExternalName service can even resolve a
// ".svc.cluster.local" name to a public host. So confidentiality is gated on the
// operator's assertion rather than a naming heuristic.
func validateVaultAddress(address string, allowInsecureHTTP bool) error {
	u, err := url.Parse(address)
	if err != nil {
		return fmt.Errorf("ed25519minter: parse vault address: %w", err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) || allowInsecureHTTP {
			return nil
		}
		return fmt.Errorf("ed25519minter: refusing to fetch the signing key over cleartext http from non-loopback host %q; use https, or opt in to insecure http only when the connection is protected out of band (e.g. an mTLS mesh)", u.Host)
	default:
		return fmt.Errorf("ed25519minter: vault address must use http or https, got scheme %q", u.Scheme)
	}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
