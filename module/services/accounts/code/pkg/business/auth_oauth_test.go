package business_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	devvalidator "accounts/pkg/auth/dev"
	"accounts/pkg/auth/oidc"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// This suite exercises the OAuth authorization-code login path end to
// end: request carries typed OAuth credentials → business.Authenticate
// calls the Exchanger (fake OAuth token endpoint) → validates the
// returned id_token via the OIDC validator (fake JWKS) → runs JIT
// provisioning → mints our own JWT → returns.

type fakeProvider struct {
	t        *testing.T
	priv     *rsa.PrivateKey
	kid      string
	issuer   string
	clientID string
	secret   string
	server   *httptest.Server
	codes    map[string]bool // minted codes still valid
	nonce    string          // OIDC nonce to echo into the next id_token
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	fp := &fakeProvider{
		t:        t,
		priv:     priv,
		kid:      "test-kid",
		clientID: "client_test",
		secret:   "secret_test",
		codes:    map[string]bool{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", fp.serveJWKS)
	mux.HandleFunc("/token", fp.serveToken)
	fp.server = httptest.NewServer(mux)
	fp.issuer = fp.server.URL + "/issuer"
	t.Cleanup(fp.server.Close)
	return fp
}

func (f *fakeProvider) issueCode(code string) { f.codes[code] = true }

func (f *fakeProvider) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	n := base64.RawURLEncoding.EncodeToString(f.priv.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.priv.E)).Bytes())
	_ = json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "kid": f.kid, "alg": "RS256", "n": n, "e": e,
		}},
	})
}

func (f *fakeProvider) serveToken(w http.ResponseWriter, r *http.Request) {
	require.NoError(f.t, r.ParseForm())
	if r.PostForm.Get("client_id") != f.clientID || r.PostForm.Get("client_secret") != f.secret {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		return
	}
	code := r.PostForm.Get("code")
	if !f.codes[code] {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	delete(f.codes, code) // single-use

	// Mint an RS256 id_token matching the JWKS key.
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":             f.issuer,
		"aud":             f.clientID,
		"sub":             "workos-user-42",
		"email":           "new-user@acme.com",
		"email_verified":  true,
		"organization_id": "workos-org-7",
		"iat":             now.Unix(),
		"nbf":             now.Add(-time.Second).Unix(),
		"exp":             now.Add(15 * time.Minute).Unix(),
	}
	// Echo the nonce the relying party bound to this authorize request, as a
	// real OIDC provider does.
	if f.nonce != "" {
		claims["nonce"] = f.nonce
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = f.kid
	idToken, err := token.SignedString(f.priv)
	require.NoError(f.t, err)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": idToken, // backend falls back to access_token if id_token absent
		"id_token":     idToken,
		"token_type":   "Bearer",
		"expires_in":   900,
	})
}

// wireOAuthOnTestService attaches a validator + exchanger to the shared
// testService for the duration of the current test. Reset via cleanup.
func wireOAuthOnTestService(t *testing.T, fp *fakeProvider) *auth.OAuthStateSigner {
	t.Helper()

	validator, err := oidc.New(oidc.Config{
		ProviderName: "workos",
		Issuer:       fp.issuer,
		JWKSURL:      fp.server.URL + "/jwks",
		Audience:     fp.clientID,
	})
	require.NoError(t, err)
	testService.SetTokenValidator(validator)

	exchanger, err := oidc.NewExchanger(oidc.ExchangerConfig{
		TokenURL:     fp.server.URL + "/token",
		ClientID:     fp.clientID,
		ClientSecret: fp.secret,
	})
	require.NoError(t, err)
	testService.SetCodeExchanger(oidc.AsBusinessExchanger(exchanger))

	signer := mustOAuthStateSigner(t)
	policy, err := auth.NewOAuthRequestPolicy("workos", []string{"https://app.acme.com/auth/callback"})
	require.NoError(t, err)
	testService.SetOAuthStateSigner(signer)
	testService.SetOAuthRequestPolicy(policy)

	t.Cleanup(func() {
		testService.SetTokenValidator(nil)
		testService.SetCodeExchanger(nil)
		testService.SetOAuthStateSigner(nil)
		testService.SetOAuthRequestPolicy(nil)
	})
	return signer
}

func oauthCodeRequest(t *testing.T, signer *auth.OAuthStateSigner, fp *fakeProvider, provider, code, redirectURI string) *gen.AuthenticateRequest {
	t.Helper()
	state, err := signer.Mint(provider, redirectURI)
	require.NoError(t, err)
	// The browser would derive this from the signed state and send it as the
	// authorize `nonce`; the provider then echoes it into the id_token.
	fp.nonce = auth.OIDCNonceForState(state)
	return &gen.AuthenticateRequest{
		Provider: provider,
		Authentication: &gen.AuthenticateRequest_OauthCode{OauthCode: &gen.OAuthCodeAuthentication{
			Code:         code,
			RedirectUri:  redirectURI,
			State:        state,
			CodeVerifier: strings.Repeat("a", 43),
		}},
	}
}

func TestAuthenticate_OAuthCodeFlow_NewUser(t *testing.T) {
	clearData(t)
	fp := newFakeProvider(t)
	signer := wireOAuthOnTestService(t, fp)

	fp.issueCode("authz-code-1")

	resp, err := testService.Authenticate(testCtx, oauthCodeRequest(t, signer, fp, "workos", "authz-code-1", "https://app.acme.com/auth/callback"))
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
	require.NotEmpty(t, resp.RefreshToken)
	require.NotEmpty(t, resp.User.Uuid)

	// Verify user was JIT-provisioned with the email from the provider
	// token, not the request (which had no provider_email set).
	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   "workos",
		ProviderId: "workos-user-42",
	})
	require.NoError(t, err)
	require.True(t, resolved.Found)
	require.Equal(t, resp.User.Uuid, resolved.UserId)
}

func TestAuthenticate_OAuthCodeFlow_ReusedCode(t *testing.T) {
	clearData(t)
	fp := newFakeProvider(t)
	signer := wireOAuthOnTestService(t, fp)

	fp.issueCode("single-use-code")

	_, err := testService.Authenticate(testCtx, oauthCodeRequest(t, signer, fp, "workos", "single-use-code", "https://app.acme.com/auth/callback"))
	require.NoError(t, err)

	// Second attempt with the same code must fail at the token endpoint.
	_, err = testService.Authenticate(testCtx, oauthCodeRequest(t, signer, fp, "workos", "single-use-code", "https://app.acme.com/auth/callback"))
	require.Error(t, err)
}

func TestAuthenticate_OAuthCodeFlow_MissingRedirectURI(t *testing.T) {
	clearData(t)
	fp := newFakeProvider(t)
	signer := wireOAuthOnTestService(t, fp)

	fp.issueCode("x")

	_, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "workos",
		Authentication: &gen.AuthenticateRequest_OauthCode{OauthCode: &gen.OAuthCodeAuthentication{
			Code: "x", State: "invalid", CodeVerifier: strings.Repeat("a", 43),
		}},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, auth.ErrInvalidOAuthRequest)
	_ = signer
}

func TestAuthenticate_OAuthCodeFlow_UnconfiguredProviderRejected(t *testing.T) {
	clearData(t)
	fp := newFakeProvider(t)
	signer := wireOAuthOnTestService(t, fp)

	fp.issueCode("mismatch-code")

	_, err := testService.Authenticate(testCtx, oauthCodeRequest(t, signer, fp, "auth0", "mismatch-code", "https://app.acme.com/auth/callback"))
	require.Error(t, err)
	require.ErrorIs(t, err, auth.ErrInvalidOAuthRequest)
}

func TestAuthenticate_NoCodeRejectedWithoutDevelopmentValidator(t *testing.T) {
	// Wiring a production OAuth validator must never make request-supplied
	// provider identity fields trustworthy when the authorization code is absent.
	clearData(t)
	fp := newFakeProvider(t)
	wireOAuthOnTestService(t, fp)

	_, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider:      "google",
		ProviderId:    "google-legacy",
		ProviderEmail: "legacy@test.local",
		Authentication: &gen.AuthenticateRequest_Fixture{Fixture: &gen.FixtureAuthentication{
			Token: "google-legacy",
		}},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, auth.ErrDevelopmentAuthDisabled)
}

func TestAuthenticate_DevelopmentPathUsesAllowlistedClaims(t *testing.T) {
	clearData(t)
	testService.SetDevelopmentTokenValidator(&requestFixtureValidator{
		token: "opaque-fixture-token",
		claims: &auth.Claims{
			Provider:  "email",
			Subject:   "allowlisted-subject",
			Email:     "allowlisted@example.com",
			ExpiresAt: time.Now().Add(time.Hour),
		},
	})
	t.Cleanup(func() { testService.SetDevelopmentTokenValidator(nil) })

	resp, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "opaque-fixture-token",
		ProviderEmail: "attacker@example.com",
		EmailVerified: true,
		Authentication: &gen.AuthenticateRequest_Fixture{Fixture: &gen.FixtureAuthentication{
			Token: "opaque-fixture-token",
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "allowlisted@example.com", resp.User.PrimaryEmail)

	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   "email",
		ProviderId: "allowlisted-subject",
	})
	require.NoError(t, err)
	require.True(t, resolved.Found)

	attacker, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   "email",
		ProviderId: "opaque-fixture-token",
	})
	require.NoError(t, err)
	require.False(t, attacker.Found, "opaque fixture token must not become the provider subject")
}

func TestAuthenticate_DevelopmentPathRejectsUnknownToken(t *testing.T) {
	clearData(t)
	testService.SetDevelopmentTokenValidator(&requestFixtureValidator{
		token: "known-token",
		claims: &auth.Claims{
			Provider: "email", Subject: "known-subject", Email: "known@example.com",
		},
	})
	t.Cleanup(func() { testService.SetDevelopmentTokenValidator(nil) })

	_, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "email", ProviderId: "ignored-legacy-value",
		Authentication: &gen.AuthenticateRequest_Fixture{Fixture: &gen.FixtureAuthentication{Token: "unknown-token"}},
	})
	require.ErrorIs(t, err, auth.ErrUnknownIdentity)
}

func TestAuthenticate_DevelopmentPathRejectsProviderMismatch(t *testing.T) {
	clearData(t)
	testService.SetDevelopmentTokenValidator(&requestFixtureValidator{
		token: "known-token",
		claims: &auth.Claims{
			Provider: "email", Subject: "known-subject", Email: "known@example.com",
		},
	})
	t.Cleanup(func() { testService.SetDevelopmentTokenValidator(nil) })

	_, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "google", ProviderId: "ignored-legacy-value",
		Authentication: &gen.AuthenticateRequest_Fixture{Fixture: &gen.FixtureAuthentication{Token: "known-token"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider mismatch")
}

func TestAuthenticateRejectsOversizedDeviceInfoBeforeCredentialWork(t *testing.T) {
	_, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		DeviceInfo: strings.Repeat("x", 513),
	})
	require.ErrorIs(t, err, auth.ErrInvalidOAuthRequest)
}

func TestAuthenticate_DevelopmentFixtureEndToEnd(t *testing.T) {
	clearData(t)
	validator, err := devvalidator.New("../../../../../fixtures/dev-admin.yaml")
	require.NoError(t, err)
	testService.SetDevelopmentTokenValidator(validator)
	t.Cleanup(func() { testService.SetDevelopmentTokenValidator(nil) })

	resp, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider:      "email",
		ProviderId:    "attacker-controlled-deprecated-subject",
		ProviderEmail: "attacker@example.com",
		Authentication: &gen.AuthenticateRequest_Fixture{Fixture: &gen.FixtureAuthentication{
			Token: "dev-admin",
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "admin@acme.com", resp.User.PrimaryEmail)
	identity, err := testService.JWTMinter().VerifyAccess(resp.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AssuranceLevelAAL2, identity.AssuranceLevel)
	require.Equal(t, []string{auth.AuthenticationMethodFixture, auth.AuthenticationMethodOTP}, identity.AuthenticationMethods)
	require.False(t, identity.MFAVerifiedAt.IsZero())
	require.True(t, identity.MFASatisfied)

	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider: "email", ProviderId: "dev-admin",
	})
	require.NoError(t, err)
	require.True(t, resolved.Found)
}

func TestAuthenticate_OAuthCodeFlow_RequiresSignedState(t *testing.T) {
	clearData(t)
	fp := newFakeProvider(t)
	signer := wireOAuthOnTestService(t, fp)
	fp.issueCode("missing-state")

	req := oauthCodeRequest(t, signer, fp, "workos", "missing-state", "https://app.acme.com/auth/callback")
	req.GetOauthCode().State = ""
	_, err := testService.Authenticate(testCtx, req)
	require.ErrorIs(t, err, auth.ErrInvalidOAuthRequest)

	req = oauthCodeRequest(t, signer, fp, "workos", "missing-state", "https://app.acme.com/auth/callback")
	req.GetOauthCode().State = "not-a-signed-state"
	_, err = testService.Authenticate(testCtx, req)
	require.ErrorIs(t, err, auth.ErrInvalidOAuthState)
}

func TestBeginOAuth_RequiresPolicyAndSigner(t *testing.T) {
	policy, err := auth.NewOAuthRequestPolicy("workos", []string{"https://app.acme.com/auth/callback"})
	require.NoError(t, err)
	signer := mustOAuthStateSigner(t)
	testService.SetOAuthRequestPolicy(policy)
	testService.SetOAuthStateSigner(signer)
	t.Cleanup(func() {
		testService.SetOAuthRequestPolicy(nil)
		testService.SetOAuthStateSigner(nil)
	})

	_, err = testService.BeginOAuth(testCtx, "workos", "https://evil.example/auth/callback")
	require.ErrorIs(t, err, auth.ErrInvalidOAuthRequest)

	state, err := testService.BeginOAuth(testCtx, "workos", "https://app.acme.com/auth/callback")
	require.NoError(t, err)
	require.NoError(t, signer.Verify(context.Background(), state, "workos", "https://app.acme.com/auth/callback"))

	testService.SetOAuthStateSigner(nil)
	_, err = testService.BeginOAuth(testCtx, "workos", "https://app.acme.com/auth/callback")
	require.Error(t, err)
}

func TestBeginOAuth_UsesVerifiedCodeflyPublicOriginWithoutStaticPort(t *testing.T) {
	policy, err := auth.NewOAuthRequestPolicy("workos", nil)
	require.NoError(t, err)
	signer := mustOAuthStateSigner(t)
	testService.SetOAuthRequestPolicy(policy)
	testService.SetOAuthStateSigner(signer)
	t.Cleanup(func() {
		testService.SetOAuthRequestPolicy(nil)
		testService.SetOAuthStateSigner(nil)
	})

	ctx, err := auth.WithVerifiedPublicOrigin(testCtx, "http://localhost:54321")
	require.NoError(t, err)
	redirectURI := "http://localhost:54321/auth/callback"
	state, err := testService.BeginOAuth(ctx, "workos", redirectURI)
	require.NoError(t, err)
	require.NoError(t, signer.Verify(context.Background(), state, "workos", redirectURI))

	_, err = testService.BeginOAuth(ctx, "workos", "http://localhost:54322/auth/callback")
	require.ErrorIs(t, err, auth.ErrInvalidOAuthRequest)
}

// Sanity: prove errors.Is propagation still works across the path.
func TestAuthenticate_OAuthCodeFlow_RealError(t *testing.T) {
	clearData(t)
	fp := newFakeProvider(t)
	signer := wireOAuthOnTestService(t, fp)

	// No code issued → server returns invalid_grant
	_, err := testService.Authenticate(testCtx, oauthCodeRequest(t, signer, fp, "workos", "never-issued", "https://app.acme.com/auth/callback"))
	require.Error(t, err)
	require.False(t, errors.Is(err, context.Canceled))
}

// A generic OIDC provider selected by a non-preset IDENTITY_PROVIDER value must
// log in through the whole path: the request provider, the OAuth request
// policy, and the validated token all carry that same name. Regression guard —
// an earlier design recorded a separately configured provider name on the token
// while the request still said "oidc", so authenticateWithCode's mismatch check
// rejected every login for exactly the multi-IdP config the feature exists for.
func TestAuthenticate_OAuthCodeFlow_GenericProviderNameKeysIdentity(t *testing.T) {
	clearData(t)
	fp := newFakeProvider(t)

	const provider = "okta"
	validator, err := oidc.New(oidc.Config{
		ProviderName: provider,
		Issuer:       fp.issuer,
		JWKSURL:      fp.server.URL + "/jwks",
		Audience:     fp.clientID,
	})
	require.NoError(t, err)
	testService.SetTokenValidator(validator)

	exchanger, err := oidc.NewExchanger(oidc.ExchangerConfig{
		TokenURL:     fp.server.URL + "/token",
		ClientID:     fp.clientID,
		ClientSecret: fp.secret,
	})
	require.NoError(t, err)
	testService.SetCodeExchanger(oidc.AsBusinessExchanger(exchanger))

	signer := mustOAuthStateSigner(t)
	policy, err := auth.NewOAuthRequestPolicy(provider, []string{"https://app.acme.com/auth/callback"})
	require.NoError(t, err)
	testService.SetOAuthStateSigner(signer)
	testService.SetOAuthRequestPolicy(policy)
	t.Cleanup(func() {
		testService.SetTokenValidator(nil)
		testService.SetCodeExchanger(nil)
		testService.SetOAuthStateSigner(nil)
		testService.SetOAuthRequestPolicy(nil)
	})

	fp.issueCode("okta-code-1")
	resp, err := testService.Authenticate(testCtx, oauthCodeRequest(t, signer, fp, provider, "okta-code-1", "https://app.acme.com/auth/callback"))
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)

	// The identity is keyed under the generic provider name.
	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   provider,
		ProviderId: "workos-user-42",
	})
	require.NoError(t, err)
	require.True(t, resolved.Found)
	require.Equal(t, resp.User.Uuid, resolved.UserId)
}

func mustOAuthStateSigner(t *testing.T) *auth.OAuthStateSigner {
	t.Helper()
	signer, err := auth.NewOAuthStateSigner([]byte("test OAuth state signing seed with sufficient entropy"))
	if err != nil {
		t.Fatalf("NewOAuthStateSigner: %v", err)
	}
	return signer
}
