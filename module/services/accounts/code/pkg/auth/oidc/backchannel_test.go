package oidc_test

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/auth/oidc"
)

// validLogoutClaims is a back-channel logout token as OpenID Connect
// Back-Channel Logout 1.0 §2.4 specifies one: no `exp` (OPTIONAL there, and
// omitted by real providers), no `nonce`, and an events claim whose single
// member is the back-channel logout event with an empty object value.
func (f *fakeIdP) validLogoutClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":    f.issuer,
		"aud":    "test-audience",
		"sub":    "user_01ABCXYZ",
		"sid":    "provider-session-7",
		"iat":    now.Unix(),
		"jti":    "logout-token-1",
		"events": map[string]any{auth.BackchannelLogoutEvent: map[string]any{}},
	}
}

func TestLogoutTokenIsAdmittedWithItsSubjectAndSession(t *testing.T) {
	idp := newFakeIdP(t)
	validator := newValidator(t, idp)

	token, err := validator.ValidateLogoutToken(context.Background(), idp.sign(t, idp.validLogoutClaims()))
	require.NoError(t, err)
	require.Equal(t, "test-provider", token.Provider)
	require.Equal(t, "user_01ABCXYZ", token.Subject)
	require.Equal(t, "provider-session-7", token.SessionID)
	require.Equal(t, "logout-token-1", token.ID)
	require.WithinDuration(t, time.Now(), token.IssuedAt, time.Minute)
}

// A token carrying only `sid` is admitted here: it is a valid logout token, and
// whether this host can act on one is the business layer's answer, not the
// verifier's. Refusing it here would conflate "not a logout token" with "a
// logout token naming something we do not record".
func TestLogoutTokenWithOnlyASessionIDIsAdmitted(t *testing.T) {
	idp := newFakeIdP(t)
	validator := newValidator(t, idp)
	claims := idp.validLogoutClaims()
	delete(claims, "sub")

	token, err := validator.ValidateLogoutToken(context.Background(), idp.sign(t, claims))
	require.NoError(t, err)
	require.Empty(t, token.Subject)
	require.Equal(t, "provider-session-7", token.SessionID)
}

// The refusals, each mutated one at a time from an otherwise valid token, so a
// case cannot pass because an earlier check fired. The id_token case is the
// load-bearing one: an id_token is signed by the same key, names the same
// issuer and audience, and would otherwise make every sign-in a replayable
// sign-out for whoever holds the token.
func TestLogoutTokenRefusals(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(jwt.MapClaims)
	}{
		{"an ordinary id_token, which carries a nonce", func(c jwt.MapClaims) {
			c["nonce"] = "the-authorize-request-nonce"
		}},
		{"no events claim at all", func(c jwt.MapClaims) {
			delete(c, "events")
		}},
		{"an events claim without the back-channel logout member", func(c jwt.MapClaims) {
			c["events"] = map[string]any{"http://schemas.openid.net/event/something-else": map[string]any{}}
		}},
		{"an events claim that is not an object", func(c jwt.MapClaims) {
			c["events"] = auth.BackchannelLogoutEvent
		}},
		{"neither a subject nor a session id", func(c jwt.MapClaims) {
			delete(c, "sub")
			delete(c, "sid")
		}},
		{"another relying party's audience", func(c jwt.MapClaims) {
			c["aud"] = "someone-elses-audience"
		}},
		{"another issuer", func(c jwt.MapClaims) {
			c["iss"] = "https://idp.test/other-tenant"
		}},
		{"no iat, so nothing bounds its life", func(c jwt.MapClaims) {
			delete(c, "iat")
		}},
		{"an iat far in the past, which is a replay", func(c jwt.MapClaims) {
			c["iat"] = time.Now().Add(-time.Hour).Unix()
		}},
		{"an expiry already passed", func(c jwt.MapClaims) {
			c["exp"] = time.Now().Add(-time.Minute).Unix()
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			idp := newFakeIdP(t)
			validator := newValidator(t, idp)
			claims := idp.validLogoutClaims()
			testCase.mutate(claims)

			_, err := validator.ValidateLogoutToken(context.Background(), idp.sign(t, claims))
			require.ErrorIs(t, err, auth.ErrNotALogoutToken)
		})
	}
}

// A logout token signed by a key the provider's JWKS does not publish is
// refused as every other unverifiable token is. Signing with a second
// generated key under the first key's `kid` is the attack this closes: the
// header says a key the validator will fetch, and the signature is not that
// key's.
func TestLogoutTokenSignedByAnotherKeyIsRefused(t *testing.T) {
	idp := newFakeIdP(t)
	other := newFakeIdP(t)
	other.issuer = idp.issuer
	other.kid = idp.kid
	validator := newValidator(t, idp)

	_, err := validator.ValidateLogoutToken(context.Background(), other.sign(t, idp.validLogoutClaims()))
	require.ErrorIs(t, err, auth.ErrNotALogoutToken)
}

// A provider whose tokens bind to the relying party through a non-standard
// client_id claim rather than through `aud` binds its logout tokens the same
// way, and a logout token that does not carry it is refused — a validator with
// no audience binding would admit any correctly-signed token from the issuer,
// which is the confusion oidc.New refuses to be constructed into.
func TestLogoutTokenHonoursAClientIDClaimBinding(t *testing.T) {
	idp := newFakeIdP(t)
	validator := newClientIDBoundValidator(t, idp)

	claims := idp.validLogoutClaims()
	delete(claims, "aud")
	_, err := validator.ValidateLogoutToken(context.Background(), idp.sign(t, claims))
	require.ErrorIs(t, err, auth.ErrNotALogoutToken)

	claims["client_id"] = "the-registered-client"
	token, err := validator.ValidateLogoutToken(context.Background(), idp.sign(t, claims))
	require.NoError(t, err)
	require.Equal(t, "user_01ABCXYZ", token.Subject)
}

// newClientIDBoundValidator binds to the relying party through a non-standard
// `client_id` claim instead of `aud`, as a provider such as WorkOS does.
func newClientIDBoundValidator(t *testing.T, fi *fakeIdP) *oidc.Validator {
	t.Helper()
	v, err := oidc.New(oidc.Config{
		ProviderName:  "test-provider",
		Issuer:        fi.issuer,
		JWKSURL:       fi.jwksURL(),
		ClientIDClaim: "client_id",
		ClientID:      "the-registered-client",
	})
	require.NoError(t, err)
	return v
}
