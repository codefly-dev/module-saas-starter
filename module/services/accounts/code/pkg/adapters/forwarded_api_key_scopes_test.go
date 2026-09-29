package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

// forwardedTransport admits one gateway-forwarded request on one of the
// transports that accept a forwarded identity, and returns the admitted
// context.
type forwardedTransport func(t *testing.T, headers http.Header) (context.Context, error)

func forwardedTransports() map[string]forwardedTransport {
	return map[string]forwardedTransport{
		"connect": func(t *testing.T, headers http.Header) (context.Context, error) {
			return (&connectPolicyInterceptor{}).authorize(context.Background(), "/saas.accounts.v1.UserService/GetSelf", headers.Clone())
		},
		// REST is transcoded by a mux built the way rest_gen.go builds it, and
		// the metadata reaches the Connect handler as request headers.
		"rest": func(t *testing.T, headers http.Header) (context.Context, error) {
			mux := runtime.NewServeMux(
				runtime.WithMetadata(CustomHeaderToGRPCMetadataAnnotator),
				runtime.WithIncomingHeaderMatcher(restIdentityHeaderMatcher),
			)
			req := httptest.NewRequest(http.MethodGet, "/v1/users/self", nil)
			req.Header = headers.Clone()
			ctx, err := runtime.AnnotateContext(context.Background(), mux, req, "/saas.accounts.v1.UserService/GetSelf")
			require.NoError(t, err)
			md, _ := metadata.FromOutgoingContext(ctx)
			forwarded := http.Header{}
			for key, values := range md {
				for _, value := range values {
					forwarded.Add(key, value)
				}
			}
			return (&connectPolicyInterceptor{}).authorize(context.Background(), "/saas.accounts.v1.UserService/GetSelf", forwarded)
		},
		"grpc": func(t *testing.T, headers http.Header) (context.Context, error) {
			md := metadata.MD{}
			for key, values := range headers {
				md.Append(strings.ToLower(key), values...)
			}
			return (&grpcPolicyAuthorizer{exposure: rpcExposureTenant}).authorize(
				metadata.NewIncomingContext(context.Background(), md), "/saas.accounts.v1.UserService/GetSelf")
		},
		"billing http": func(t *testing.T, headers http.Header) (context.Context, error) {
			req := httptest.NewRequest(http.MethodGet, "/v1/billing/subscription", nil)
			req.Header = headers.Clone()
			ctx, _, _, err := authenticateHTTPRequest(&business.Service{}, req)
			return ctx, err
		},
	}
}

func apiKeyForwardedHeaders(user, org string) http.Header {
	return http.Header{
		"X-Codefly-Gateway-Token": {"test-gateway-token"},
		"X-User-Id":               {user},
		"X-Org-Id":                {org},
		"X-Credential-Kind":       {credentialKindAPIKey},
	}
}

// The gateway stamps X-Scopes on every API key it admits, empty for a key
// created without scopes. A forwarded API-key identity with no X-Scopes header
// at all lost the key's scope ceiling on the way — a caller's
// `Connection: X-Scopes` did exactly that at the gateway. Every transport
// refuses it rather than guess what the key was bounded by.
func TestForwardedAPIKeyWithoutScopesHeaderIsRefused(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	user := uuid.Must(uuid.NewV7()).String()
	org := uuid.Must(uuid.NewV7()).String()

	for name, admit := range forwardedTransports() {
		t.Run(name, func(t *testing.T) {
			_, err := admit(t, apiKeyForwardedHeaders(user, org))
			require.Error(t, err, "an API key assertion without X-Scopes must not be admitted")
			if name == "grpc" {
				require.Equal(t, codes.PermissionDenied, status.Code(err))
			}
		})
	}
}

// What the gateway actually forwards is projected: a scoped key keeps its
// ceiling, a key created without scopes (X-Scopes present and empty) arrives
// with none — and so, at the scope gate, with no authority — and a session
// carries the empty scope list the gateway stamps for it.
func TestForwardedAPIKeyWithScopesHeaderIsAdmitted(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	user := uuid.Must(uuid.NewV7()).String()
	org := uuid.Must(uuid.NewV7()).String()

	for name, admit := range forwardedTransports() {
		// The plain-HTTP routes declare no scope, so the complete assertion is
		// projected and the route then declines the key it names.
		keyRefused := name == "billing http"
		t.Run(name+"/scoped", func(t *testing.T) {
			headers := apiKeyForwardedHeaders(user, org)
			headers.Set("X-Scopes", "users:read")
			ctx, err := admit(t, headers)
			if keyRefused {
				require.ErrorIs(t, err, errAPIKeyNotAccepted)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"users:read"}, scopesFromContext(ctx))
			require.Equal(t, credentialKindAPIKey, credentialKindFromContext(ctx))
		})
		t.Run(name+"/created without scopes", func(t *testing.T) {
			headers := apiKeyForwardedHeaders(user, org)
			headers["X-Scopes"] = []string{""}
			ctx, err := admit(t, headers)
			if keyRefused {
				require.ErrorIs(t, err, errAPIKeyNotAccepted)
				return
			}
			require.NoError(t, err)
			require.Empty(t, scopesFromContext(ctx))
			require.Equal(t, credentialKindAPIKey, credentialKindFromContext(ctx))
		})
		t.Run(name+"/session", func(t *testing.T) {
			headers := apiKeyForwardedHeaders(user, org)
			headers.Set("X-Credential-Kind", credentialKindSession)
			headers["X-Scopes"] = []string{""}
			ctx, err := admit(t, headers)
			require.NoError(t, err)
			require.Equal(t, credentialKindSession, credentialKindFromContext(ctx))
		})
	}
}

// The gateway stamps X-Credential-Kind and X-Scopes on every request it admits,
// so a trusted assertion missing either lost part of itself on the way. Without
// the kind, accounts cannot tell a key from a session and so cannot hold a key
// to its scopes; without the scope list, it cannot bound a key at all. Every
// transport refuses both, and a kind the gateway never stamps.
func TestForwardedIdentityWithoutCredentialKindOrScopesIsRefused(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	user := uuid.Must(uuid.NewV7()).String()
	org := uuid.Must(uuid.NewV7()).String()
	base := func() http.Header {
		return http.Header{
			"X-Codefly-Gateway-Token": {"test-gateway-token"},
			"X-User-Id":               {user},
			"X-Org-Id":                {org},
		}
	}
	cases := map[string]http.Header{}
	cases["no credential kind"] = base()
	cases["no credential kind"]["X-Scopes"] = []string{"users:read"}
	cases["a session without X-Scopes"] = base()
	cases["a session without X-Scopes"].Set("X-Credential-Kind", credentialKindSession)
	cases["neither header"] = base()
	cases["an unknown credential kind"] = base()
	cases["an unknown credential kind"].Set("X-Credential-Kind", "service_account")
	cases["an unknown credential kind"]["X-Scopes"] = []string{""}

	for transport, admit := range forwardedTransports() {
		for name, headers := range cases {
			t.Run(transport+"/"+name, func(t *testing.T) {
				_, err := admit(t, headers)
				require.Error(t, err)
				if transport == "grpc" {
					require.Equal(t, codes.PermissionDenied, status.Code(err))
				}
			})
		}
	}
}
