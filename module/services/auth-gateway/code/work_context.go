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
	// inflight coalesces concurrent JWKS fetches into a single network
	// round-trip. Without it, a burst of Work Context requests against a cold
	// cache — or a stream of requests during an accounts outage, where each
	// fetch fails and leaves the cache cold — would each issue their own fetch;
	// the shared call means at most one is in flight at a time. The fetch itself
	// runs without holding mu, so verification never blocks on network I/O while
	// the lock is held.
	inflight *jwksFetch
}

// jwksFetch is one shared, in-flight JWKS fetch. Waiters block on done and then
// read result/err, which the leader publishes before closing done.
type jwksFetch struct {
	done   chan struct{}
	result *jwksSnapshot
	err    error
}

// jwksSnapshot is an immutable verifier built from one fetched key set.
type jwksSnapshot struct {
	verifier *codefly.WorkContextVerifier
	keyIDs   map[string]struct{}
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
	if _, err := v.coalescedFetch(ctx); err != nil {
		return err
	}
	// A full refresh installs the current key set, so restore the probe budget:
	// the next unknown key id may legitimately be a freshly rotated-in key.
	v.mu.Lock()
	v.unknownProbed = false
	v.mu.Unlock()
	return nil
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
	if v.verifier != nil && v.now().Before(v.expiresAt) {
		if _, known := v.keyIDs[keyID]; known || v.unknownProbed {
			verifier := v.verifier
			v.mu.Unlock()
			return verifier, nil
		}
		// Fresh cache, unknown key id, probe budget available: spend it now,
		// before releasing the lock, so concurrent unknown-key requests don't
		// each schedule a probe.
		v.unknownProbed = true
		v.mu.Unlock()
		snapshot, err := v.coalescedFetch(ctx)
		if err != nil {
			return nil, err
		}
		return snapshot.verifier, nil
	}
	v.mu.Unlock()

	// Cold or expired cache: refresh and open a fresh probe window.
	snapshot, err := v.coalescedFetch(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	v.unknownProbed = false
	v.mu.Unlock()
	return snapshot.verifier, nil
}

// coalescedFetch performs one JWKS fetch shared across concurrent callers and
// installs the result as the current key set. The network round-trip runs
// without holding mu; the cache swap and the inflight hand-off happen under one
// locked section, so a caller that arrives after the leader finishes sees the
// fresh cache rather than starting a second fetch.
func (v *workContextVerifier) coalescedFetch(ctx context.Context) (*jwksSnapshot, error) {
	v.mu.Lock()
	if call := v.inflight; call != nil {
		v.mu.Unlock()
		select {
		case <-call.done:
			return call.result, call.err
		case <-ctx.Done():
			return nil, invalidWorkContext(ctx.Err())
		}
	}
	call := &jwksFetch{done: make(chan struct{})}
	v.inflight = call
	v.mu.Unlock()

	snapshot, err := v.fetchSnapshot(ctx)

	v.mu.Lock()
	v.inflight = nil
	if err == nil {
		v.verifier = snapshot.verifier
		v.keyIDs = snapshot.keyIDs
		v.expiresAt = v.now().Add(v.cacheTTL)
	}
	v.mu.Unlock()

	call.result, call.err = snapshot, err
	close(call.done)
	return snapshot, err
}

func (v *workContextVerifier) fetchSnapshot(ctx context.Context) (*jwksSnapshot, error) {
	keys, err := v.fetch(ctx)
	if err != nil {
		return nil, err
	}
	verifier, err := codefly.NewWorkContextVerifier(codefly.WorkContextVerifierOptions{
		PublicKeys: keys,
		Now:        v.now,
	})
	if err != nil {
		return nil, invalidWorkContext(err)
	}
	keyIDs := make(map[string]struct{}, len(keys))
	for keyID := range keys {
		keyIDs[keyID] = struct{}{}
	}
	return &jwksSnapshot{verifier: verifier, keyIDs: keyIDs}, nil
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
