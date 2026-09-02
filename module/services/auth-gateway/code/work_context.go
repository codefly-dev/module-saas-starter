package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
)

const (
	workContextJWKSPath           = "/v1/auth/.well-known/jwks.json"
	workContextJWKSCacheTTL       = 5 * time.Minute
	workContextJWKSRequestTimeout = 2 * time.Second
	workContextJWKSMaxBytes       = 256 * 1024
	workContextJWKSMaxKeys        = 64
)

// workContextVerifier verifies presented Work Contexts at the edge against the
// access-token JWKS published by accounts. The Work Context signing key is the
// access-token key, so the public gateway route serves both and the edge never
// needs Vault.
//
// Every failure — a forged signature, an expired or widened context, an unknown
// key id, or an accounts outage that makes the key set unreachable — collapses
// to codefly.ErrWorkContextInvalid. The edge answers 401 without leaking which
// check failed or that a dependency is down.
type workContextVerifier struct {
	url        string
	httpClient *http.Client
	cacheTTL   time.Duration
	timeout    time.Duration
	now        func() time.Time

	mu sync.Mutex
	// unknownProbed bounds JWKS refetches for unknown key ids to one per cache
	// window, so a stream of garbage tokens carrying attacker-chosen key ids
	// cannot turn verification into an unbounded fetch loop. A TTL refresh opens
	// a fresh probe window (rotation is picked up within one cache TTL).
	verifier      *codefly.WorkContextVerifier
	keyIDs        map[string]struct{}
	expiresAt     time.Time
	unknownProbed bool
}

func newWorkContextVerifier(accountsBaseURL string) *workContextVerifier {
	return &workContextVerifier{
		url:      strings.TrimSuffix(accountsBaseURL, "/") + workContextJWKSPath,
		cacheTTL: workContextJWKSCacheTTL,
		timeout:  workContextJWKSRequestTimeout,
		now:      time.Now,
		httpClient: &http.Client{
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Refresh warms the cached key set. A callee calls it at boot so verification
// fails closed from the first request rather than racing the first fetch.
func (v *workContextVerifier) Refresh(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.refreshLocked(ctx)
}

// Verify establishes trust for a presented Work Context. It confirms the
// signature, freshness, and attenuation of the token against the published
// keys; audience and scope are the callee's concern and are not asserted here.
func (v *workContextVerifier) Verify(ctx context.Context, token codefly.WorkContextToken) error {
	keyID, err := workContextTokenKeyID(token)
	if err != nil {
		return err
	}
	verifier, err := v.verifierForKey(ctx, keyID)
	if err != nil {
		return err
	}
	if _, err := verifier.Verify(token, codefly.WorkContextExpectations{}); err != nil {
		return invalidWorkContext(err)
	}
	return nil
}

func (v *workContextVerifier) verifierForKey(
	ctx context.Context,
	keyID string,
) (*codefly.WorkContextVerifier, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	refreshed := false
	if v.verifier == nil || !v.now().Before(v.expiresAt) {
		if err := v.refreshLocked(ctx); err != nil {
			return nil, err
		}
		refreshed = true
	}
	if _, known := v.keyIDs[keyID]; !known && !refreshed && !v.unknownProbed {
		if err := v.refreshLocked(ctx); err != nil {
			return nil, err
		}
		v.unknownProbed = true
	}
	return v.verifier, nil
}

func (v *workContextVerifier) refreshLocked(ctx context.Context) error {
	keys, err := v.fetch(ctx)
	if err != nil {
		return err
	}
	verifier, err := codefly.NewWorkContextVerifier(codefly.WorkContextVerifierOptions{
		PublicKeys: keys,
		Now:        v.now,
	})
	if err != nil {
		return invalidWorkContext(err)
	}
	keyIDs := make(map[string]struct{}, len(keys))
	for keyID := range keys {
		keyIDs[keyID] = struct{}{}
	}
	v.verifier = verifier
	v.keyIDs = keyIDs
	v.expiresAt = v.now().Add(v.cacheTTL)
	v.unknownProbed = false
	return nil
}

func (v *workContextVerifier) fetch(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	requestCtx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, v.url, nil)
	if err != nil {
		return nil, invalidWorkContext(err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := v.httpClient.Do(request)
	if err != nil {
		return nil, invalidWorkContext(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: JWKS returned HTTP %d", codefly.ErrWorkContextInvalid, response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, workContextJWKSMaxBytes+1))
	if err != nil {
		return nil, invalidWorkContext(err)
	}
	if len(payload) > workContextJWKSMaxBytes {
		return nil, fmt.Errorf("%w: JWKS exceeds %d bytes", codefly.ErrWorkContextInvalid, workContextJWKSMaxBytes)
	}
	return parseWorkContextJWKS(payload)
}

func parseWorkContextJWKS(payload []byte) (map[string]ed25519.PublicKey, error) {
	var document struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			Kid string `json:"kid"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, invalidWorkContext(err)
	}
	if len(document.Keys) == 0 || len(document.Keys) > workContextJWKSMaxKeys {
		return nil, fmt.Errorf("%w: JWKS must contain between 1 and %d keys", codefly.ErrWorkContextInvalid, workContextJWKSMaxKeys)
	}
	keys := make(map[string]ed25519.PublicKey, len(document.Keys))
	for _, key := range document.Keys {
		if key.Kty != "OKP" || key.Crv != "Ed25519" ||
			(key.Alg != "" && key.Alg != "EdDSA") ||
			(key.Use != "" && key.Use != "sig") {
			continue
		}
		if key.Kid == "" {
			return nil, fmt.Errorf("%w: JWKS key is missing a key id", codefly.ErrWorkContextInvalid)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: JWKS key %q is not a valid Ed25519 key", codefly.ErrWorkContextInvalid, key.Kid)
		}
		if _, duplicate := keys[key.Kid]; duplicate {
			return nil, fmt.Errorf("%w: JWKS has duplicate key id %q", codefly.ErrWorkContextInvalid, key.Kid)
		}
		keys[key.Kid] = decoded
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: JWKS has no Ed25519 signing key", codefly.ErrWorkContextInvalid)
	}
	return keys, nil
}

func workContextTokenKeyID(token codefly.WorkContextToken) (string, error) {
	segment, _, found := strings.Cut(token.Encoded(), ".")
	if !found {
		return "", fmt.Errorf("%w: malformed token", codefly.ErrWorkContextInvalid)
	}
	payload, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return "", invalidWorkContext(err)
	}
	probe := struct {
		KeyID string `json:"key_id"`
	}{}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return "", invalidWorkContext(err)
	}
	if probe.KeyID == "" {
		return "", fmt.Errorf("%w: token is missing a key id", codefly.ErrWorkContextInvalid)
	}
	return probe.KeyID, nil
}

// invalidWorkContext folds an arbitrary underlying failure into the single
// invalid sentinel, so callers see one error class regardless of cause.
func invalidWorkContext(err error) error {
	return fmt.Errorf("%w: %v", codefly.ErrWorkContextInvalid, err)
}
