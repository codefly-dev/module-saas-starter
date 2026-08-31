package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	workosauth "accounts/pkg/auth/workos"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// fakeOIDCProvider is a spec-compliant OpenID Connect stub: it publishes a
// discovery document, a JWKS for a generated RSA key, and a token endpoint that
// mints a signed access token. It exercises the generic IDENTITY_PROVIDER=oidc
// stack end to end without depending on any real IdP.
type fakeOIDCProvider struct {
	priv          *rsa.PrivateKey
	kid           string
	server        *httptest.Server
	issuer        string
	aud           string
	discoveryHits int
	tokenHits     int
}

func newFakeOIDCProvider(t *testing.T) *fakeOIDCProvider {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	f := &fakeOIDCProvider{priv: priv, kid: "kid-1", aud: "api://generic"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		f.discoveryHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 f.issuer,
			"authorization_endpoint": f.issuer + "/authorize",
			"token_endpoint":         f.issuer + "/token",
			"jwks_uri":               f.issuer + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(f.priv.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.priv.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA", "use": "sig", "kid": f.kid, "alg": "RS256", "n": n, "e": e,
			}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		f.tokenHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": f.sign(t),
			"token_type":   "Bearer",
		})
	})
	f.server = httptest.NewServer(mux)
	f.issuer = f.server.URL
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOIDCProvider) sign(t *testing.T) string {
	t.Helper()
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":            f.issuer,
		"aud":            f.aud,
		"sub":            "user_enterprise_01",
		"email":          "user@enterprise.example",
		"email_verified": true,
		"iat":            now.Unix(),
		"exp":            now.Add(15 * time.Minute).Unix(),
	})
	token.Header["kid"] = f.kid
	signed, err := token.SignedString(f.priv)
	require.NoError(t, err)
	return signed
}

func configureGenericOIDC(t *testing.T, issuer string) {
	t.Helper()
	setIdentityConfiguration(t, "IDENTITY_CLIENT_ID", "client_generic")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET", "sk_generic")
	setIdentityConfiguration(t, "IDENTITY_ISSUER", issuer)
	setIdentityConfiguration(t, "IDENTITY_AUDIENCE", "api://generic")
}

func TestGenericOIDCStackDrivesAWorkingLogin(t *testing.T) {
	clearAuthProviderEnvironment(t)
	f := newFakeOIDCProvider(t)
	configureGenericOIDC(t, f.issuer)

	validator, exchanger, err := buildProviderStack("oidc", "")
	require.NoError(t, err)
	require.NotNil(t, validator)
	require.NotNil(t, exchanger)

	// The generic path must use the standard OAuth 2.0 code-grant exchanger,
	// never the WorkOS authenticate adapter that reads a WorkOS-only response.
	_, isWorkOS := exchanger.(*workosauth.Exchanger)
	require.False(t, isWorkOS, "generic oidc must not inherit the WorkOS exchanger")

	tokens, err := exchanger.Exchange(t.Context(), "auth-code", "http://localhost:21931/auth/callback", "verifier")
	require.NoError(t, err)
	require.NotEmpty(t, tokens.AccessToken)
	require.Equal(t, 1, f.tokenHits, "exchange must hit the discovered token endpoint")

	claims, err := validator.Validate(t.Context(), tokens.AccessToken)
	require.NoError(t, err)
	require.Equal(t, "oidc", claims.Provider)
	require.Equal(t, "user_enterprise_01", claims.Subject)
	require.Equal(t, "user@enterprise.example", claims.Email)
	require.True(t, claims.EmailVerified)
}

func TestGenericOIDCStackHonorsDiscoveryOverrides(t *testing.T) {
	clearAuthProviderEnvironment(t)
	f := newFakeOIDCProvider(t)
	configureGenericOIDC(t, f.issuer)
	// Pinning both endpoints must skip the well-known document entirely, the
	// escape hatch for IdPs whose discovery is incomplete or unreachable.
	setIdentityConfiguration(t, "IDENTITY_JWKS_URL", f.issuer+"/jwks")
	setIdentityConfiguration(t, "IDENTITY_TOKEN_URL", f.issuer+"/token")

	validator, exchanger, err := buildProviderStack("oidc", "")
	require.NoError(t, err)

	tokens, err := exchanger.Exchange(t.Context(), "auth-code", "http://localhost:21931/auth/callback", "verifier")
	require.NoError(t, err)
	claims, err := validator.Validate(t.Context(), tokens.AccessToken)
	require.NoError(t, err)
	require.Equal(t, "user@enterprise.example", claims.Email)
	require.Equal(t, 0, f.discoveryHits, "pinned endpoints must not trigger discovery")
	require.Equal(t, 1, f.tokenHits, "exchange must hit the pinned token endpoint")
}

func TestGenericOIDCStackRecordsSelectorAsProviderName(t *testing.T) {
	clearAuthProviderEnvironment(t)
	f := newFakeOIDCProvider(t)
	configureGenericOIDC(t, f.issuer)
	accessToken := f.sign(t)

	// claims.Provider must equal the IDENTITY_PROVIDER selector verbatim: the
	// browser, the OAuth request policy, and the mismatch guard all key on that
	// same string, so a divergent name would fail every login (regression: an
	// earlier design recorded a separately configured name and broke the guard).
	validator, _, err := buildProviderStack("oidc", "")
	require.NoError(t, err)
	claims, err := validator.Validate(t.Context(), accessToken)
	require.NoError(t, err)
	require.Equal(t, "oidc", claims.Provider)

	// Two generic IdPs selected by distinct IDENTITY_PROVIDER values key
	// distinct user_identities (provider, provider_id) rows for the same subject.
	// A non-preset name is generic only with the explicit opt-in.
	setIdentityConfiguration(t, "IDENTITY_GENERIC_OIDC", "true")
	oktaValidator, _, err := buildProviderStack("okta", "")
	require.NoError(t, err)
	oktaClaims, err := oktaValidator.Validate(t.Context(), accessToken)
	require.NoError(t, err)
	require.Equal(t, "okta", oktaClaims.Provider)
	require.Equal(t, claims.Subject, oktaClaims.Subject)
	require.NotEqual(t, claims.Provider, oktaClaims.Provider)
}

func TestGenericOIDCStackRequiresExplicitOptInForNonPresetNames(t *testing.T) {
	clearAuthProviderEnvironment(t)
	f := newFakeOIDCProvider(t)
	configureGenericOIDC(t, f.issuer)

	// A non-preset name (here a typo of the "workos" preset) with otherwise
	// valid discovery config must fail startup closed, not silently build a
	// generic stack that mismatches WorkOS's token shape and fails at first
	// login. The generic path is reachable only by explicit declaration.
	_, _, err := buildProviderStack("wrokos", "")
	require.ErrorContains(t, err, "unsupported identity provider")
	require.ErrorContains(t, err, "IDENTITY_GENERIC_OIDC")

	setIdentityConfiguration(t, "IDENTITY_GENERIC_OIDC", "true")
	validator, exchanger, err := buildProviderStack("wrokos", "")
	require.NoError(t, err)
	require.NotNil(t, validator)
	require.NotNil(t, exchanger)
}

func TestGenericOIDCStackFailsAtStartupOnMisconfiguration(t *testing.T) {
	clearAuthProviderEnvironment(t)

	_, _, err := buildProviderStack("oidc", "")
	require.ErrorContains(t, err, "IDENTITY_CLIENT_ID")

	setIdentityConfiguration(t, "IDENTITY_CLIENT_ID", "client_generic")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET", "sk_generic")
	_, _, err = buildProviderStack("oidc", "")
	require.ErrorContains(t, err, "IDENTITY_ISSUER")
}

func TestGenericOIDCStackDefaultsAudienceToClientID(t *testing.T) {
	clearAuthProviderEnvironment(t)
	f := newFakeOIDCProvider(t)
	// Standard OIDC: the id_token's aud is the client id.
	f.aud = "client_generic"

	// Minimal configuration: client id, secret and issuer only. Neither
	// IDENTITY_AUDIENCE nor IDENTITY_CLIENT_ID_CLAIM is set, so the validator
	// must default its audience binding to the client id rather than fail closed
	// at startup ("an audience binding is required").
	setIdentityConfiguration(t, "IDENTITY_CLIENT_ID", "client_generic")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET", "sk_generic")
	setIdentityConfiguration(t, "IDENTITY_ISSUER", f.issuer)

	validator, exchanger, err := buildProviderStack("oidc", "")
	require.NoError(t, err)
	require.NotNil(t, validator)

	tokens, err := exchanger.Exchange(t.Context(), "auth-code", "http://localhost:21931/auth/callback", "verifier")
	require.NoError(t, err)
	claims, err := validator.Validate(t.Context(), tokens.AccessToken)
	require.NoError(t, err)
	require.Equal(t, "user@enterprise.example", claims.Email)

	// The default is a real binding, not an "accept any audience" fallback: a
	// token whose aud is not the client id is still rejected.
	f.aud = "someone-else"
	_, err = validator.Validate(t.Context(), f.sign(t))
	require.ErrorContains(t, err, "audience mismatch")
}

func TestWorkOSStackDoesNotDefaultAudienceToClientID(t *testing.T) {
	clearAuthProviderEnvironment(t)
	f := newFakeOIDCProvider(t)

	// Same minimal configuration as the generic default test, but for the workos
	// provider. WorkOS validates its own access token, bound to the application
	// via a non-standard client_id claim rather than aud == client id, so the
	// audience default must NOT apply: startup must still fail closed (preserving
	// the required-binding hardening) rather than boot and reject every login.
	setIdentityConfiguration(t, "IDENTITY_CLIENT_ID", "client_workos")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET", "sk_workos")
	setIdentityConfiguration(t, "IDENTITY_ISSUER", f.issuer)

	_, _, err := buildProviderStack("workos", "")
	require.ErrorContains(t, err, "audience binding is required")
}
