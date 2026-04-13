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
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"api/pkg/auth/oidc"
	"api/pkg/gen"
)

// This suite exercises the OAuth authorization-code login path end to
// end: request carries a `code` in profile map → business.Authenticate
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
		"sub":             "workos-user-42",
		"email":           "new-user@acme.com",
		"email_verified":  true,
		"organization_id": "workos-org-7",
		"iat":             now.Unix(),
		"nbf":             now.Add(-time.Second).Unix(),
		"exp":             now.Add(15 * time.Minute).Unix(),
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
func wireOAuthOnTestService(t *testing.T, fp *fakeProvider) {
	t.Helper()

	validator, err := oidc.New(oidc.Config{
		ProviderName: "workos",
		Issuer:       fp.issuer,
		JWKSURL:      fp.server.URL + "/jwks",
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

	t.Cleanup(func() {
		testService.SetTokenValidator(nil)
		testService.SetCodeExchanger(nil)
	})
}

func TestAuthenticate_OAuthCodeFlow_NewUser(t *testing.T) {
	clearData(t)
	fp := newFakeProvider(t)
	wireOAuthOnTestService(t, fp)

	fp.issueCode("authz-code-1")

	resp, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "workos",
		Profile: map[string]string{
			"code":         "authz-code-1",
			"redirect_uri": "https://app.acme.com/auth/callback",
		},
	})
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
	wireOAuthOnTestService(t, fp)

	fp.issueCode("single-use-code")

	_, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "workos",
		Profile: map[string]string{
			"code":         "single-use-code",
			"redirect_uri": "https://app.acme.com/auth/callback",
		},
	})
	require.NoError(t, err)

	// Second attempt with the same code must fail at the token endpoint.
	_, err = testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "workos",
		Profile: map[string]string{
			"code":         "single-use-code",
			"redirect_uri": "https://app.acme.com/auth/callback",
		},
	})
	require.Error(t, err)
}

func TestAuthenticate_OAuthCodeFlow_MissingRedirectURI(t *testing.T) {
	clearData(t)
	fp := newFakeProvider(t)
	wireOAuthOnTestService(t, fp)

	fp.issueCode("x")

	_, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "workos",
		Profile: map[string]string{
			"code": "x",
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "redirect_uri")
}

func TestAuthenticate_OAuthCodeFlow_ProviderMismatch(t *testing.T) {
	clearData(t)
	fp := newFakeProvider(t)
	wireOAuthOnTestService(t, fp)

	fp.issueCode("mismatch-code")

	_, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "auth0", // fake provider says "workos"
		Profile: map[string]string{
			"code":         "mismatch-code",
			"redirect_uri": "https://app.acme.com/auth/callback",
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider mismatch")
}

func TestAuthenticate_LegacyPath_StillWorks(t *testing.T) {
	// Even with OAuth wiring in place, the dev/fixture path (code absent)
	// must continue to work — it's how tests and dev-admin log in.
	clearData(t)
	fp := newFakeProvider(t)
	wireOAuthOnTestService(t, fp)

	resp, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider:      "google",
		ProviderId:    "google-legacy",
		ProviderEmail: "legacy@test.local",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
	require.NotEmpty(t, resp.RefreshToken)
}

// Sanity: prove errors.Is propagation still works across the path.
func TestAuthenticate_OAuthCodeFlow_RealError(t *testing.T) {
	clearData(t)
	fp := newFakeProvider(t)
	wireOAuthOnTestService(t, fp)

	// No code issued → server returns invalid_grant
	_, err := testService.Authenticate(testCtx, &gen.AuthenticateRequest{
		Provider: "workos",
		Profile: map[string]string{
			"code":         "never-issued",
			"redirect_uri": "https://app.acme.com/auth/callback",
		},
	})
	require.Error(t, err)
	require.False(t, errors.Is(err, context.Canceled))
}
