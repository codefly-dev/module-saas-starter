package adapters

import (
	"context"
	"errors"
	"net/http"

	"accounts/pkg/auth"
	"accounts/pkg/business"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
)

type sourceOperationContextKey struct{}

func sourceOperationProcedure(method string) bool {
	switch method {
	case business.SourceOperationMethod,
		"/saas.accounts.v1.DatasourceService/LookupInvokeSourceOperation",
		business.SourceReceiptRetentionMethod,
		"/saas.accounts.v1.DatasourceService/LookupPruneSourceOperationReceipts",
		business.ReconcileSourceMethod,
		"/saas.accounts.v1.DatasourceService/LookupReconcileSource":
		return true
	default:
		return false
	}
}

// The existing delegated-audience exchange issues this capability. Verification
// accepts only this owner's audience, then rechecks its live chain and revision.
// The module-capabilities identity token has no source scope and is never a call.
func authenticateSourceOperationContext(ctx context.Context, headers http.Header) (context.Context, error) {
	authority := WorkContextSingleton()
	if !authority.configured() {
		return ctx, connect.NewError(connect.CodeUnavailable, errors.New("source authority unavailable"))
	}
	values := headers.Values(workContextHeader)
	if len(values) != 1 {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("one source Work Context required"))
	}
	// Presented TO the source operation surface, so verified as its audience.
	verified, err := authority.verifyFor(ctx, values[0], business.SourceOperationAudience)
	if err != nil || verified.Context().GetTenantId() == "" || verified.Context().GetOwnerPrincipalId() == "" {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid source Work Context"))
	}
	claims := verified.Context()
	if claims.GetReplayPolicy() != workcontext.ReplayIdempotent {
		return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("idempotent source Work Context required"))
	}
	ctx = stampRequestIdentity(ctx, auth.OrdinaryRequestIdentity(claims.GetOwnerPrincipalId(), claims.GetTenantId()), auth.Assurance{})
	delegated, _, err := service.ConfirmSourceDelegationParent(ctx, claims.TenantId, claims.OwnerPrincipalId, claims.AuthorizationRevision, operationScopesFromCore(claims.AuthorityScopes), sourceDelegationHops(claims.ActorChain), claims.GetOperationBinding().GetBindingId())
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
	if !workContextGrants(claims, "datasource.sources", action, source) {
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
