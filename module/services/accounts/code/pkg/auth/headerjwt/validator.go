// Package headerjwt implements auth.TokenValidator for gateway-pre-authenticated
// deployments: a customer-operated access gateway (PingAccess, an nginx/Apache
// auth module, an API gateway) authenticates the user upstream and injects a
// signed JWT in a request header. The app never runs an OAuth ceremony — the
// login route hands this validator the header's value and receives Claims.
//
// The token string is opaque to this package: the login route copies the
// configured header verbatim. Everything trust-bearing happens here.
//
// # Fail-closed posture
//
// The default mode verifies the JWT against a configured JWKS URL (signature,
// exp, aud, and optionally iss). If the JWKS cannot be fetched or parsed the
// result is denial (ErrJWKSUnavailable), never an unverified decode.
//
// A deployment where the gateway is the sole ingress and strips any
// client-supplied copy of the header may opt into PerimeterTrustDecode: the
// signature is not checked (the gateway is the trust anchor), but exp and aud
// are still enforced. It is off by default and must be named explicitly — the
// whole point of the flag is that turning it on is a deliberate, auditable
// perimeter-trust decision, not an accidental downgrade.
package headerjwt

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

// Config controls the validator. ProviderName and Audience are always
// required; JWKSURL is required unless PerimeterTrustDecode is set.
type Config struct {
	// ProviderName is written to Claims.Provider and used as the
	// user_identities.provider key. Required.
	ProviderName string
	// JWKSURL is where to GET the JSON Web Key Set used to verify the
	// signature. Required unless PerimeterTrustDecode is true.
	JWKSURL string
	// Audience is the expected `aud`. Required and always enforced, including
	// under PerimeterTrustDecode.
	Audience string
	// Issuer is the expected `iss`. Optional; empty disables the check.
	Issuer string

	// SubjectClaim carries the stable provider user id. Defaults to "sub".
	SubjectClaim string
	// EmailClaim carries the primary email. Defaults to "email".
	EmailClaim string
	// EmailVerifiedClaim carries the provider's email-verified assertion.
	// Defaults to "email_verified"; an absent or non-affirmative claim is
	// treated as unverified.
	EmailVerifiedClaim string
	// NameClaims are joined (space-separated, in order, skipping absent parts)
	// into Claims.DisplayName. A single entry is the common case; two entries
	// support providers that split a name across e.g. given name + surname.
	// Empty leaves the display name blank.
	NameClaims []string
	// GroupClaim carries the caller's group membership, either a single string
	// or an array of strings. Empty disables group extraction (and therefore
	// the AllowedGroups gate).
	GroupClaim string
	// AllowedGroups, when non-empty, gates login: the group claim must overlap
	// this set or the login is denied with ErrGroupNotAllowed. Empty skips the
	// gate entirely.
	AllowedGroups []string

	// PerimeterTrustDecode disables signature verification while still
	// enforcing exp and aud. Off by default. Only safe when the gateway is the
	// sole ingress and strips client-supplied copies of the header.
	PerimeterTrustDecode bool

	// AllowedAlgs restricts acceptable signing algs for the verified path.
	// Defaults to RS256 only.
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
	if c.SubjectClaim == "" {
		c.SubjectClaim = "sub"
	}
	if c.EmailClaim == "" {
		c.EmailClaim = "email"
	}
	if c.EmailVerifiedClaim == "" {
		c.EmailVerifiedClaim = "email_verified"
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

// Validator is a header-JWT TokenValidator. Safe for concurrent use.
type Validator struct {
	cfg Config

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey // keyed by kid
	fetchedAt time.Time
}

// New constructs a Validator from Config. The JWKS is not fetched eagerly —
// first use triggers a lazy fetch so construction cannot fail on network.
func New(cfg Config) (*Validator, error) {
	cfg.withDefaults()
	if cfg.ProviderName == "" {
		return nil, errors.New("headerjwt: ProviderName is required")
	}
	if cfg.Audience == "" {
		return nil, errors.New("headerjwt: Audience is required")
	}
	if !cfg.PerimeterTrustDecode && cfg.JWKSURL == "" {
		return nil, errors.New("headerjwt: JWKSURL is required unless PerimeterTrustDecode is set")
	}
	// A group allow-list with no claim to read would deny every login silently
	// at request time; fail loudly at construction instead.
	if len(cfg.AllowedGroups) > 0 && cfg.GroupClaim == "" {
		return nil, errors.New("headerjwt: GroupClaim is required when AllowedGroups is set")
	}
	return &Validator{cfg: cfg, keys: map[string]*rsa.PublicKey{}}, nil
}

// Validate implements auth.TokenValidator.
func (v *Validator) Validate(ctx context.Context, token string) (*auth.Claims, error) {
	claims := jwt.MapClaims{}

	if v.cfg.PerimeterTrustDecode {
		parser := jwt.NewParser(jwt.WithValidMethods(v.cfg.AllowedAlgs))
		if _, _, err := parser.ParseUnverified(token, claims); err != nil {
			return nil, mapParseError(err)
		}
		// The signature is intentionally not checked here, but exp and aud
		// still bind — a stale or misaddressed token is rejected even when the
		// gateway is the trust anchor.
		if err := v.claimValidator().Validate(claims); err != nil {
			return nil, mapParseError(err)
		}
	} else {
		opts := []jwt.ParserOption{
			jwt.WithValidMethods(v.cfg.AllowedAlgs),
			jwt.WithExpirationRequired(),
			jwt.WithAudience(v.cfg.Audience),
			jwt.WithLeeway(v.cfg.ClockSkew),
		}
		if v.cfg.Issuer != "" {
			opts = append(opts, jwt.WithIssuer(v.cfg.Issuer))
		}
		parser := jwt.NewParser(opts...)
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
	}

	subject, _ := claims[v.cfg.SubjectClaim].(string)
	if subject == "" {
		return nil, auth.ErrMissingSubject
	}
	email, _ := claims[v.cfg.EmailClaim].(string)
	if email == "" {
		return nil, auth.ErrMissingEmail
	}

	if err := v.enforceGroups(claims); err != nil {
		return nil, err
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
		DisplayName:   v.displayName(claims),
		ExpiresAt:     exp,
	}, nil
}

// claimValidator validates exp/aud (and iss when configured) without touching
// the signature. Used only by the perimeter-trust decode path.
func (v *Validator) claimValidator() *jwt.Validator {
	opts := []jwt.ParserOption{
		jwt.WithExpirationRequired(),
		jwt.WithAudience(v.cfg.Audience),
		jwt.WithLeeway(v.cfg.ClockSkew),
	}
	if v.cfg.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(v.cfg.Issuer))
	}
	return jwt.NewValidator(opts...)
}

// displayName joins the configured name claims (in order, skipping absent or
// blank parts) into a single presentational string.
func (v *Validator) displayName(claims jwt.MapClaims) string {
	parts := make([]string, 0, len(v.cfg.NameClaims))
	for _, name := range v.cfg.NameClaims {
		if s, ok := claims[name].(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, " ")
}

// enforceGroups applies the optional AllowedGroups gate. A configured gate with
// no overlap — including the case where the token carries no group claim at all
// — is denied with ErrGroupNotAllowed. No gate configured skips the check.
func (v *Validator) enforceGroups(claims jwt.MapClaims) error {
	if len(v.cfg.AllowedGroups) == 0 {
		return nil
	}
	have := extractGroups(claims[v.cfg.GroupClaim])
	allowed := make(map[string]struct{}, len(v.cfg.AllowedGroups))
	for _, g := range v.cfg.AllowedGroups {
		allowed[g] = struct{}{}
	}
	for _, g := range have {
		if _, ok := allowed[g]; ok {
			return nil
		}
	}
	return auth.ErrGroupNotAllowed
}

// extractGroups normalizes a group claim that a provider may encode either as a
// single string or as an array of strings.
func extractGroups(claim any) []string {
	switch t := claim.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// claimBool interprets a claim as a boolean. Providers publish email_verified
// as either a JSON boolean or a "true"/"false" string; any other shape —
// including an absent claim — is treated as false.
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

// resolveKey returns the cached RSA key for a kid, fetching the JWKS if the
// cache is cold or stale.
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

// refresh fetches and parses the JWKS. Any failure is reported as
// ErrJWKSUnavailable so the caller denies rather than falling back to an
// unverified decode.
func (v *Validator) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return fmt.Errorf("%w: build request: %v", auth.ErrJWKSUnavailable, err)
	}
	resp, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: fetch: %v", auth.ErrJWKSUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: http %d", auth.ErrJWKSUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%w: read: %v", auth.ErrJWKSUnavailable, err)
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
		return fmt.Errorf("%w: parse: %v", auth.ErrJWKSUnavailable, err)
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
		return fmt.Errorf("%w: no usable RSA keys", auth.ErrJWKSUnavailable)
	}

	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}

// mapParseError translates jwt.v5 errors into our sentinel errors, preserving
// the fail-closed JWKS signal raised inside the keyfunc.
func mapParseError(err error) error {
	switch {
	case errors.Is(err, auth.ErrJWKSUnavailable):
		return auth.ErrJWKSUnavailable
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
