package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

// NonceConsumer records single-use OAuth-state nonces so a captured state can't
// be replayed inside its TTL. Consume marks nonce used and reports whether this
// was its FIRST use (true = fresh, false = replay). A non-nil error means the
// backing store was unreachable; Verify treats that as fail-open (the IdP's
// own single-use authorization code is the authoritative anti-replay, so a
// store blip must not break every login) but logs it.
type NonceConsumer interface {
	Consume(ctx context.Context, nonce string, ttl time.Duration) (firstUse bool, err error)
}

// inMemoryNonceConsumer is the default single-process consumer: enough to make
// one-shot hold without Redis (dev / single replica). Redis-backed callers
// override it via SetNonceConsumer for correctness across replicas.
type inMemoryNonceConsumer struct {
	mu   sync.Mutex
	seen map[string]time.Time // nonce -> expiry
}

func newInMemoryNonceConsumer() *inMemoryNonceConsumer {
	return &inMemoryNonceConsumer{seen: make(map[string]time.Time)}
}

func (c *inMemoryNonceConsumer) Consume(_ context.Context, nonce string, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	// Opportunistically drop expired entries so the map stays bounded by the
	// live-state window, not lifetime login volume.
	for key, expiresAt := range c.seen {
		if now.After(expiresAt) {
			delete(c.seen, key)
		}
	}
	if expiresAt, ok := c.seen[nonce]; ok && now.Before(expiresAt) {
		return false, nil
	}
	c.seen[nonce] = now.Add(ttl)
	return true, nil
}

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
	key      []byte
	ttl      time.Duration
	consumer NonceConsumer
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
	return &OAuthStateSigner{
		key:      h.Sum(nil),
		ttl:      10 * time.Minute,
		consumer: newInMemoryNonceConsumer(),
	}, nil
}

// SetNonceConsumer swaps the default single-process consumer for a shared one
// (e.g. Redis-backed) so state one-shot holds across replicas. A nil consumer
// is ignored, keeping the fail-safe in-memory default.
func (s *OAuthStateSigner) SetNonceConsumer(consumer NonceConsumer) {
	if consumer != nil {
		s.consumer = consumer
	}
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
func (s *OAuthStateSigner) Verify(ctx context.Context, state, provider, redirectURI string) error {
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
	// Single-use: the nonce is consumed the first time a valid state is verified,
	// so replaying the same state inside its TTL is rejected. Compute the
	// remaining lifetime so the consumed marker expires with the state itself.
	remaining := time.Until(time.Unix(claims.ExpiresAt, 0))
	if remaining <= 0 {
		return ErrInvalidOAuthState
	}
	firstUse, err := s.consumer.Consume(ctx, claims.Nonce, remaining)
	if err != nil {
		// Fail open on a store outage: the IdP's own single-use authorization
		// code is the authoritative anti-replay, so a Redis blip must not break
		// every OAuth login. Observable so an outage isn't silent.
		log.Printf("oauth-state: nonce consume failed, admitting (fail-open): %v", err)
		return nil
	}
	if !firstUse {
		return ErrInvalidOAuthState
	}
	return nil
}

// ErrInvalidOAuthState is the single error returned for any state
// validation failure (signature, expiry, provider mismatch, redirect
// mismatch, malformed). Callers MUST NOT branch on the cause — that
// would give forgers an oracle.
var ErrInvalidOAuthState = errors.New("auth: invalid oauth state")

// OIDCNonceForState derives the OpenID Connect `nonce` bound to a given signed
// state. The frontend sends this value as the authorize `nonce` parameter and
// the provider echoes it into the id_token; the callback recomputes it from the
// same (verified, single-use) state and asserts equality, binding the id_token
// to this exact authorize request as replay/injection defense. Deriving it from
// the state means no extra value has to be stored or returned to the browser.
// The frontend mirrors this derivation (base64url(sha256(state))).
func OIDCNonceForState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
