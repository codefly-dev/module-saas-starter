package adapters

import (
	"context"
	"net/http"
	"testing"

	"accounts/pkg/auth"

	"connectrpc.com/connect"
	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestPolicyAdmissionParity(t *testing.T) {
	previousToken := internalToken
	SetInternalToken("test-internal-token")
	t.Cleanup(func() { SetInternalToken(previousToken) })

	connectPolicy := &connectPolicyInterceptor{getMinter: nil}
	grpcPolicy := &grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}

	tests := []struct {
		name        string
		procedure   string
		headers     http.Header
		metadata    metadata.MD
		connectCode connect.Code
		grpcCode    codes.Code
	}{
		{name: "public", procedure: "/saas.accounts.v1.UserService/Version"},
		{name: "protected without identity", procedure: "/saas.accounts.v1.UserService/GetSelf", connectCode: connect.CodeUnauthenticated, grpcCode: codes.Unauthenticated},
		{name: "internal without credential", procedure: "/saas.accounts.v1.APIKeyService/ValidateAPIKey", connectCode: connect.CodePermissionDenied, grpcCode: codes.PermissionDenied},
		{name: "internal with credential remains private", procedure: "/saas.accounts.v1.APIKeyService/ValidateAPIKey", headers: http.Header{"X-Codefly-Internal-Token": []string{"test-internal-token"}}, metadata: metadata.Pairs("x-codefly-internal-token", "test-internal-token"), connectCode: connect.CodePermissionDenied, grpcCode: codes.PermissionDenied},
		{name: "unknown method", procedure: "/saas.accounts.v1.UserService/FutureUnclassifiedRPC", connectCode: connect.CodePermissionDenied, grpcCode: codes.PermissionDenied},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			headers := test.headers
			if headers == nil {
				headers = make(http.Header)
			}
			_, connectErr := connectPolicy.authorize(context.Background(), test.procedure, headers)
			if test.connectCode == 0 {
				require.NoError(t, connectErr)
			} else {
				require.Equal(t, test.connectCode, connect.CodeOf(connectErr))
			}

			ctx := metadata.NewIncomingContext(context.Background(), test.metadata)
			_, grpcErr := grpcPolicy.authorize(ctx, test.procedure)
			if test.grpcCode == codes.OK {
				require.NoError(t, grpcErr)
			} else {
				require.Equal(t, test.grpcCode, status.Code(grpcErr))
			}
		})
	}
}

func TestPolicyAdmissionRejectsSpoofedForwardedIdentity(t *testing.T) {
	connectPolicy := &connectPolicyInterceptor{getMinter: nil}
	headers := http.Header{"X-User-Id": []string{"spoofed"}}
	_, err := connectPolicy.authorize(context.Background(), "/saas.accounts.v1.UserService/GetSelf", headers)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.Empty(t, headers.Get("X-User-Id"))

	grpcPolicy := &grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-user-id", "spoofed",
		string(wool.UserAuthIDKey), "spoofed",
	))
	ctx, err = grpcPolicy.authorize(ctx, "/saas.accounts.v1.UserService/GetSelf")
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	md, _ := metadata.FromIncomingContext(ctx)
	require.Empty(t, md.Get("x-user-id"))
	require.Empty(t, md.Get(string(wool.UserAuthIDKey)))
}

func TestPolicyAdmissionInternalCredentialFailsClosedWhenUnconfigured(t *testing.T) {
	previousToken := internalToken
	SetInternalToken("")
	t.Cleanup(func() { SetInternalToken(previousToken) })

	policy := &grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureInternal}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "anything"))
	_, err := policy.authorize(ctx, "/saas.accounts.v1.PermissionService/CheckPermission")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestInternalListenerOnlyAdmitsInternalRPCWithCredential(t *testing.T) {
	previousToken := internalToken
	SetInternalToken("test-internal-token")
	t.Cleanup(func() { SetInternalToken(previousToken) })

	policy := &grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureInternal}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "test-internal-token"))
	_, err := policy.authorize(ctx, "/saas.accounts.v1.APIKeyService/ValidateAPIKey")
	require.NoError(t, err)

	_, err = policy.authorize(context.Background(), "/saas.accounts.v1.APIKeyService/ValidateAPIKey")
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	_, err = policy.authorize(ctx, "/saas.accounts.v1.UserService/GetSelf")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestInternalListenerAcceptsRotationTokenDuringOverlap(t *testing.T) {
	previousToken := internalToken
	previousRotation := rotationInternalTokens
	SetInternalToken("new-internal-token")
	SetInternalTokenRotation("previous-internal-token", "")
	t.Cleanup(func() {
		SetInternalToken(previousToken)
		rotationInternalTokens = previousRotation
	})

	policy := &grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureInternal}
	const method = "/saas.accounts.v1.APIKeyService/ValidateAPIKey"

	// Both the current and the still-valid previous credential are admitted.
	for _, token := range []string{"new-internal-token", "previous-internal-token"} {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", token))
		_, err := policy.authorize(ctx, method)
		require.NoError(t, err, "token %q should be accepted during overlap", token)
	}

	// A retired credential is refused.
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "retired-internal-token"))
	_, err := policy.authorize(ctx, method)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// Once rotation completes and the previous token is cleared, only the
	// current credential remains valid.
	SetInternalTokenRotation()
	ctx = metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "previous-internal-token"))
	_, err = policy.authorize(ctx, method)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestForwardedIdentityRequiresGatewayCredential(t *testing.T) {
	previousToken := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousToken) })

	connectPolicy := &connectPolicyInterceptor{getMinter: nil}
	headers := http.Header{
		"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
		"X-User-Id":               []string{"user-1"},
		"X-Org-Id":                []string{"org-1"},
	}
	ctx, err := connectPolicy.authorize(context.Background(), "/saas.accounts.v1.UserService/GetSelf", headers)
	require.NoError(t, err)
	userID, ok := wool.Get(ctx).UserID()
	require.True(t, ok)
	require.Equal(t, "user-1", userID)
	require.Empty(t, headers.Get("X-Codefly-Gateway-Token"))

	grpcPolicy := &grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}
	grpcCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-codefly-gateway-token", "test-gateway-token",
		"x-user-id", "user-1",
		"x-org-id", "org-1",
	))
	grpcCtx, err = grpcPolicy.authorize(grpcCtx, "/saas.accounts.v1.UserService/GetSelf")
	require.NoError(t, err)
	userID, ok = wool.Get(grpcCtx).UserID()
	require.True(t, ok)
	require.Equal(t, "user-1", userID)
}

func TestPublicOriginRequiresGatewayCredential(t *testing.T) {
	previousToken := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousToken) })

	connectPolicy := &connectPolicyInterceptor{getMinter: nil}
	headers := http.Header{
		"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
		publicOriginHeader:        []string{"http://localhost:54321"},
	}
	ctx, err := connectPolicy.authorize(context.Background(), "/saas.accounts.v1.AuthService/BeginOAuth", headers)
	require.NoError(t, err)
	origin, ok := auth.VerifiedPublicOrigin(ctx)
	require.True(t, ok)
	require.Equal(t, "http://localhost:54321", origin)
	require.Empty(t, headers.Get(publicOriginHeader))

	spoofed := http.Header{publicOriginHeader: []string{"https://evil.example"}}
	ctx, err = connectPolicy.authorize(context.Background(), "/saas.accounts.v1.AuthService/BeginOAuth", spoofed)
	require.NoError(t, err)
	_, ok = auth.VerifiedPublicOrigin(ctx)
	require.False(t, ok)

	grpcPolicy := &grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}
	grpcCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-codefly-gateway-token", "test-gateway-token",
		"x-codefly-public-origin", "http://localhost:54321",
	))
	grpcCtx, err = grpcPolicy.authorize(grpcCtx, "/saas.accounts.v1.AuthService/BeginOAuth")
	require.NoError(t, err)
	origin, ok = auth.VerifiedPublicOrigin(grpcCtx)
	require.True(t, ok)
	require.Equal(t, "http://localhost:54321", origin)
	grpcHeaders, ok := metadata.FromIncomingContext(grpcCtx)
	require.True(t, ok)
	require.Empty(t, grpcHeaders.Get("x-codefly-gateway-token"))
	require.Empty(t, grpcHeaders.Get("x-codefly-public-origin"))
}
