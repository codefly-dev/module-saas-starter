package adapters

import (
	"context"
	"errors"
	"net/http"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
)

type sourceOperationContextKey struct{}

func sourceOperationProcedure(method string) bool {
	return method == business.SourceOperationMethod || method == "/saas.accounts.v1.DatasourceService/LookupInvokeSourceOperation"
}

// The existing delegated-audience exchange issues this capability. Verification
// accepts only this owner's audience, then rechecks its live chain and revision.
// The module-capabilities identity token has no source scope and is never a call.
func authenticateSourceOperationContext(ctx context.Context, headers http.Header) (context.Context, error) {
	authority := WorkContextSingleton()
	if authority == nil || authority.verifier == nil || authority.configureErr != nil {
		return ctx, connect.NewError(connect.CodeUnavailable, errors.New("source authority unavailable"))
	}
	values := headers.Values(workcontext.WorkContextHeaderName)
	if len(values) != 1 {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("one source Work Context required"))
	}
	token, err := workcontext.ParseWorkContextToken(values[0])
	if err != nil {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid source Work Context"))
	}
	claims, err := authority.verifier.Verify(token, workcontext.WorkContextExpectations{Issuer: authority.issuer, Audience: business.SourceOperationAudience})
	if err != nil || claims.GetTenantId() == "" || claims.GetOwnerPrincipalId() == "" {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid source Work Context"))
	}
	if claims.GetReplayPolicy() != workcontext.WorkContextReplayIdempotent {
		return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("idempotent source Work Context required"))
	}
	ctx = stampRequestIdentity(ctx, auth.OrdinaryRequestIdentity(claims.GetOwnerPrincipalId(), claims.GetTenantId()), auth.Assurance{})
	delegated, _, err := service.ConfirmSourceDelegationParent(ctx, claims.TenantId, claims.OwnerPrincipalId, claims.AuthorizationRevision, operationScopesFromCore(claims.AuthorityScopes), sourceDelegationHops(claims.ActorChain))
	if err != nil {
		return ctx, translateGRPCError(sourceDelegationParentError(err))
	}
	if !delegated {
		if len(claims.ActorChain) > 0 && authority.journal == nil {
			return ctx, connect.NewError(connect.CodeUnavailable, errors.New("delegation journal unavailable"))
		}
		if _, err = authority.requireCurrentAuthority(ctx, claims.GetTenantId(), claims); err != nil {
			return ctx, translateGRPCError(err)
		}
	}
	return context.WithValue(ctx, sourceOperationContextKey{}, claims), nil
}

func requireSourceOperationScope(ctx context.Context, org, source, action string) error {
	claims, ok := ctx.Value(sourceOperationContextKey{}).(*basev0.WorkContextV1)
	if !ok {
		return nil
	} // Interactive identity still passes the host RBAC gate.
	if claims.GetTenantId() != org {
		return connect.NewError(connect.CodePermissionDenied, errors.New("source tenant mismatch"))
	}
	if err := workcontext.RequireWorkContextScope(claims, workcontext.WorkContextScopeRequirement{ResourceKind: "datasource.sources", Action: action, ResourceID: source}); err != nil {
		return connect.NewError(connect.CodePermissionDenied, errors.New("source scope required"))
	}
	// An unconstrained kind is not an installation-selected source slot.
	scopes := claims.GetAuthorityScopes()
	if chain := claims.GetActorChain(); len(chain) > 0 {
		scopes = chain[len(chain)-1].GetGrantedScopes()
	}
	for _, scope := range scopes {
		if scope.GetResourceKind() == "datasource.sources" && len(scope.GetResourceIds()) == 0 {
			return connect.NewError(connect.CodePermissionDenied, errors.New("explicit source scope required"))
		}
	}
	return nil
}
