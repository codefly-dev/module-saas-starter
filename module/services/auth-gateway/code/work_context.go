package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
)

// accountsJWKSPath is the standards-named endpoint accounts publishes its
// Ed25519 public keys on. It serves one document for both edge verification
// paths: the Work Context signing key is the access-token signing key.
const accountsJWKSPath = "/v1/auth/.well-known/jwks.json"

const workContextJWKSCacheTTL = 5 * time.Minute

// workContextVerifier verifies presented Work Contexts at the edge against the
// access-token JWKS published by accounts. The Work Context signing key is the
// access-token key, so the public gateway route serves both and the edge never
// needs Vault.
//
// Every failure — a forged signature, an expired or widened context, an unknown
// key id, or an accounts outage that makes the key set unreachable — collapses
// to codefly.ErrWorkContextInvalid. The edge answers 401 without leaking which
// check failed or that a dependency is down.
//
// The cache carries no stale grace: a presented capability is an optional
// attenuation a caller may always retry, so this path fails closed the moment
// the key set expires. The access-token path makes the opposite call (see
// accessJWKS), because losing it would take authentication down entirely.
type workContextVerifier struct {
	cache *jwksCache[*codefly.WorkContextVerifier]
}

func newWorkContextVerifier(accountsBaseURL string) *workContextVerifier {
	cache := newJWKSCache(
		strings.TrimSuffix(accountsBaseURL, "/")+accountsJWKSPath,
		workContextJWKSCacheTTL,
		0,
		func(keys map[string]ed25519.PublicKey) (*codefly.WorkContextVerifier, error) {
			return codefly.NewWorkContextVerifier(codefly.WorkContextVerifierOptions{PublicKeys: keys})
		},
		invalidWorkContext,
	)
	cache.observe = func(err error) { recordJWKSRefresh(jwksKeySetWorkContext, err) }
	return &workContextVerifier{cache: cache}
}

// Refresh warms the cached key set. A callee calls it at boot so verification
// fails closed from the first request rather than racing the first fetch.
func (v *workContextVerifier) Refresh(ctx context.Context) error {
	return v.cache.refresh(ctx)
}

// Verify establishes trust for a presented Work Context. It confirms the
// signature, freshness, and attenuation of the token against the published
// keys; audience and scope are the callee's concern and are not asserted here.
func (v *workContextVerifier) Verify(ctx context.Context, token codefly.WorkContextToken) error {
	keyID, err := workContextTokenKeyID(token)
	if err != nil {
		return err
	}
	verifier, err := v.cache.resolve(ctx, keyID)
	if err != nil {
		return err
	}
	if _, err := verifier.Verify(token, codefly.WorkContextExpectations{}); err != nil {
		return invalidWorkContext(err)
	}
	return nil
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
