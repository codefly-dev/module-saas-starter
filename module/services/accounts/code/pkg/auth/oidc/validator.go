// Package oidc implements a generic JWKS-backed TokenValidator that fits
// any OpenID-Connect-style identity provider: WorkOS, Auth0, Clerk, Okta,
// Google, Azure AD, etc. Each provider is a thin preset over this package
// (see presets.go).
//
// The validator runs at login/signup time only — the hot path uses our own
// Ed25519 minted JWT verified by the sidecar, not provider tokens.
//
// What makes this generic:
//
//   - RS256 signature verification via JWKS (all OIDC providers publish one).
//   - Standard `iss`, `aud`, `exp`, `nbf` validation.
//   - Configurable claim names for email and org id (providers differ).
//   - Provider name is a free-form string stored on the returned Claims so
//     the IdentityResolver can key provider_identities by (provider, sub).
//
// What stays provider-specific (handled by presets, not this file):
//
//   - Default JWKS URL and issuer URL format.
//   - Nonstandard claim names.
//   - OAuth code → access token exchange (callback handler, not this file).
package oidc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"accounts/pkg/auth"
)

// Config controls the validator. ProviderName, Issuer and JWKSURL are
// required, as is an audience binding (either Audience, or ClientIDClaim
// together with ClientID); everything else has sensible defaults.
type Config struct {
	// ProviderName is written to Claims.Provider and used as the
	// provider_identities.provider key. Required. Examples: "workos",
	// "auth0", "clerk", "google".
	ProviderName string
	// Issuer is the expected `iss` claim. Required.
	Issuer string
	// JWKSURL is where to GET the JSON Web Key Set. Required.
	JWKSURL string
	// Audience is enforced via the `aud` claim. It is one of the two ways to
	// bind tokens to this relying party; the other is ClientIDClaim+ClientID.
	// At least one binding is required (see New).
	Audience string
	// EmailClaim is the claim name carrying the user's email.
	// Defaults to "email".
	EmailClaim string
	// EmailVerifiedClaim is the claim name carrying the provider's assertion
	// that the user controls the email. Defaults to "email_verified". An
	// absent or non-affirmative claim is treated as unverified.
	EmailVerifiedClaim string
	// AllowMissingEmail permits a provider adapter to validate the signed
	// token first and then supply a verified email from the authenticated token
	// exchange response. Generic OIDC flows should leave this false.
	AllowMissingEmail bool
	// OrgClaim is the claim name carrying the provider's organization id.
	// Defaults to "organization_id". Empty string disables org extraction.
	OrgClaim string
	// ClientIDClaim and ClientID validate providers such as WorkOS that bind an
	// access token to an application with a non-standard `client_id` claim
	// instead of the OAuth `aud` claim.
	ClientIDClaim string
	ClientID      string
	// AllowedAlgs restricts acceptable signing algs. Defaults to RS256 only.
	AllowedAlgs []string
	// HTTPClient used to fetch the JWKS. Defaults to http.DefaultClient.
	HTTPClient *http.Client
	// CacheTTL is how long a fetched JWKS is trusted before refresh.
	// Defaults to 10 minutes.
	CacheTTL time.Duration
	// ClockSkew tolerance on exp/nbf. Defaults to 60 seconds.
	ClockSkew time.Duration
}

func (c *Config) withDefaults() {
	if c.EmailClaim == "" {
		c.EmailClaim = "email"
	}
	if c.EmailVerifiedClaim == "" {
		c.EmailVerifiedClaim = "email_verified"
	}
	if c.OrgClaim == "" {
		c.OrgClaim = "organization_id"
	}
	if len(c.AllowedAlgs) == 0 {
		c.AllowedAlgs = []string{"RS256"}
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	if c.CacheTTL == 0 {
		c.CacheTTL = 10 * time.Minute
	}
	if c.ClockSkew == 0 {
		c.ClockSkew = 60 * time.Second
	}
}

// Validator is a JWKS-backed TokenValidator. Safe for concurrent use.
type Validator struct {
	cfg Config

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey // keyed by kid
	fetchedAt time.Time
}

// New constructs a Validator from Config. The JWKS is NOT fetched eagerly —
// first use triggers a lazy fetch so construction cannot fail on network.
func New(cfg Config) (*Validator, error) {
	cfg.withDefaults()
	if cfg.ProviderName == "" {
		return nil, errors.New("oidc: ProviderName is required")
	}
	if cfg.Issuer == "" {
		return nil, errors.New("oidc: Issuer is required")
	}
	if cfg.JWKSURL == "" {
		return nil, errors.New("oidc: JWKSURL is required")
	}
	// Fail closed if no audience binding is configured: without one, the
	// validator accepts any correctly-signed, unexpired token from the issuer
	// regardless of relying party (audience confusion on a shared IdP tenant).
	if cfg.Audience == "" && (cfg.ClientIDClaim == "" || cfg.ClientID == "") {
		return nil, errors.New("oidc: an audience binding is required: set Audience, or ClientIDClaim and ClientID")
	}
	return &Validator{cfg: cfg, keys: map[string]*rsa.PublicKey{}}, nil
}

// Validate implements auth.TokenValidator. It performs no nonce check; the
// OAuth code flow uses ValidateWithNonce to bind the id_token to its authorize
// request.
func (v *Validator) Validate(ctx context.Context, token string) (*auth.Claims, error) {
	return v.ValidateWithNonce(ctx, token, "")
}

// ValidateWithNonce verifies the token as Validate does and additionally asserts
// the OpenID Connect `nonce` claim equals expectedNonce. An empty expectedNonce
// skips the check (no nonce was requested). When one is expected, a token whose
// nonce claim is absent or unequal is rejected with auth.ErrTokenWrongNonce —
// this is the id_token replay/injection defense for the authorization-code flow.
func (v *Validator) ValidateWithNonce(ctx context.Context, token, expectedNonce string) (*auth.Claims, error) {
	opts := []jwt.ParserOption{
		jwt.WithValidMethods(v.cfg.AllowedAlgs),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(v.cfg.ClockSkew),
	}
	if v.cfg.Audience != "" {
		opts = append(opts, jwt.WithAudience(v.cfg.Audience))
	}
	parser := jwt.NewParser(opts...)

	claims := jwt.MapClaims{}
	parsed, err := parser.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if !algAllowed(v.cfg.AllowedAlgs, t.Method.Alg()) {
			return nil, auth.ErrTokenAlgForbidden
		}
		kid, _ := t.Header["kid"].(string)
		return v.resolveKey(ctx, kid)
	})
	if err != nil {
		return nil, mapParseError(err)
	}
	if !parsed.Valid {
		return nil, auth.ErrTokenMalformed
	}

	if expectedNonce != "" {
		nonce, _ := claims["nonce"].(string)
		if nonce != expectedNonce {
			return nil, auth.ErrTokenWrongNonce
		}
	}

	subject, _ := claims["sub"].(string)
	if subject == "" {
		return nil, auth.ErrMissingSubject
	}
	email, _ := claims[v.cfg.EmailClaim].(string)
	if email == "" && !v.cfg.AllowMissingEmail {
		return nil, auth.ErrMissingEmail
	}
	if v.cfg.ClientIDClaim != "" {
		clientID, _ := claims[v.cfg.ClientIDClaim].(string)
		if clientID == "" || clientID != v.cfg.ClientID {
			return nil, auth.ErrTokenWrongAudience
		}
	}
	var providerOrg string
	if v.cfg.OrgClaim != "" {
		providerOrg, _ = claims[v.cfg.OrgClaim].(string)
	}

	var exp time.Time
	if expFloat, ok := claims["exp"].(float64); ok {
		exp = time.Unix(int64(expFloat), 0)
	}

	return &auth.Claims{
		Provider:      v.cfg.ProviderName,
		Subject:       subject,
		Email:         email,
		EmailVerified: claimBool(claims[v.cfg.EmailVerifiedClaim]),
		ProviderOrgID: providerOrg,
		ExpiresAt:     exp,
	}, nil
}

// claimBool interprets an OIDC claim as a boolean. Providers publish
// email_verified as either a JSON boolean or a "true"/"false" string; any
// other shape — including an absent claim — is treated as false.
func claimBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	default:
		return false
	}
}

func algAllowed(allowed []string, have string) bool {
	for _, a := range allowed {
		if a == have {
			return true
		}
	}
	return false
}

// resolveKey returns the cached RSA key for a kid, fetching the JWKS if
// the cache is cold or stale.
func (v *Validator) resolveKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if kid == "" {
		return nil, fmt.Errorf("%w: missing kid", auth.ErrTokenMalformed)
	}

	v.mu.RLock()
	key, ok := v.keys[kid]
	fresh := time.Since(v.fetchedAt) < v.cfg.CacheTTL
	v.mu.RUnlock()
	if ok && fresh {
		return key, nil
	}

	if err := v.refresh(ctx); err != nil {
		return nil, err
	}

	v.mu.RLock()
	key = v.keys[kid]
	v.mu.RUnlock()
	if key == nil {
		return nil, fmt.Errorf("%w: unknown kid %q", auth.ErrTokenSignature, kid)
	}
	return key, nil
}

// refresh fetches and parses the JWKS.
func (v *Validator) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return fmt.Errorf("oidc: build jwks request: %w", err)
	}
	resp, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("oidc: fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc: jwks http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("oidc: read jwks: %w", err)
	}

	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &jwks); err != nil {
		return fmt.Errorf("oidc: parse jwks: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(n),
			E: int(new(big.Int).SetBytes(e).Int64()),
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("oidc: no usable RSA keys in jwks")
	}

	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}

// mapParseError translates jwt.v5 errors into our sentinel errors.
func mapParseError(err error) error {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return auth.ErrTokenExpired
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		return auth.ErrTokenWrongIssuer
	case errors.Is(err, jwt.ErrTokenInvalidAudience):
		return auth.ErrTokenWrongAudience
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return auth.ErrTokenSignature
	case errors.Is(err, auth.ErrTokenAlgForbidden):
		return auth.ErrTokenAlgForbidden
	default:
		return fmt.Errorf("%w: %v", auth.ErrTokenMalformed, err)
	}
}

// Compile-time assertion.
var _ auth.TokenValidator = (*Validator)(nil)
