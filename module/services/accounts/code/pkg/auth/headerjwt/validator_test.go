package headerjwt

import (
	"context"
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
)

// fakeIssuer stands in for the customer gateway's signing authority: it holds
// one RSA key, publishes it at /jwks, and mints tokens the validator verifies.
type fakeIssuer struct {
	priv   *rsa.PrivateKey
	kid    string
	server *httptest.Server
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	fi := &fakeIssuer{priv: priv, kid: "gw-kid"}
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

func (f *fakeIssuer) jwksURL() string { return f.server.URL + "/jwks" }

func (f *fakeIssuer) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.kid
	s, err := tok.SignedString(f.priv)
	require.NoError(t, err)
	return s
}

func baseClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"sub":            "gw-user-1",
		"email":          "user@acme.example",
		"email_verified": true,
		"aud":            "codefly-app",
		"iat":            now.Unix(),
		"nbf":            now.Add(-time.Second).Unix(),
		"exp":            now.Add(15 * time.Minute).Unix(),
	}
}

func TestValidateHappyPath(t *testing.T) {
	fi := newFakeIssuer(t)
	v, err := New(Config{
		ProviderName: "header-jwt",
		JWKSURL:      fi.jwksURL(),
		Audience:     "codefly-app",
		NameClaims:   []string{"given_name", "family_name"},
	})
	require.NoError(t, err)

	claims := baseClaims()
	claims["given_name"] = "Ada"
	claims["family_name"] = "Lovelace"

	got, err := v.Validate(context.Background(), fi.sign(t, claims))
	require.NoError(t, err)
	require.Equal(t, "header-jwt", got.Provider)
	require.Equal(t, "gw-user-1", got.Subject)
	require.Equal(t, "user@acme.example", got.Email)
	require.True(t, got.EmailVerified)
	require.Equal(t, "Ada Lovelace", got.DisplayName)
}

func TestValidateConfigurableClaimNames(t *testing.T) {
	fi := newFakeIssuer(t)
	v, err := New(Config{
		ProviderName:       "ping",
		JWKSURL:            fi.jwksURL(),
		Audience:           "codefly-app",
		SubjectClaim:       "uid",
		EmailClaim:         "mail",
		EmailVerifiedClaim: "mail_ok",
		NameClaims:         []string{"cn"},
	})
	require.NoError(t, err)

	claims := baseClaims()
	delete(claims, "sub")
	delete(claims, "email")
	delete(claims, "email_verified")
	claims["uid"] = "ping-42"
	claims["mail"] = "person@corp.example"
	claims["mail_ok"] = "true" // string form
	claims["cn"] = "Grace Hopper"

	got, err := v.Validate(context.Background(), fi.sign(t, claims))
	require.NoError(t, err)
	require.Equal(t, "ping-42", got.Subject)
	require.Equal(t, "person@corp.example", got.Email)
	require.True(t, got.EmailVerified)
	require.Equal(t, "Grace Hopper", got.DisplayName)
}

func TestValidateExpired(t *testing.T) {
	fi := newFakeIssuer(t)
	v, err := New(Config{ProviderName: "hj", JWKSURL: fi.jwksURL(), Audience: "codefly-app"})
	require.NoError(t, err)

	claims := baseClaims()
	claims["exp"] = time.Now().Add(-time.Hour).Unix()

	_, err = v.Validate(context.Background(), fi.sign(t, claims))
	require.ErrorIs(t, err, auth.ErrTokenExpired)
}

func TestValidateAudienceMismatch(t *testing.T) {
	fi := newFakeIssuer(t)
	v, err := New(Config{ProviderName: "hj", JWKSURL: fi.jwksURL(), Audience: "codefly-app"})
	require.NoError(t, err)

	claims := baseClaims()
	claims["aud"] = "some-other-app"

	_, err = v.Validate(context.Background(), fi.sign(t, claims))
	require.ErrorIs(t, err, auth.ErrTokenWrongAudience)
}

func TestValidateIssuerMismatch(t *testing.T) {
	fi := newFakeIssuer(t)
	v, err := New(Config{ProviderName: "hj", JWKSURL: fi.jwksURL(), Audience: "codefly-app", Issuer: "https://gw.corp.example"})
	require.NoError(t, err)

	claims := baseClaims()
	claims["iss"] = "https://evil.example"

	_, err = v.Validate(context.Background(), fi.sign(t, claims))
	require.ErrorIs(t, err, auth.ErrTokenWrongIssuer)
}

func TestValidateBadSignature(t *testing.T) {
	fi := newFakeIssuer(t)
	// Validator trusts fi's JWKS, but the token is signed by a different key.
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	v, err := New(Config{ProviderName: "hj", JWKSURL: fi.jwksURL(), Audience: "codefly-app"})
	require.NoError(t, err)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, baseClaims())
	tok.Header["kid"] = fi.kid // claims fi's kid, signed by the wrong key
	signed, err := tok.SignedString(other)
	require.NoError(t, err)

	_, err = v.Validate(context.Background(), signed)
	require.ErrorIs(t, err, auth.ErrTokenSignature)
}

func TestValidateJWKSUnreachableFailsClosed(t *testing.T) {
	fi := newFakeIssuer(t)
	signed := fi.sign(t, baseClaims())
	fi.server.Close() // JWKS endpoint now unreachable

	v, err := New(Config{ProviderName: "hj", JWKSURL: fi.jwksURL(), Audience: "codefly-app"})
	require.NoError(t, err)

	_, err = v.Validate(context.Background(), signed)
	require.ErrorIs(t, err, auth.ErrJWKSUnavailable)
}

func TestGroupGate(t *testing.T) {
	fi := newFakeIssuer(t)

	t.Run("string overlap allows", func(t *testing.T) {
		v, err := New(Config{
			ProviderName: "hj", JWKSURL: fi.jwksURL(), Audience: "codefly-app",
			GroupClaim: "groups", AllowedGroups: []string{"eng", "ops"},
		})
		require.NoError(t, err)
		claims := baseClaims()
		claims["groups"] = "eng"
		_, err = v.Validate(context.Background(), fi.sign(t, claims))
		require.NoError(t, err)
	})

	t.Run("array overlap allows", func(t *testing.T) {
		v, err := New(Config{
			ProviderName: "hj", JWKSURL: fi.jwksURL(), Audience: "codefly-app",
			GroupClaim: "groups", AllowedGroups: []string{"ops"},
		})
		require.NoError(t, err)
		claims := baseClaims()
		claims["groups"] = []any{"eng", "ops"}
		_, err = v.Validate(context.Background(), fi.sign(t, claims))
		require.NoError(t, err)
	})

	t.Run("no overlap is a distinct rejection", func(t *testing.T) {
		v, err := New(Config{
			ProviderName: "hj", JWKSURL: fi.jwksURL(), Audience: "codefly-app",
			GroupClaim: "groups", AllowedGroups: []string{"admins"},
		})
		require.NoError(t, err)
		claims := baseClaims()
		claims["groups"] = []any{"eng", "ops"}
		_, err = v.Validate(context.Background(), fi.sign(t, claims))
		require.ErrorIs(t, err, auth.ErrGroupNotAllowed)
	})

	t.Run("missing group claim under a gate denies", func(t *testing.T) {
		v, err := New(Config{
			ProviderName: "hj", JWKSURL: fi.jwksURL(), Audience: "codefly-app",
			GroupClaim: "groups", AllowedGroups: []string{"admins"},
		})
		require.NoError(t, err)
		_, err = v.Validate(context.Background(), fi.sign(t, baseClaims()))
		require.ErrorIs(t, err, auth.ErrGroupNotAllowed)
	})

	t.Run("unset gate is skipped", func(t *testing.T) {
		v, err := New(Config{
			ProviderName: "hj", JWKSURL: fi.jwksURL(), Audience: "codefly-app",
			GroupClaim: "groups", // no AllowedGroups
		})
		require.NoError(t, err)
		_, err = v.Validate(context.Background(), fi.sign(t, baseClaims()))
		require.NoError(t, err)
	})
}

func TestPerimeterTrustDecode(t *testing.T) {
	fi := newFakeIssuer(t)

	t.Run("off by default requires JWKS", func(t *testing.T) {
		_, err := New(Config{ProviderName: "hj", Audience: "codefly-app"})
		require.Error(t, err)
	})

	t.Run("decodes without signature but enforces aud", func(t *testing.T) {
		v, err := New(Config{ProviderName: "hj", Audience: "codefly-app", PerimeterTrustDecode: true})
		require.NoError(t, err)

		// Signed by an unrelated key and no JWKS is configured; a verifying
		// validator could never accept this, proving the signature is skipped.
		other, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, baseClaims())
		signed, err := tok.SignedString(other)
		require.NoError(t, err)

		got, err := v.Validate(context.Background(), signed)
		require.NoError(t, err)
		require.Equal(t, "gw-user-1", got.Subject)
	})

	t.Run("still rejects expired", func(t *testing.T) {
		v, err := New(Config{ProviderName: "hj", Audience: "codefly-app", PerimeterTrustDecode: true})
		require.NoError(t, err)
		claims := baseClaims()
		claims["exp"] = time.Now().Add(-time.Hour).Unix()
		_, err = v.Validate(context.Background(), fi.sign(t, claims))
		require.ErrorIs(t, err, auth.ErrTokenExpired)
	})

	t.Run("still rejects audience mismatch", func(t *testing.T) {
		v, err := New(Config{ProviderName: "hj", Audience: "codefly-app", PerimeterTrustDecode: true})
		require.NoError(t, err)
		claims := baseClaims()
		claims["aud"] = "wrong"
		_, err = v.Validate(context.Background(), fi.sign(t, claims))
		require.ErrorIs(t, err, auth.ErrTokenWrongAudience)
	})
}

func TestNewValidation(t *testing.T) {
	_, err := New(Config{JWKSURL: "https://x/jwks", Audience: "a"})
	require.Error(t, err, "ProviderName required")

	_, err = New(Config{ProviderName: "hj", JWKSURL: "https://x/jwks"})
	require.Error(t, err, "Audience required")

	_, err = New(Config{ProviderName: "hj", Audience: "a"})
	require.Error(t, err, "JWKSURL required unless perimeter-trust")
}

func TestMissingSubjectAndEmail(t *testing.T) {
	fi := newFakeIssuer(t)
	v, err := New(Config{ProviderName: "hj", JWKSURL: fi.jwksURL(), Audience: "codefly-app"})
	require.NoError(t, err)

	noSub := baseClaims()
	delete(noSub, "sub")
	_, err = v.Validate(context.Background(), fi.sign(t, noSub))
	require.ErrorIs(t, err, auth.ErrMissingSubject)

	noEmail := baseClaims()
	delete(noEmail, "email")
	_, err = v.Validate(context.Background(), fi.sign(t, noEmail))
	require.ErrorIs(t, err, auth.ErrMissingEmail)
}
