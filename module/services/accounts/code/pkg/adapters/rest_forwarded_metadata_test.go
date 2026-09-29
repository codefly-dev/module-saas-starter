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

	"accounts/pkg/auth"
)

// grpc-gateway's default matcher forwards an inbound Grpc-Metadata-<name>
// header as gRPC metadata <name>. On the REST hop that would let a caller name
// x-user-id, x-org-id or any other identity field beside the one the gateway
// stamped — on a request that genuinely carries the gateway credential — and the
// interceptor would read whichever value came first. The REST matcher declines
// every prefixed spelling, whatever its case: identity and trust fields cross
// the hop only under their own names.
func TestRESTMatcherDeclinesGRPCMetadataPrefixedHeaders(t *testing.T) {
	names := append([]string{
		"Authorization", "X-Codefly-Gateway-Token", publicOriginHeader,
		"X-Codefly-Internal-Token", "X-Codefly-Work-Context", "X-Tenant-Hint",
	}, forwardedIdentityHeaders...)
	for _, name := range names {
		for _, spelling := range []string{
			"Grpc-Metadata-" + name,
			strings.ToLower("Grpc-Metadata-" + name),
			strings.ToUpper("Grpc-Metadata-" + name),
		} {
			key, ok := restIdentityHeaderMatcher(spelling)
			require.Falsef(t, ok, "%s must not become gRPC metadata, got %q", spelling, key)
		}
	}
}

// The same hole end to end: a request as the gateway forwards it (credential +
// stamped identity) plus a caller's Grpc-Metadata- spelling of every identity
// field and of the verified public origin, transcoded by a mux built the way
// rest_gen.go builds it, then admitted by the Connect interceptor it lands on.
// The identity accounts sees must be the stamped one, for every field.
func TestRESTHopIdentityCannotBeForgedThroughGRPCMetadataPrefix(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	stampedUser := uuid.Must(uuid.NewV7()).String()
	stampedOrg := uuid.Must(uuid.NewV7()).String()
	stampedSession := uuid.Must(uuid.NewV7()).String()
	forged := uuid.Must(uuid.NewV7()).String()

	mux := runtime.NewServeMux(
		runtime.WithMetadata(CustomHeaderToGRPCMetadataAnnotator),
		runtime.WithIncomingHeaderMatcher(restIdentityHeaderMatcher),
	)
	req := httptest.NewRequest(http.MethodGet, "/v1/users/self", nil)
	req.Header.Set("X-Codefly-Gateway-Token", "test-gateway-token")
	req.Header.Set(publicOriginHeader, "https://app.example.com")
	req.Header.Set("X-User-Id", stampedUser)
	req.Header.Set("X-Org-Id", stampedOrg)
	req.Header.Set("X-Session-Id", stampedSession)
	req.Header.Set("X-Credential-Kind", credentialKindSession)
	req.Header.Set("X-Scopes", "")
	for _, name := range append([]string{publicOriginHeader}, forwardedIdentityHeaders...) {
		req.Header.Set("Grpc-Metadata-"+name, forged)
	}

	ctx, err := runtime.AnnotateContext(context.Background(), mux, req, "/saas.accounts.v1.UserService/GetSelf")
	require.NoError(t, err)
	md, _ := metadata.FromOutgoingContext(ctx)

	require.Equal(t, []string{"test-gateway-token"}, md.Get("x-codefly-gateway-token"))
	require.Equal(t, []string{"https://app.example.com"}, md.Get("x-codefly-public-origin"))
	// A session's scope list is stamped empty, and the empty value must cross
	// the hop: accounts refuses a trusted assertion that arrives without it.
	stamped := map[string]string{
		"x-user-id": stampedUser, "x-org-id": stampedOrg, "x-session-id": stampedSession,
		"x-credential-kind": credentialKindSession, "x-scopes": "",
	}
	for _, name := range forwardedIdentityHeaders {
		key := strings.ToLower(name)
		if want, ok := stamped[key]; ok {
			require.Equalf(t, []string{want}, md.Get(key), "%s must carry only the stamped value", key)
			continue
		}
		require.Emptyf(t, md.Get(key), "%s was not stamped, so nothing may populate it", key)
	}

	// Metadata reaches the Connect handler as request headers.
	headers := http.Header{}
	for key, values := range md {
		for _, value := range values {
			headers.Add(key, value)
		}
	}
	admitted, err := (&connectPolicyInterceptor{}).authorize(context.Background(), "/saas.accounts.v1.UserService/GetSelf", headers)
	require.NoError(t, err)
	identity, ok := auth.VerifiedRequestIdentity(admitted)
	require.True(t, ok)
	require.Equal(t, stampedUser, identity.RealActorID())
	require.Equal(t, stampedUser, identity.EffectiveSubjectID(), "no impersonation was stamped")
	require.Equal(t, stampedOrg, identity.OrgID.String())
}

// A trusted forwarder asserts exactly one value per identity field. A second
// value got there by some other path, and reading either index would let that
// path choose the identity, so the request is refused on every transport rather
// than resolved.
func TestTrustedForwardedIdentityWithTwoValuesIsRefused(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	user := uuid.Must(uuid.NewV7()).String()
	org := uuid.Must(uuid.NewV7()).String()
	other := uuid.Must(uuid.NewV7()).String()

	for _, name := range append([]string{publicOriginHeader}, forwardedIdentityHeaders...) {
		t.Run(name, func(t *testing.T) {
			headers := http.Header{
				"X-Codefly-Gateway-Token": {"test-gateway-token"},
				"X-Credential-Kind":       {credentialKindSession},
				"X-Scopes":                {""},
				"X-User-Id":               {user},
				"X-Org-Id":                {org},
			}
			headers[http.CanonicalHeaderKey(name)] = []string{other, user}
			_, err := (&connectPolicyInterceptor{}).authorize(context.Background(), "/saas.accounts.v1.UserService/GetSelf", headers)
			require.Error(t, err)
			require.Contains(t, err.Error(), "ambiguous")

			md := metadata.Pairs("x-codefly-gateway-token", "test-gateway-token", "x-credential-kind", credentialKindSession,
				"x-scopes", "", "x-user-id", user, "x-org-id", org)
			key := strings.ToLower(name)
			md.Set(key, other, user)
			_, err = (&grpcPolicyAuthorizer{exposure: rpcExposureTenant}).authorize(
				metadata.NewIncomingContext(context.Background(), md), "/saas.accounts.v1.UserService/GetSelf")
			require.Equal(t, codes.PermissionDenied, status.Code(err))
		})
	}
}

// Two gateway credentials are not one trusted assertion: the forwarded
// identity is discarded and the caller must authenticate for itself.
func TestTwoGatewayCredentialsAreNotTrusted(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	user := uuid.Must(uuid.NewV7()).String()
	org := uuid.Must(uuid.NewV7()).String()

	_, err := (&connectPolicyInterceptor{}).authorize(context.Background(), "/saas.accounts.v1.UserService/GetSelf", http.Header{
		"X-Codefly-Gateway-Token": {"test-gateway-token", "caller-chosen"},
		"X-User-Id":               {user},
		"X-Org-Id":                {org},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "authentication required")

	_, err = (&grpcPolicyAuthorizer{exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"x-codefly-gateway-token", "test-gateway-token",
			"x-credential-kind", credentialKindSession,
			"x-scopes", "",
			"x-codefly-gateway-token", "caller-chosen",
			"x-user-id", user,
			"x-org-id", org,
		)), "/saas.accounts.v1.UserService/GetSelf")
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
