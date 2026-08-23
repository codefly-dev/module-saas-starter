package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// OAuthStateSigner mints and verifies the `state` parameter sent through
// the OAuth authorization-code flow. Fixes the CSRF gap where the FE
// alone validates the state (sessionStorage check): an attacker who
// breaches the FE's session-state isolation can still forge a callback
// matching the cached value. Server-side validation closes this with a
// short-lived HMAC-signed token bound to `provider + redirect_uri`.
//
// Token format: `<base64url(payload)>.<base64url(hmac)>` — entirely
// self-validating, no Redis or DB needed for the happy path. The nonce
// inside the payload exists to make replay detectable if the operator
// later wants to add a Redis-backed one-shot list (the signer doesn't
// need it for correctness; the `exp` enforces a 10-min window).
//
// HMAC over Ed25519 is intentional: state is verified by the same
// backend that minted it, so symmetric is simpler and avoids the
// JWKS-distribution problem. The HMAC key is derived from the existing
// Ed25519 signing key with a context label, so we don't introduce a
// new secret that has to be rotated separately.
type OAuthStateSigner struct {
	key []byte
	ttl time.Duration
}

// NewOAuthStateSigner derives an HMAC key from the given seed and
// returns a signer with a 10-minute TTL. seed should be a private
// secret (e.g. the Ed25519 private key bytes); SHA-256 with a context
// label keeps the derivation domain-separated from token signing.
//
// An empty seed is a hard error rather than a silent random-key fallback:
// a per-instance random key makes state minted by one replica fail to
// verify on another (breaking every OAuth callback behind a load
// balancer) and hides the underlying misconfiguration. Callers own the
// dev-vs-prod distinction and must supply a real seed.
func NewOAuthStateSigner(seed []byte) (*OAuthStateSigner, error) {
	if len(seed) == 0 {
		return nil, errors.New("oauth-state: signing seed is required")
	}
	h := sha256.New()
	h.Write([]byte("saas-starter:oauth-state:v1"))
	h.Write(seed)
	return &OAuthStateSigner{key: h.Sum(nil), ttl: 10 * time.Minute}, nil
}

// stateClaims is the payload shape. Field names short for compactness
// — these never appear in any external API.
type stateClaims struct {
	Provider    string `json:"p"`
	RedirectURI string `json:"r"`
	Nonce       string `json:"n"`
	ExpiresAt   int64  `json:"e"` // unix seconds
}

// Mint returns a state token bound to (provider, redirectURI). Each
// call produces a fresh random nonce so two concurrent sign-in attempts
// from the same browser are independently verifiable.
func (s *OAuthStateSigner) Mint(provider, redirectURI string) (string, error) {
	if provider == "" || redirectURI == "" {
		return "", errors.New("oauth-state: provider and redirect_uri required")
	}
	nb := make([]byte, 16)
	if _, err := rand.Read(nb); err != nil {
		return "", err
	}
	payload := stateClaims{
		Provider:    provider,
		RedirectURI: redirectURI,
		Nonce:       base64.RawURLEncoding.EncodeToString(nb),
		ExpiresAt:   time.Now().Add(s.ttl).Unix(),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(encoded)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig, nil
}

// Verify returns nil iff the state token's signature is valid, it has
// not expired, and the bound provider + redirect_uri match the values
// the caller is about to use. A mismatch on any of those triggers
// ErrInvalidOAuthState — never differentiate the cause to the client,
// to avoid an oracle for forgers.
func (s *OAuthStateSigner) Verify(state, provider, redirectURI string) error {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return ErrInvalidOAuthState
	}
	body, sig := parts[0], parts[1]

	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(body))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return ErrInvalidOAuthState
	}

	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return ErrInvalidOAuthState
	}
	var claims stateClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return ErrInvalidOAuthState
	}
	if time.Unix(claims.ExpiresAt, 0).Before(time.Now()) {
		return ErrInvalidOAuthState
	}
	if claims.Provider != provider || claims.RedirectURI != redirectURI {
		return ErrInvalidOAuthState
	}
	return nil
}

// ErrInvalidOAuthState is the single error returned for any state
// validation failure (signature, expiry, provider mismatch, redirect
// mismatch, malformed). Callers MUST NOT branch on the cause — that
// would give forgers an oracle.
var ErrInvalidOAuthState = errors.New("auth: invalid oauth state")
