package oidc_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/auth/oidc"
)

// fakeIdP serves a JWKS for a generated RSA key and can mint RS256 tokens
// with arbitrary claims. Exercises the OIDC validator without a network
// dependency on any real provider.
type fakeIdP struct {
	t      *testing.T
	priv   *rsa.PrivateKey
	kid    string
	server *httptest.Server
	issuer string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	fi := &fakeIdP{t: t, priv: priv, kid: "test-kid-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", fi.serveJWKS)
	fi.server = httptest.NewServer(mux)
	fi.issuer = "https://idp.test/tenant"
	t.Cleanup(fi.server.Close)
	return fi
}

func (f *fakeIdP) jwksURL() string { return f.server.URL + "/jwks" }

func (f *fakeIdP) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	n := base64.RawURLEncoding.EncodeToString(f.priv.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.priv.E)).Bytes())
	_ = json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "kid": f.kid, "alg": "RS256", "n": n, "e": e,
		}},
	})
}

func (f *fakeIdP) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = f.kid
	signed, err := token.SignedString(f.priv)
	require.NoError(t, err)
	return signed
}

func (f *fakeIdP) validClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":             f.issuer,
		"sub":             "user_01ABCXYZ",
		"email":           "user@acme.com",
		"email_verified":  true,
		"organization_id": "org_01DEFGHI",
		"iat":             now.Unix(),
		"nbf":             now.Add(-time.Second).Unix(),
		"exp":             now.Add(15 * time.Minute).Unix(),
	}
}

func newValidator(t *testing.T, fi *fakeIdP) *oidc.Validator {
	t.Helper()
	v, err := oidc.New(oidc.Config{
		ProviderName: "test-provider",
		Issuer:       fi.issuer,
		JWKSURL:      fi.jwksURL(),
	})
	require.NoError(t, err)
	return v
}

// ============================================================================

func TestNew_ValidatesRequiredFields(t *testing.T) {
	_, err := oidc.New(oidc.Config{})
	require.Error(t, err)

	_, err = oidc.New(oidc.Config{ProviderName: "x"})
	require.Error(t, err)

	_, err = oidc.New(oidc.Config{ProviderName: "x", Issuer: "y"})
	require.Error(t, err)
}

func TestValidate_Happy(t *testing.T) {
	ctx := context.Background()
	fi := newFakeIdP(t)
	v := newValidator(t, fi)

	claims, err := v.Validate(ctx, fi.sign(t, fi.validClaims()))
	require.NoError(t, err)
	require.Equal(t, "test-provider", claims.Provider)
	require.Equal(t, "user_01ABCXYZ", claims.Subject)
	require.Equal(t, "user@acme.com", claims.Email)
	require.Equal(t, "org_01DEFGHI", claims.ProviderOrgID)
	require.False(t, claims.ExpiresAt.IsZero())
}

func TestValidate_Expired(t *testing.T) {
	ctx := context.Background()
	fi := newFakeIdP(t)
	v := newValidator(t, fi)

	c := fi.validClaims()
	c["exp"] = time.Now().Add(-1 * time.Hour).Unix()
	c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
	c["nbf"] = time.Now().Add(-2 * time.Hour).Unix()

	_, err := v.Validate(ctx, fi.sign(t, c))
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrTokenExpired))
}

func TestValidate_WrongIssuer(t *testing.T) {
	ctx := context.Background()
	fi := newFakeIdP(t)
	v := newValidator(t, fi)

	c := fi.validClaims()
	c["iss"] = "https://attacker.example.com"

	_, err := v.Validate(ctx, fi.sign(t, c))
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrTokenWrongIssuer))
}

func TestValidate_MissingEmail(t *testing.T) {
	ctx := context.Background()
	fi := newFakeIdP(t)
	v := newValidator(t, fi)

	c := fi.validClaims()
	delete(c, "email")

	_, err := v.Validate(ctx, fi.sign(t, c))
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrMissingEmail))
}

func TestValidate_AllowsProviderAdapterToSupplyEmailAndChecksClientIDClaim(t *testing.T) {
	ctx := context.Background()
	fi := newFakeIdP(t)
	v, err := oidc.New(oidc.Config{
		ProviderName:      "workos",
		Issuer:            fi.issuer,
		JWKSURL:           fi.jwksURL(),
		OrgClaim:          "org_id",
		AllowMissingEmail: true,
		ClientIDClaim:     "client_id",
		ClientID:          "client_123",
	})
	require.NoError(t, err)

	c := fi.validClaims()
	delete(c, "email")
	c["client_id"] = "client_123"
	c["org_id"] = "org_123"
	claims, err := v.Validate(ctx, fi.sign(t, c))
	require.NoError(t, err)
	require.Empty(t, claims.Email)
	require.Equal(t, "org_123", claims.ProviderOrgID)

	c["client_id"] = "different-client"
	_, err = v.Validate(ctx, fi.sign(t, c))
	require.ErrorIs(t, err, auth.ErrTokenWrongAudience)
}

func TestValidate_UnknownKid(t *testing.T) {
	ctx := context.Background()
	fi := newFakeIdP(t)
	v := newValidator(t, fi)

	otherPriv, _ := rsa.GenerateKey(rand.Reader, 2048)
	tk := jwt.NewWithClaims(jwt.SigningMethodRS256, fi.validClaims())
	tk.Header["kid"] = "unknown-kid"
	signed, _ := tk.SignedString(otherPriv)

	_, err := v.Validate(ctx, signed)
	require.Error(t, err)
}

func TestValidate_WrongSignature(t *testing.T) {
	ctx := context.Background()
	fi := newFakeIdP(t)
	v := newValidator(t, fi)

	otherPriv, _ := rsa.GenerateKey(rand.Reader, 2048)
	tk := jwt.NewWithClaims(jwt.SigningMethodRS256, fi.validClaims())
	tk.Header["kid"] = fi.kid
	signed, _ := tk.SignedString(otherPriv)

	_, err := v.Validate(ctx, signed)
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrTokenSignature))
}

func TestValidate_AlgNoneRejected(t *testing.T) {
	ctx := context.Background()
	fi := newFakeIdP(t)
	v := newValidator(t, fi)

	header := `{"alg":"none","typ":"JWT","kid":"` + fi.kid + `"}`
	payload := fmt.Sprintf(`{"iss":%q,"sub":"x","email":"x@y.com","exp":9999999999}`, fi.issuer)
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	forged := enc(header) + "." + enc(payload) + "."

	_, err := v.Validate(ctx, forged)
	require.Error(t, err)
}

func TestValidate_JWKSCaching(t *testing.T) {
	ctx := context.Background()
	fi := newFakeIdP(t)

	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		calls++
		fi.serveJWKS(w, r)
	})
	cachingServer := httptest.NewServer(mux)
	t.Cleanup(cachingServer.Close)

	v, err := oidc.New(oidc.Config{
		ProviderName: "test",
		Issuer:       fi.issuer,
		JWKSURL:      cachingServer.URL + "/jwks",
		CacheTTL:     5 * time.Minute,
	})
	require.NoError(t, err)

	for range 10 {
		_, err := v.Validate(ctx, fi.sign(t, fi.validClaims()))
		require.NoError(t, err)
	}
	require.Equal(t, 1, calls, "JWKS must be fetched exactly once across 10 validations")
}

// ============================================================================
// Presets
// ============================================================================

func TestWorkOSConfig(t *testing.T) {
	c := oidc.WorkOSConfig("client_01ABC")
	require.Equal(t, "workos", c.ProviderName)
	require.Equal(t,
		"https://api.workos.com/user_management/client_01ABC", c.Issuer)
	require.Equal(t, "https://api.workos.com/sso/jwks/client_01ABC", c.JWKSURL)
	require.Equal(t, "client_id", c.ClientIDClaim)
	require.Equal(t, "client_01ABC", c.ClientID)
	require.Equal(t, "org_id", c.OrgClaim)
	require.True(t, c.AllowMissingEmail)
}

func TestAuth0Config(t *testing.T) {
	c := oidc.Auth0Config("acme.auth0.com", "https://api.acme.com")
	require.Equal(t, "auth0", c.ProviderName)
	require.Equal(t, "https://acme.auth0.com/", c.Issuer)
	require.Contains(t, c.JWKSURL, "well-known/jwks.json")
	require.Equal(t, "https://api.acme.com", c.Audience)
	require.Equal(t, "org_id", c.OrgClaim)
}

func TestClerkConfig(t *testing.T) {
	c := oidc.ClerkConfig("clerk.acme.com")
	require.Equal(t, "clerk", c.ProviderName)
	require.Equal(t, "https://clerk.acme.com", c.Issuer)
}

func TestGoogleConfig(t *testing.T) {
	c := oidc.GoogleConfig("client-id.apps.googleusercontent.com")
	require.Equal(t, "google", c.ProviderName)
	require.Equal(t, "https://accounts.google.com", c.Issuer)
	require.Equal(t, "client-id.apps.googleusercontent.com", c.Audience)
	require.Equal(t, "", c.OrgClaim)
}

// ============================================================================
// Preset → Validator smoke: make sure presets plug into the generic New.
// ============================================================================

func TestWorkOSPresetWithFakeJWKS(t *testing.T) {
	ctx := context.Background()
	fi := newFakeIdP(t)

	cfg := oidc.WorkOSConfig("client_01ABC")
	cfg.Issuer = fi.issuer
	cfg.JWKSURL = fi.jwksURL()

	v, err := oidc.New(cfg)
	require.NoError(t, err)

	workOSClaims := fi.validClaims()
	delete(workOSClaims, "organization_id")
	workOSClaims["org_id"] = "org_01DEFGHI"
	workOSClaims["client_id"] = "client_01ABC"
	claims, err := v.Validate(ctx, fi.sign(t, workOSClaims))
	require.NoError(t, err)
	require.Equal(t, "workos", claims.Provider)
	require.Equal(t, "org_01DEFGHI", claims.ProviderOrgID,
		"WorkOS preset must use the 'org_id' claim name")
}
