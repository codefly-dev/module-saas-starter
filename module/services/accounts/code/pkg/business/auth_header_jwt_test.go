package business_test

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

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/auth/headerjwt"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// headerJWTIssuer is a minimal signing authority: one RSA key published at
// /jwks, used to mint the gateway-injected identity header.
type headerJWTIssuer struct {
	priv   *rsa.PrivateKey
	kid    string
	server *httptest.Server
}

func newHeaderJWTIssuer(t *testing.T) *headerJWTIssuer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	fi := &headerJWTIssuer{priv: priv, kid: "gw"}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(fi.priv.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(fi.priv.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA", "use": "sig", "kid": fi.kid, "alg": "RS256", "n": n, "e": e,
			}},
		})
	})
	fi.server = httptest.NewServer(mux)
	t.Cleanup(fi.server.Close)
	return fi
}

func (f *headerJWTIssuer) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.kid
	s, err := tok.SignedString(f.priv)
	require.NoError(t, err)
	return s
}

func wireHeaderJWTOnTestService(t *testing.T, fi *headerJWTIssuer, cfg headerjwt.Config) {
	t.Helper()
	cfg.ProviderName = "header-jwt"
	cfg.JWKSURL = fi.server.URL + "/jwks"
	cfg.Audience = "codefly-app"
	v, err := headerjwt.New(cfg)
	require.NoError(t, err)
	testService.SetTokenValidator(v)
	t.Cleanup(func() { testService.SetTokenValidator(nil) })
}

func headerJWTRequest(token string) *gen.AuthenticateRequest {
	return &gen.AuthenticateRequest{
		Provider:       "header-jwt",
		Authentication: &gen.AuthenticateRequest_HeaderJwt{HeaderJwt: &gen.HeaderJWTAuthentication{Token: token}},
	}
}

func TestAuthenticate_HeaderJWT_NewUser(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available (run with codefly test)")
	}
	clearData(t)
	fi := newHeaderJWTIssuer(t)
	wireHeaderJWTOnTestService(t, fi, headerjwt.Config{NameClaims: []string{"given_name", "family_name"}})

	now := time.Now()
	token := fi.sign(t, jwt.MapClaims{
		"sub":            "gw-user-7",
		"email":          "grace@acme.example",
		"email_verified": true,
		"given_name":     "Grace",
		"family_name":    "Hopper",
		"aud":            "codefly-app",
		"iat":            now.Unix(),
		"exp":            now.Add(15 * time.Minute).Unix(),
	})

	resp, err := testService.Authenticate(testCtx, headerJWTRequest(token))
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
	require.NotEmpty(t, resp.User.Uuid)

	identity, err := testService.JWTMinter().VerifyAccess(resp.AccessToken)
	require.NoError(t, err)
	require.Equal(t, []string{auth.AuthenticationMethodHeaderJWT}, identity.AuthenticationMethods)

	// user_identities row is keyed by the configured provider + subject claim.
	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   "header-jwt",
		ProviderId: "gw-user-7",
	})
	require.NoError(t, err)
	require.True(t, resolved.Found)
	require.Equal(t, resp.User.Uuid, resolved.UserId)

	// The multi-claim display name is persisted on the provisioned user.
	user, err := testStore.GetUser(testCtx, resp.User.Uuid)
	require.NoError(t, err)
	require.Equal(t, "Grace Hopper", user.Profile["display_name"])
}

func TestAuthenticate_HeaderJWT_GroupGateRejection(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available (run with codefly test)")
	}
	clearData(t)
	fi := newHeaderJWTIssuer(t)
	wireHeaderJWTOnTestService(t, fi, headerjwt.Config{
		GroupClaim:    "groups",
		AllowedGroups: []string{"admins"},
	})

	now := time.Now()
	token := fi.sign(t, jwt.MapClaims{
		"sub":            "gw-user-8",
		"email":          "outsider@acme.example",
		"email_verified": true,
		"groups":         []any{"eng", "ops"},
		"aud":            "codefly-app",
		"iat":            now.Unix(),
		"exp":            now.Add(15 * time.Minute).Unix(),
	})

	_, err := testService.Authenticate(testCtx, headerJWTRequest(token))
	require.ErrorIs(t, err, auth.ErrGroupNotAllowed)
}

func TestAuthenticate_HeaderJWT_ExpiredRejected(t *testing.T) {
	if testService == nil {
		t.Skip("test infrastructure not available (run with codefly test)")
	}
	clearData(t)
	fi := newHeaderJWTIssuer(t)
	wireHeaderJWTOnTestService(t, fi, headerjwt.Config{})

	token := fi.sign(t, jwt.MapClaims{
		"sub":            "gw-user-9",
		"email":          "stale@acme.example",
		"email_verified": true,
		"aud":            "codefly-app",
		"exp":            time.Now().Add(-time.Hour).Unix(),
	})

	_, err := testService.Authenticate(testCtx, headerJWTRequest(token))
	require.ErrorIs(t, err, auth.ErrTokenExpired)
}
