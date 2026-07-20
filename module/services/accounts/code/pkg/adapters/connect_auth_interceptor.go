package adapters

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/codefly-dev/core/wool"
)

// forwardedIdentityHeaders are caller-controlled identity headers. Unless the
// API is explicitly configured behind the trusted auth sidecar, remove them
// before policy admission and handler execution.
var forwardedIdentityHeaders = []string{
	"X-User-Id", "X-Org-Id", "X-Org-Role", "X-Platform-Role", "X-Roles",
	"X-Auth-Id", "X-User-Email", "X-User-Name", "X-Session-Id",
	"X-Acting-As-User-Id", "X-Scopes", "X-MFA-Satisfied",
	"X-Authentication-Methods", "X-Auth-Time", "X-Assurance-Level", "X-MFA-Verified-At",
}

type connectPolicyInterceptor struct {
	getMinter func() auth.JWTMinter
}

// connectAuthInterceptor enforces the descriptor-derived policy for unary and
// streaming Connect handlers. Unknown methods are denied, public methods pass,
// internal methods are denied because Connect is tenant-facing, and every
// other tier requires a verified user identity.
func connectAuthInterceptor(getMinter func() auth.JWTMinter) connect.Interceptor {
	return &connectPolicyInterceptor{
		getMinter: getMinter,
	}
}

func (i *connectPolicyInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := i.authorize(ctx, req.Spec().Procedure, req.Header())
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (i *connectPolicyInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *connectPolicyInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := i.authorize(ctx, conn.Spec().Procedure, conn.RequestHeader())
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (i *connectPolicyInterceptor) authorize(ctx context.Context, procedure string, headers http.Header) (context.Context, error) {
	trustedForwarded := validGatewayToken(headers.Get("X-Codefly-Gateway-Token"))
	if !trustedForwarded {
		for _, header := range forwardedIdentityHeaders {
			headers.Del(header)
		}
	}
	headers.Del("X-Codefly-Gateway-Token")

	policy, classified := business.LookupRPCPolicy(procedure)
	if !classified {
		return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("RPC is not classified by the authorization policy"))
	}
	if policy.Tier == business.RPCPolicyPublic {
		return ctx, nil
	}
	if policy.Tier == business.RPCPolicyInternal {
		return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("internal RPC is not exposed on the tenant listener"))
	}

	if trustedForwarded && headers.Get("X-User-Id") != "" {
		return stampForwardedHTTPIdentity(ctx, headers), nil
	}
	var minter auth.JWTMinter
	if i.getMinter != nil {
		minter = i.getMinter()
	}
	if minter == nil {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	authz := headers.Get("Authorization")
	token, ok := strings.CutPrefix(authz, "Bearer ")
	if !ok || token == "" {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	identity, err := minter.VerifyAccess(token)
	if err != nil {
		wool.Get(ctx).In("connectPolicyInterceptor").Debug("VerifyAccess failed", wool.ErrField(err))
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired access token"))
	}

	ctx = stampVerifiedIdentity(ctx, identity.UserID.String(), identity.OrgID.String(), identity.Assurance())
	return auth.WithVerifiedSessionID(ctx, identity.SessionID), nil
}

func stampForwardedHTTPIdentity(ctx context.Context, headers http.Header) context.Context {
	ctx = stampVerifiedIdentity(ctx, headers.Get("X-User-Id"), headers.Get("X-Org-Id"), assuranceFromTransport(
		headers.Get("X-Authentication-Methods"),
		headers.Get("X-Auth-Time"),
		headers.Get("X-Assurance-Level"),
		headers.Get("X-MFA-Verified-At"),
	))
	if scopes := headers.Get("X-Scopes"); scopes != "" {
		ctx = withScopes(ctx, parseScopes(scopes))
	}
	return auth.WithVerifiedSessionIDString(ctx, headers.Get("X-Session-Id"))
}
