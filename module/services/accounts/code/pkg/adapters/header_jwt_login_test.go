package adapters

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// captureBody is a terminal handler that records the request body grpc-gateway
// would see after the REST middleware runs.
type captureBody struct{ body []byte }

func (c *captureBody) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	c.body, _ = io.ReadAll(r.Body)
}

func serveThroughConsume(t *testing.T, method, path, headerName, headerValue, body string) map[string]json.RawMessage {
	t.Helper()
	sink := &captureBody{}
	h := consumeHeaderJWTLogin(sink)

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if headerName != "" && headerValue != "" {
		req.Header.Set(headerName, headerValue)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := map[string]json.RawMessage{}
	if len(sink.body) > 0 {
		require.NoError(t, json.Unmarshal(sink.body, &out))
	}
	return out
}

// TestConsumeHeaderJWTLogin exercises the REST-surface consumption that the
// grpc-gateway path actually uses — the path the browser hits at
// POST /v1/auth/authenticate, where the connect-handler injection never sees
// the configured header.
func TestConsumeHeaderJWTLogin(t *testing.T) {
	t.Cleanup(func() { SetHeaderJWTLoginHeader("") })

	t.Run("gateway header becomes the body credential", func(t *testing.T) {
		SetHeaderJWTLoginHeader("X-Auth-Jwt")
		got := serveThroughConsume(t, http.MethodPost, "/v1/auth/authenticate",
			"X-Auth-Jwt", "signed.jwt.value", `{"provider":"header-jwt"}`)

		require.JSONEq(t, `{"token":"signed.jwt.value"}`, string(got["header_jwt"]))
		require.JSONEq(t, `"header-jwt"`, string(got["provider"]), "unrelated fields preserved")
	})

	t.Run("client-supplied body credential is stripped and replaced by the header", func(t *testing.T) {
		SetHeaderJWTLoginHeader("X-Auth-Jwt")
		got := serveThroughConsume(t, http.MethodPost, "/v1/auth/authenticate",
			"X-Auth-Jwt", "real.jwt",
			`{"header_jwt":{"token":"forged"},"fixture":{"token":"x"}}`)

		require.JSONEq(t, `{"token":"real.jwt"}`, string(got["header_jwt"]))
		_, hasFixture := got["fixture"]
		require.False(t, hasFixture, "client-supplied fixture credential must be stripped")
	})

	t.Run("no header strips a smuggled credential and leaves none", func(t *testing.T) {
		// Perimeter-trust decode would accept an unsigned token; a client must
		// not be able to supply one in the body when the gateway header is absent.
		SetHeaderJWTLoginHeader("X-Auth-Jwt")
		got := serveThroughConsume(t, http.MethodPost, "/v1/auth/authenticate",
			"", "", `{"header_jwt":{"token":"forged"}}`)

		_, hasCred := got["header_jwt"]
		require.False(t, hasCred, "no trusted header means no header_jwt credential")
	})

	t.Run("no provider configured is a pass-through", func(t *testing.T) {
		SetHeaderJWTLoginHeader("")
		got := serveThroughConsume(t, http.MethodPost, "/v1/auth/authenticate",
			"X-Auth-Jwt", "signed.jwt", `{"header_jwt":{"token":"client"}}`)
		require.JSONEq(t, `{"token":"client"}`, string(got["header_jwt"]), "body untouched when not header-jwt mode")
	})

	t.Run("non-login path is untouched", func(t *testing.T) {
		SetHeaderJWTLoginHeader("X-Auth-Jwt")
		got := serveThroughConsume(t, http.MethodPost, "/v1/auth/refresh",
			"X-Auth-Jwt", "signed.jwt", `{"refresh_token":"r"}`)
		_, hasCred := got["header_jwt"]
		require.False(t, hasCred, "only the authenticate route consumes the header")
		require.JSONEq(t, `"r"`, string(got["refresh_token"]))
	})
}

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

	t.Run("absent header drops a smuggled client credential", func(t *testing.T) {
		// In header-jwt mode the trusted header is the only credential source.
		// When it is absent, a client-supplied credential must not survive —
		// otherwise a caller could authenticate with a body-smuggled token.
		SetHeaderJWTLoginHeader("X-Auth-Jwt")
		req := &gen.AuthenticateRequest{
			Authentication: &gen.AuthenticateRequest_Fixture{
				Fixture: &gen.FixtureAuthentication{Token: "smuggled"},
			},
		}
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
