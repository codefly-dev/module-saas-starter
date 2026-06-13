package adapters

import (
	"context"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"api/pkg/auth"

	"github.com/codefly-dev/core/wool"
)

// stripForwardedIdentity removes any caller-supplied identity metadata from the
// incoming context — both the dotted wool keys that wool.GRPC().Inject() reads
// (user.id, org.id, …) and the X-* header forms — so identity cannot be spoofed
// by a client connecting directly to the api. The authorization header is left
// intact so bearer verification still runs.
func stripForwardedIdentity(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	md = md.Copy()
	for _, k := range wool.ContextKeys {
		md.Delete(string(k))
	}
	for _, h := range []string{"x-user-id", "x-org-id", "x-roles", "x-auth-id", "x-user-email", "x-user-name"} {
		md.Delete(h)
	}
	return metadata.NewIncomingContext(ctx, md)
}

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
	// Secure by default: do NOT trust caller-supplied identity headers
	// (X-User-Id / user.id / …). They are only safe when the api is network-
	// isolated behind the auth-sidecar, which strips and re-injects them.
	// Operators of that topology opt in with API_TRUST_FORWARDED_IDENTITY=true.
	trustForwarded := os.Getenv("API_TRUST_FORWARDED_IDENTITY") == "true"
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		minter := getMinter()
		if minter == nil {
			return handler(ctx, req)
		}
		if isPublicGrpcMethod(info.FullMethod) {
			return handler(ctx, req)
		}

		// Strip forwarded identity so a direct connection (bypassing the
		// sidecar) cannot spoof identity via metadata that wool.GRPC().Inject()
		// in the handlers would otherwise trust. After this, identity can only
		// come from the verified bearer stamped below.
		if !trustForwarded {
			ctx = stripForwardedIdentity(ctx)
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
//
// Keep this list in sync with:
//   - connect_gen.go (Connect handlers that skip the auth interceptor)
//   - module.go moduleRPCs entries with HandlerAuthz="public"
//
// The two transports MUST agree, otherwise the same RPC behaves
// differently depending on whether the caller used HTTP/2 gRPC or
// Connect. The module_test.go capability test pins this contract.
var publicGrpcMethods = map[string]struct{}{
	// Auth — public by necessity (login flow can't carry a Bearer yet).
	"/customers.AuthService/BeginOAuth":   {},
	"/customers.AuthService/Authenticate": {},
	"/customers.AuthService/RefreshToken": {},
	"/customers.AuthService/Logout":       {},
	"/customers.AuthService/GetJWKS":      {},
	// Smoke / introspection — describes the public API surface, no
	// tenant data. IntrospectionService.GetServiceInfo / Version.
	"/customers.UserService/Version":                       {},
	"/customers.IntrospectionService/GetServiceInfo":       {},
	// CheckPermission is called by the auth-sidecar over a private
	// network boundary (mesh-internal). The wire is unauthenticated
	// today; production deployments should isolate it via mTLS or a
	// dedicated network namespace.
	"/customers.PermissionService/CheckPermission": {},
}

func isPublicGrpcMethod(fullMethod string) bool {
	_, ok := publicGrpcMethods[fullMethod]
	return ok
}
