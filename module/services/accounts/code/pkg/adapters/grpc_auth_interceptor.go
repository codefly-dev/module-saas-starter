package adapters

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
)

// stripForwardedIdentity removes caller-controlled identity metadata. The
// authorization and internal-credential fields are retained for verification.
func stripForwardedIdentity(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	md = md.Copy()
	for _, key := range wool.ContextKeys {
		md.Delete(string(key))
	}
	for _, header := range forwardedIdentityHeaders {
		md.Delete(strings.ToLower(header))
	}
	return metadata.NewIncomingContext(ctx, md)
}

type grpcPolicyAuthorizer struct {
	getMinter func() auth.JWTMinter
	exposure  rpcExposure
}

type rpcExposure uint8

const (
	rpcExposureTenant rpcExposure = iota
	rpcExposureInternal
)

func newGRPCPolicyAuthorizer(getMinter func() auth.JWTMinter, exposure rpcExposure) *grpcPolicyAuthorizer {
	return &grpcPolicyAuthorizer{
		getMinter: getMinter,
		exposure:  exposure,
	}
}

func grpcAuthInterceptor(getMinter func() auth.JWTMinter, exposure rpcExposure) grpc.UnaryServerInterceptor {
	authorizer := newGRPCPolicyAuthorizer(getMinter, exposure)
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := authorizer.authorize(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func grpcStreamAuthInterceptor(getMinter func() auth.JWTMinter, exposure rpcExposure) grpc.StreamServerInterceptor {
	authorizer := newGRPCPolicyAuthorizer(getMinter, exposure)
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := authorizer.authorize(stream.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		return handler(srv, &contextServerStream{ServerStream: stream, ctx: ctx})
	}
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (stream *contextServerStream) Context() context.Context { return stream.ctx }

func (i *grpcPolicyAuthorizer) authorize(ctx context.Context, fullMethod string) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	trustedForwarded := validGatewayToken(firstMetadataValue(md, "x-codefly-gateway-token"))
	forwardedPublicOrigin := firstMetadataValue(md, "x-codefly-public-origin")
	if !trustedForwarded {
		ctx = stripForwardedIdentity(ctx)
		md, _ = metadata.FromIncomingContext(ctx)
	}
	if trustedForwarded && forwardedPublicOrigin != "" {
		var err error
		ctx, err = auth.WithVerifiedPublicOrigin(ctx, forwardedPublicOrigin)
		if err != nil {
			return ctx, status.Error(codes.PermissionDenied, "invalid trusted public origin")
		}
	}
	// Gateway credentials prove trust at this adapter boundary; application
	// handlers must not receive or reuse the raw capabilities.
	md = md.Copy()
	md.Delete("x-codefly-gateway-token")
	md.Delete("x-codefly-public-origin")
	ctx = metadata.NewIncomingContext(ctx, md)
	policy, classified := business.LookupRPCPolicy(fullMethod)
	if !classified {
		return ctx, status.Error(codes.PermissionDenied, "RPC is not classified by the authorization policy")
	}
	if i.exposure == rpcExposureInternal {
		if policy.Tier != business.RPCPolicyInternal {
			return ctx, status.Error(codes.PermissionDenied, "tenant RPC is not exposed on the internal listener")
		}
		if !validInternalToken(firstMetadataValue(md, "x-codefly-internal-token")) {
			return ctx, status.Error(codes.PermissionDenied, "internal service credential required")
		}
		return ctx, nil
	}

	if policy.Tier == business.RPCPolicyInternal {
		return ctx, status.Error(codes.PermissionDenied, "internal RPC is not exposed on the tenant listener")
	}
	if policy.Tier == business.RPCPolicyPublic {
		return ctx, nil
	}

	if trustedForwarded && hasForwardedIdentity(md) {
		return stampForwardedGRPCIdentity(ctx, md), nil
	}
	var minter auth.JWTMinter
	if i.getMinter != nil {
		minter = i.getMinter()
	}
	if minter == nil {
		return ctx, status.Error(codes.Unauthenticated, "authentication required")
	}
	var token string
	if values := md.Get("authorization"); len(values) > 0 {
		token, _ = strings.CutPrefix(values[0], "Bearer ")
	}
	if token == "" {
		return ctx, status.Error(codes.Unauthenticated, "authentication required")
	}
	identity, err := minter.VerifyAccess(token)
	if err != nil {
		wool.Get(ctx).In("grpcPolicyInterceptor").Debug("VerifyAccess failed", wool.ErrField(err))
		return ctx, status.Error(codes.Unauthenticated, "invalid or expired access token")
	}
	ctx = stampVerifiedIdentity(ctx, identity.UserID.String(), identity.OrgID.String(), identity.Assurance())
	ctx = auth.WithVerifiedSessionID(ctx, identity.SessionID)
	ctx = withScopedRoles(ctx, identity.ScopedRoles)
	ctx = withScopedRolesTruncated(ctx, identity.ScopedRolesTruncated)
	if values := md.Get("x-scopes"); len(values) > 0 && values[0] != "" {
		ctx = withScopes(ctx, parseScopes(values[0]))
	}
	return ctx, nil
}

func firstMetadataValue(md metadata.MD, key string) string {
	if values := md.Get(key); len(values) > 0 {
		return values[0]
	}
	return ""
}

func stampForwardedGRPCIdentity(ctx context.Context, md metadata.MD) context.Context {
	ctx = stampVerifiedIdentity(ctx, firstMetadataValue(md, "x-user-id"), firstMetadataValue(md, "x-org-id"), assuranceFromTransport(
		firstMetadataValue(md, "x-authentication-methods"),
		firstMetadataValue(md, "x-auth-time"),
		firstMetadataValue(md, "x-assurance-level"),
		firstMetadataValue(md, "x-mfa-verified-at"),
	))
	if scopes := firstMetadataValue(md, "x-scopes"); scopes != "" {
		ctx = withScopes(ctx, parseScopes(scopes))
	}
	if scopedRoles := firstMetadataValue(md, "x-scoped-roles"); scopedRoles != "" {
		ctx = withScopedRoles(ctx, parseScopedRoles(scopedRoles))
	}
	ctx = withScopedRolesTruncated(ctx, firstMetadataValue(md, "x-scoped-roles-truncated") == "true")
	return auth.WithVerifiedSessionIDString(ctx, firstMetadataValue(md, "x-session-id"))
}

func stampVerifiedIdentity(ctx context.Context, userID, orgID string, assurance auth.Assurance) context.Context {
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, userID, orgID)
	ctx = context.WithValue(ctx, wool.UserIDKey, userID)
	ctx = context.WithValue(ctx, wool.UserAuthIDKey, userID)
	if orgID != "" && orgID != "00000000-0000-0000-0000-000000000000" {
		ctx = context.WithValue(ctx, wool.OrgIDKey, orgID)
	}
	return auth.WithAssurance(ctx, assurance)
}

func hasForwardedIdentity(md metadata.MD) bool {
	if len(md.Get("x-user-id")) > 0 && md.Get("x-user-id")[0] != "" {
		return true
	}
	for _, key := range wool.ContextKeys {
		if string(key) == string(wool.UserIDKey) || string(key) == string(wool.UserAuthIDKey) {
			if values := md.Get(string(key)); len(values) > 0 && values[0] != "" {
				return true
			}
		}
	}
	return false
}

func validInternalToken(candidate string) bool {
	if internalToken == "" || candidate == "" || len(candidate) != len(internalToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(internalToken)) == 1
}

func validGatewayToken(candidate string) bool {
	if gatewayToken == "" || candidate == "" || len(candidate) != len(gatewayToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(gatewayToken)) == 1
}
