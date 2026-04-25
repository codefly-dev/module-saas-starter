package adapters

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"api/pkg/auth"

	"github.com/codefly-dev/core/wool"
)

// grpcAuthInterceptor mirrors connectAuthInterceptor for the raw gRPC
// path. Defense-in-depth: production has the auth-sidecar in front of
// every transport, but the api should NEVER trust upstream identity
// headers blindly. Even when the sidecar already validated the JWT, we
// re-verify the bearer here so a misconfigured sidecar / direct port
// access in dev / mistakenly-allowed traffic cannot bypass auth.
//
// Behaviour mirrors the Connect interceptor:
//   - Public methods (Authenticate, RefreshToken, Logout, GetJWKS) skip
//     bearer validation entirely.
//   - Missing / invalid bearer is permissive: the request is forwarded
//     to the handler, which calls requireAuth(ctx) and returns
//     Unauthenticated. Centralizing rejection at requireAuth keeps the
//     handler-level error consistent across paths.
//   - Valid bearer: stamp wool's UserID / UserAuthID / OrgID context
//     keys so downstream callerID() / callerOrg() see the same identity
//     they would see via X-User-Id headers from the sidecar.
//
// Sidecar interaction: when the sidecar IS in front, it strips the
// Authorization header and forwards X-User-Id / X-Org-Id / X-Auth-Id
// instead. wool.GRPC().Inject() (called by handlers) reads those into
// the same UserID / OrgID context keys. This interceptor's stamping
// then becomes a no-op overwrite with the same values — harmless.
func grpcAuthInterceptor(getMinter func() auth.JWTMinter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		minter := getMinter()
		if minter == nil {
			return handler(ctx, req)
		}
		if isPublicGrpcMethod(info.FullMethod) {
			return handler(ctx, req)
		}

		md, _ := metadata.FromIncomingContext(ctx)
		var token string
		if v := md.Get("authorization"); len(v) > 0 {
			if t, ok := strings.CutPrefix(v[0], "Bearer "); ok {
				token = t
			}
		}
		if token == "" {
			return handler(ctx, req)
		}

		identity, err := minter.VerifyAccess(token)
		if err != nil {
			wool.Get(ctx).In("grpcAuthInterceptor").
				Debug("VerifyAccess failed", wool.ErrField(err))
			return handler(ctx, req)
		}

		ctx = context.WithValue(ctx, wool.UserIDKey, identity.UserID.String())
		ctx = context.WithValue(ctx, wool.UserAuthIDKey, identity.UserID.String())
		zeroOrg := "00000000-0000-0000-0000-000000000000"
		if orgID := identity.OrgID.String(); orgID != "" && orgID != zeroOrg {
			ctx = context.WithValue(ctx, wool.OrgIDKey, orgID)
		}
		if identity.MFASatisfied {
			ctx = withMFASatisfied(ctx)
		}
		if v := md.Get("x-scopes"); len(v) > 0 && v[0] != "" {
			ctx = withScopes(ctx, parseScopes(v[0]))
		}
		return handler(ctx, req)
	}
}

// publicGrpcMethods are gRPC FullMethod paths that do not require auth.
// gRPC FullMethod format: /<package>.<service>/<method>.
var publicGrpcMethods = map[string]struct{}{
	"/customers.AuthService/BeginOAuth":   {},
	"/customers.AuthService/Authenticate": {},
	"/customers.AuthService/RefreshToken": {},
	"/customers.AuthService/Logout":       {},
	"/customers.AuthService/GetJWKS":      {},
}

func isPublicGrpcMethod(fullMethod string) bool {
	_, ok := publicGrpcMethods[fullMethod]
	return ok
}
