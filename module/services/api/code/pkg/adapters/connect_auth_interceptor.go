package adapters

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"api/pkg/auth"

	"github.com/codefly-dev/core/wool"
)

// connectAuthInterceptor validates `Authorization: Bearer <jwt>` on inbound
// Connect requests and stamps the wool context keys (UserAuthID, UserID,
// OrgID, Roles) that downstream `callerID` / `callerOrg` already read.
//
// In the production architecture, an upstream auth-sidecar handles JWT
// validation and forwards the user identity as X-User-Id / X-Org-Id /
// X-Roles headers. The frontend service in saas-starter does NOT depend
// on the sidecar though — it talks directly to the api's Connect server —
// so without this interceptor, every authenticated RPC fails with
// "caller identity not found".
//
// Endpoints that pre-date authentication (Authenticate / Refresh / Logout
// / JWKS) bypass the check by skipping requests whose procedure path is
// listed in publicProcedures.
//
// The interceptor is deliberately permissive on missing tokens: it does
// NOT reject, just does nothing. Authorization is enforced at the handler
// level via callerID(ctx) — the same enforcement point used by the
// gRPC path. That keeps the request-rejection logic in one place.
func connectAuthInterceptor(getMinter func() auth.JWTMinter) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			// Late-bind: business.Service is wired AFTER the Connect
			// server starts (see connect_gen.go), so we resolve the
			// minter per request instead of capturing at construction.
			minter := getMinter()
			// No minter wired: treat the same as no auth-sidecar present
			// in dev with an unauthenticated stack. Hand off untouched.
			if minter == nil {
				return next(ctx, req)
			}

			// Skip the auth flow itself — clients aren't authenticated yet.
			if isPublicProcedure(req.Spec().Procedure) {
				return next(ctx, req)
			}

			authz := req.Header().Get("Authorization")
			token, ok := strings.CutPrefix(authz, "Bearer ")
			if !ok || token == "" {
				return next(ctx, req)
			}

			identity, err := minter.VerifyAccess(token)
			if err != nil {
				// Bad token — let downstream callerID() return Unauthenticated
				// rather than masking the cause here. Logging at debug so the
				// dev console isn't spammed by background polling that fires
				// before the page has tokens.
				wool.Get(ctx).In("connectAuthInterceptor").
					Debug("VerifyAccess failed", wool.ErrField(err))
				return next(ctx, req)
			}

			// Stamp the keys callerID() / callerOrg() / wool.UserAuthID()
			// pull from. wool.With* mutates the Wool's *internal* context
			// — to make those values visible to downstream handlers we
			// must extract the updated context and pass it on. Calling
			// next(ctx, req) with the original ctx silently drops them.
			ctx = context.WithValue(ctx, wool.UserIDKey, identity.UserID.String())
			ctx = context.WithValue(ctx, wool.UserAuthIDKey, identity.UserID.String())
			zeroOrg := "00000000-0000-0000-0000-000000000000"
			if orgID := identity.OrgID.String(); orgID != "" && orgID != zeroOrg {
				ctx = context.WithValue(ctx, wool.OrgIDKey, orgID)
			}
			if identity.MFASatisfied {
				ctx = withMFASatisfied(ctx)
			}
			// Forward API-key scopes from the sidecar. Empty when the
			// caller is on the JWT path (interactive session); present
			// only for API-key auth, where requireScope enforces it.
			if raw := req.Header().Get("X-Scopes"); raw != "" {
				ctx = withScopes(ctx, parseScopes(raw))
			}
			// Roles aren't a first-class wool field — wool exposes Roles()
			// reader but no setter. Handlers that care (callerOrgRole etc.)
			// either re-validate the JWT or read OrgID + DB lookup.
			_ = identity.PlatformRole
			_ = identity.OrgRole

			return next(ctx, req)
		}
	}
}

// publicProcedures are Connect procedure paths that do NOT require auth.
// The proto package is "customers" (see genconnect.AuthServiceName).
// Procedure path format: /<package>.<service>/<method>.
var publicProcedures = map[string]struct{}{
	"/customers.AuthService/BeginOAuth":   {},
	"/customers.AuthService/Authenticate": {},
	"/customers.AuthService/RefreshToken": {},
	"/customers.AuthService/Logout":       {},
	"/customers.AuthService/GetJWKS":      {},
}

func isPublicProcedure(procedure string) bool {
	_, ok := publicProcedures[procedure]
	return ok
}
