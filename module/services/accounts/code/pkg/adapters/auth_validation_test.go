package adapters

import (
	"strings"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

func TestAuthenticateRequestValidationSeparatesOAuthAndFixtureCredentials(t *testing.T) {
	oauthRequest := &gen.AuthenticateRequest{
		Provider: "workos",
		Authentication: &gen.AuthenticateRequest_OauthCode{OauthCode: &gen.OAuthCodeAuthentication{
			Code:         "authorization-code",
			RedirectUri:  "https://app.example.com/auth/callback",
			State:        "signed-state",
			CodeVerifier: strings.Repeat("a", 43),
		}},
	}
	require.NoError(t, Validate(oauthRequest), "OAuth credentials must not require fixture fields")

	fixtureRequest := &gen.AuthenticateRequest{
		Provider:       "email",
		Authentication: &gen.AuthenticateRequest_Fixture{Fixture: &gen.FixtureAuthentication{Token: "dev-admin"}},
	}
	require.NoError(t, Validate(fixtureRequest), "fixture credentials must not require OAuth fields")

	require.Error(t, Validate(&gen.AuthenticateRequest{Provider: "workos"}), "authentication oneof is required")
}
