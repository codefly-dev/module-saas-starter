package adapters

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestInjectHeaderJWTCredential(t *testing.T) {
	t.Cleanup(func() { SetHeaderJWTLoginHeader("") })

	t.Run("no header configured is a no-op", func(t *testing.T) {
		SetHeaderJWTLoginHeader("")
		req := &gen.AuthenticateRequest{}
		h := http.Header{"X-Auth-Jwt": {"tok"}}
		injectHeaderJWTCredential(req, h)
		require.Nil(t, req.Authentication)
	})

	t.Run("present header becomes the credential", func(t *testing.T) {
		SetHeaderJWTLoginHeader("X-Auth-Jwt")
		req := &gen.AuthenticateRequest{}
		h := http.Header{"X-Auth-Jwt": {"signed.jwt.value"}}
		injectHeaderJWTCredential(req, h)

		cred, ok := req.Authentication.(*gen.AuthenticateRequest_HeaderJwt)
		require.True(t, ok)
		require.Equal(t, "signed.jwt.value", cred.HeaderJwt.Token)
	})

	t.Run("configured but absent header leaves request untouched", func(t *testing.T) {
		SetHeaderJWTLoginHeader("X-Auth-Jwt")
		req := &gen.AuthenticateRequest{}
		injectHeaderJWTCredential(req, http.Header{})
		require.Nil(t, req.Authentication)
	})

	t.Run("gateway header overrides a client-supplied credential", func(t *testing.T) {
		SetHeaderJWTLoginHeader("X-Auth-Jwt")
		req := &gen.AuthenticateRequest{
			Authentication: &gen.AuthenticateRequest_Fixture{
				Fixture: &gen.FixtureAuthentication{Token: "smuggled"},
			},
		}
		h := http.Header{"X-Auth-Jwt": {"gateway.jwt"}}
		injectHeaderJWTCredential(req, h)

		cred, ok := req.Authentication.(*gen.AuthenticateRequest_HeaderJwt)
		require.True(t, ok)
		require.Equal(t, "gateway.jwt", cred.HeaderJwt.Token)
	})
}
