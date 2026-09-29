package adapters

import (
	"context"
	"net/http"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"connectrpc.com/connect"
	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestReflectionRPCsAreDeniedAsUnclassified(t *testing.T) {
	// gRPC server reflection is registered on the tenant server (grpc_gen.go),
	// but its methods are not in the RPC policy catalog, so the default-deny
	// authorizer rejects them before any handler runs — reflection leaks no
	// schema even though it is registered. A regression that classified or
	// exempted reflection would hand unauthenticated callers the full API
	// surface; this pins the deny.
	policy := &grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}
	for _, method := range []string{
		"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo",
		"/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo",
	} {
		_, err := policy.authorize(context.Background(), method)
		require.Error(t, err, method)
		require.Equal(t, codes.PermissionDenied, status.Code(err), method)
	}
}

func TestDirectJWTStampsVerifiedActorOnContext(t *testing.T) {
	chain := &auth.Actor{Subject: "svc:billing-worker", Act: &auth.Actor{Subject: "svc:gateway"}}
	minter := &fixedAccessMinter{identity: &auth.Identity{
		UserID:    uuid.Must(uuid.NewV7()),
		SessionID: uuid.Must(uuid.NewV7()),
		Actor:     chain,
	}}
	policy := &grpcPolicyAuthorizer{
		getMinter: func() auth.JWTMinter { return minter },
		exposure:  rpcExposureTenant,
	}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer any"))
	ctx, err := policy.authorize(ctx, "/saas.accounts.v1.UserService/GetSelf")
	require.NoError(t, err)

	// The actor chain survives to the handler context instead of being decoded
	// and discarded.
	got, ok := auth.VerifiedActorFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, chain, got)
}

func TestForwardedIdentityStampsVerifiedActorOnlyWhenGatewayTrusted(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	actor := metadata.Pairs(
		"x-codefly-gateway-token", "test-gateway-token",
		"x-credential-kind", credentialKindSession,
		"x-scopes", "",
		"x-user-id", uuid.Must(uuid.NewV7()).String(),
		"x-act", `{"sub":"svc:billing-worker","act":{"sub":"svc:gateway"}}`,
	)
	policy := &grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}
	ctx, err := policy.authorize(metadata.NewIncomingContext(context.Background(), actor), "/saas.accounts.v1.UserService/GetSelf")
	require.NoError(t, err)
	got, ok := auth.VerifiedActorFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "svc:billing-worker", got.Subject)
	require.Equal(t, "svc:gateway", got.Act.Subject)

	// Without the gateway credential, x-act is stripped as spoofable and never
	// becomes a verified actor.
	spoof := metadata.Pairs(
		"x-user-id", uuid.Must(uuid.NewV7()).String(),
		"x-act", `{"sub":"svc:attacker"}`,
		"authorization", "Bearer any",
	)
	spoofPolicy := &grpcPolicyAuthorizer{
		getMinter: func() auth.JWTMinter {
			return &fixedAccessMinter{identity: &auth.Identity{UserID: uuid.Must(uuid.NewV7()), SessionID: uuid.Must(uuid.NewV7())}}
		},
		exposure: rpcExposureTenant,
	}
	ctx, err = spoofPolicy.authorize(metadata.NewIncomingContext(context.Background(), spoof), "/saas.accounts.v1.UserService/GetSelf")
	require.NoError(t, err)
	_, ok = auth.VerifiedActorFromContext(ctx)
	require.False(t, ok, "a caller-injected x-act must not be trusted")
}

func TestPolicyAdmissionFailsClosedWhenRevocationUnavailable(t *testing.T) {
	const method = "/saas.accounts.v1.UserService/GetSelf"
	minter := func() auth.JWTMinter {
		return &fixedAccessMinter{verifyErr: auth.ErrRevocationUnavailable}
	}

	// A revocation-store outage is a retryable operator-side failure, not bad
	// credentials: the caller must be denied (fail-closed) with Unavailable so a
	// possibly-revoked token is never admitted.
	connectPolicy := &connectPolicyInterceptor{getMinter: minter}
	_, connectErr := connectPolicy.authorize(context.Background(), method, http.Header{"Authorization": []string{"Bearer any"}})
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(connectErr))

	grpcPolicy := &grpcPolicyAuthorizer{getMinter: minter, exposure: rpcExposureTenant}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer any"))
	_, grpcErr := grpcPolicy.authorize(ctx, method)
	require.Equal(t, codes.Unavailable, status.Code(grpcErr))
}

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

// The module authority endpoint is the internal tier narrowed to what a
// composed module may call: a module-surface method passes with the internal
// credential, a host-only internal method and a tenant method never do, and no
// method passes without the credential.
func TestModuleAuthorityListenerAdmitsOnlyTheModuleSurfaceWithCredential(t *testing.T) {
	previousToken := internalToken
	SetInternalToken("test-internal-token")
	t.Cleanup(func() { SetInternalToken(previousToken) })

	policy := &grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureModule}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "test-internal-token"))

	for _, method := range business.ModuleAuthorityProcedures() {
		_, err := policy.authorize(ctx, method)
		require.NoError(t, err, method)
		_, err = policy.authorize(context.Background(), method)
		require.Equal(t, codes.PermissionDenied, status.Code(err), method)
	}
	for _, method := range []string{
		"/saas.accounts.v1.ModuleCapabilitiesService/MintModuleWorkContext",
		"/saas.accounts.v1.APIKeyService/ValidateAPIKey",
		"/saas.accounts.v1.PermissionService/CheckPermission",
		"/saas.accounts.v1.UsageService/ConsumeUsage",
		"/saas.accounts.v1.UserService/GetSelf",
		// Internal-tier, and on the same service as the two admitted oracles,
		// but it authorizes on the shared perimeter credential alone and writes
		// a replay claim for the org the request names.
		"/saas.accounts.v1.WorkContextService/ConsumeSingleUse",
	} {
		_, err := policy.authorize(ctx, method)
		require.Equal(t, codes.PermissionDenied, status.Code(err), method)
	}
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

	const (
		forwardedUser = "019f6bf7-5b1c-730d-9687-fe6d4aff31ee"
		forwardedOrg  = "019f6bf7-5b4b-74e5-8c17-092259bb1663"
	)

	connectPolicy := &connectPolicyInterceptor{getMinter: nil}
	headers := http.Header{
		"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
		"X-Credential-Kind":       []string{credentialKindSession},
		"X-Scopes":                []string{""},
		"X-User-Id":               []string{forwardedUser},
		"X-Org-Id":                []string{forwardedOrg},
	}
	ctx, err := connectPolicy.authorize(context.Background(), "/saas.accounts.v1.UserService/GetSelf", headers)
	require.NoError(t, err)
	userID, ok := wool.Get(ctx).UserID()
	require.True(t, ok)
	require.Equal(t, forwardedUser, userID)
	require.Empty(t, headers.Get("X-Codefly-Gateway-Token"))

	grpcPolicy := &grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}
	grpcCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-codefly-gateway-token", "test-gateway-token",
		"x-credential-kind", credentialKindSession,
		"x-scopes", "",
		"x-user-id", forwardedUser,
		"x-org-id", forwardedOrg,
	))
	grpcCtx, err = grpcPolicy.authorize(grpcCtx, "/saas.accounts.v1.UserService/GetSelf")
	require.NoError(t, err)
	userID, ok = wool.Get(grpcCtx).UserID()
	require.True(t, ok)
	require.Equal(t, forwardedUser, userID)
}

func TestPublicOriginRequiresGatewayCredential(t *testing.T) {
	previousToken := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousToken) })

	connectPolicy := &connectPolicyInterceptor{getMinter: nil}
	headers := http.Header{
		"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
		"X-Credential-Kind":       []string{credentialKindSession},
		"X-Scopes":                []string{""},
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
		"x-credential-kind", credentialKindSession,
		"x-scopes", "",
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

// withGatewayRotation installs a current and a previous gateway credential
// with the previous one's expiry, restoring the package state afterwards.
func withGatewayRotation(t *testing.T, current, previous string, previousExpiresAt time.Time) {
	t.Helper()
	savedCurrent, savedPrevious, savedExpiresAt := gatewayToken, previousGatewayToken, previousGatewayTokenExpiresAt
	t.Cleanup(func() {
		SetGatewayToken(savedCurrent)
		SetPreviousGatewayToken(savedPrevious)
		SetPreviousGatewayTokenExpiresAt(savedExpiresAt)
	})
	SetGatewayToken(current)
	SetPreviousGatewayToken(previous)
	SetPreviousGatewayTokenExpiresAt(previousExpiresAt)
}

// forwardedIdentityTrusted reports whether a Connect request stamped with token
// and a forwarded user id reaches the handler as that user.
func forwardedIdentityTrusted(t *testing.T, token string) (bool, error) {
	t.Helper()
	const forwardedUser = "019f6bf7-5b1c-730d-9687-fe6d4aff31ee"
	ctx, err := (&connectPolicyInterceptor{}).authorize(context.Background(), "/saas.accounts.v1.UserService/GetSelf", http.Header{
		"X-Codefly-Gateway-Token": {token},
		"X-Credential-Kind":       {credentialKindSession},
		"X-Scopes":                {""},
		"X-User-Id":               {forwardedUser},
	})
	if err != nil {
		return false, err
	}
	userID, ok := wool.Get(ctx).UserID()
	return ok && userID == forwardedUser, nil
}

// Rotating the gateway credential rolls auth-gateway onto a new value while
// accounts already holds it as current and the old one as previous; forwarded
// identity from a gateway pod still stamping the old value is trusted until the
// previous slot is emptied, and never after.
func TestPreviousGatewayTokenIsTrustedOnlyDuringRotation(t *testing.T) {
	withGatewayRotation(t, "incoming-gateway-token", "outgoing-gateway-token", time.Now().Add(time.Hour))
	forwarded := func(token string) (bool, error) { return forwardedIdentityTrusted(t, token) }

	for _, token := range []string{"incoming-gateway-token", "outgoing-gateway-token"} {
		trusted, err := forwarded(token)
		require.NoError(t, err)
		require.True(t, trusted, "%s must be trusted mid-rotation", token)
	}
	require.False(t, validGatewayToken("some-other-token"))
	require.False(t, validGatewayToken(""))

	SetPreviousGatewayToken("")
	require.True(t, validGatewayToken("incoming-gateway-token"))
	require.False(t, validGatewayToken("outgoing-gateway-token"), "a retired credential proves nothing")
	_, err := forwarded("outgoing-gateway-token")
	require.Error(t, err, "without trust the forwarded identity is stripped and no bearer was presented")
}

// A rotated-out gateway credential loses its authority at its expiry even when
// nobody empties the previous slot; the current credential is unaffected.
func TestPreviousGatewayTokenStopsMatchingAtItsExpiry(t *testing.T) {
	expiresAt := time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)
	withGatewayRotation(t, "incoming-gateway-token", "outgoing-gateway-token", expiresAt)

	require.True(t, validGatewayTokenAt("outgoing-gateway-token", expiresAt.Add(-time.Hour)))
	require.True(t, validGatewayTokenAt("outgoing-gateway-token", expiresAt.Add(-time.Nanosecond)))
	require.False(t, validGatewayTokenAt("outgoing-gateway-token", expiresAt), "the window is open strictly before its expiry")
	require.False(t, validGatewayTokenAt("outgoing-gateway-token", expiresAt.Add(time.Hour)))
	require.True(t, validGatewayTokenAt("incoming-gateway-token", expiresAt.Add(time.Hour)), "the current credential has no expiry")
	require.False(t, validGatewayTokenAt("some-other-token", expiresAt.Add(-time.Hour)))

	SetPreviousGatewayTokenExpiresAt(time.Time{})
	require.False(t, validGatewayTokenAt("outgoing-gateway-token", expiresAt.Add(-time.Hour)),
		"a previous credential installed without an expiry proves nothing")
}

// The request path reads the real clock: a previous credential whose expiry has
// passed no longer carries forwarded identity through the interceptor.
func TestExpiredPreviousGatewayTokenCarriesNoForwardedIdentity(t *testing.T) {
	withGatewayRotation(t, "incoming-gateway-token", "outgoing-gateway-token", time.Now().Add(-time.Second))

	trusted, err := forwardedIdentityTrusted(t, "incoming-gateway-token")
	require.NoError(t, err)
	require.True(t, trusted)
	require.False(t, validGatewayToken("outgoing-gateway-token"))
	_, err = forwardedIdentityTrusted(t, "outgoing-gateway-token")
	require.Error(t, err, "without trust the forwarded identity is stripped and no bearer was presented")
}
