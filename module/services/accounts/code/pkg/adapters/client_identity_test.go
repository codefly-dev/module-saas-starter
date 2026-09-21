package adapters

import (
	"context"
	"net/http"
	"testing"

	"accounts/pkg/auth"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const forwardedClientID = "example-console"

// The gateway resolves the client from the token's `azp` and forwards it. Both
// transports project it onto the same typed identity, so an audited call can
// name the client it was made through whichever way it arrived.
func TestForwardedClientIsProjectedAcrossTransports(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	connectCtx, err := (&connectPolicyInterceptor{getMinter: nil}).authorize(
		context.Background(), "/saas.accounts.v1.UserService/GetSelf", http.Header{
			"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
			"X-User-Id":               []string{supportActorID},
			"X-Org-Id":                []string{targetOrgID},
			"X-Client-Id":             []string{forwardedClientID},
		})
	require.NoError(t, err)

	grpcCtx, err := (&grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"x-codefly-gateway-token", "test-gateway-token",
			"x-user-id", supportActorID,
			"x-org-id", targetOrgID,
			"x-client-id", forwardedClientID,
		)), "/saas.accounts.v1.UserService/GetSelf")
	require.NoError(t, err)

	for name, ctx := range map[string]context.Context{
		"gateway to Connect": connectCtx,
		"gateway to gRPC":    grpcCtx,
	} {
		identity, ok := auth.VerifiedRequestIdentity(ctx)
		require.True(t, ok, name)
		require.Equal(t, forwardedClientID, identity.ClientID, name)
		require.Equal(t, supportActorID, identity.RealActorID(), name)
	}
}

// The host's own web session names no client, so the field stays empty rather
// than acquiring a placeholder that audit would have to interpret.
func TestSessionWithoutAClientProjectsNone(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	ctx, err := (&connectPolicyInterceptor{getMinter: nil}).authorize(
		context.Background(), "/saas.accounts.v1.UserService/GetSelf", http.Header{
			"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
			"X-User-Id":               []string{supportActorID},
			"X-Org-Id":                []string{targetOrgID},
		})
	require.NoError(t, err)

	identity, ok := auth.VerifiedRequestIdentity(ctx)
	require.True(t, ok)
	require.Empty(t, identity.ClientID)
}

// Without the gateway credential the header is caller-controlled, so it is
// stripped like every other forwarded identity field: a caller cannot attribute
// their own call to a client they are not using.
func TestForgedClientHeaderIsNotTrusted(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	minter := func() auth.JWTMinter {
		return &fixedAccessMinter{identity: &auth.Identity{
			UserID:    uuid.MustParse(supportActorID),
			SessionID: uuid.Must(uuid.NewV7()),
		}}
	}

	connectCtx, err := (&connectPolicyInterceptor{getMinter: minter}).authorize(
		context.Background(), "/saas.accounts.v1.UserService/GetSelf", http.Header{
			"Authorization": []string{"Bearer any"},
			"X-User-Id":     []string{supportActorID},
			"X-Client-Id":   []string{forwardedClientID},
		})
	require.NoError(t, err)
	identity, ok := auth.VerifiedRequestIdentity(connectCtx)
	require.True(t, ok)
	require.Empty(t, identity.ClientID, "a caller-injected client must not be recorded")

	grpcCtx, err := (&grpcPolicyAuthorizer{getMinter: minter, exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"authorization", "Bearer any",
			"x-user-id", supportActorID,
			"x-client-id", forwardedClientID,
		)), "/saas.accounts.v1.UserService/GetSelf")
	require.NoError(t, err)
	identity, ok = auth.VerifiedRequestIdentity(grpcCtx)
	require.True(t, ok)
	require.Empty(t, identity.ClientID)
}

// A trusted-but-unregistrable value is refused rather than dropped: admitting
// the request with an empty client would record a client call as a first-party
// one, in a table nothing can correct afterwards.
func TestMalformedForwardedClientIsRefused(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	_, err := (&connectPolicyInterceptor{getMinter: nil}).authorize(
		context.Background(), "/saas.accounts.v1.UserService/GetSelf", http.Header{
			"X-Codefly-Gateway-Token": []string{"test-gateway-token"},
			"X-User-Id":               []string{supportActorID},
			"X-Client-Id":             []string{"Not A Client Id"},
		})
	require.Error(t, err)

	_, err = (&grpcPolicyAuthorizer{getMinter: nil, exposure: rpcExposureTenant}).authorize(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"x-codefly-gateway-token", "test-gateway-token",
			"x-user-id", supportActorID,
			"x-client-id", "Not A Client Id",
		)), "/saas.accounts.v1.UserService/GetSelf")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
