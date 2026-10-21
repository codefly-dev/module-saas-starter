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

// sourceOperationDelegationKey carries the SOURCE of the delegation a child
// capability was confirmed against, when one was.
//
// It exists because the capability's own scopes cannot answer "which source did
// this authority come from". A mint seals the binding's declared
// source_delegation_scopes VERBATIM (source_delegation.go), and that list is
// per-BINDING: one scope may name up to 64 resource ids. So a child minted from
// source A's delegation legitimately carries a scope naming A, B and C, and a
// scope check alone admits it on all three — under A's delegator's authority,
// with B's and C's delegations never consulted and their revocation unable to
// stop it. The delegation row is the only thing that knows which source the
// authority actually came from, and ConfirmSourceDelegationParent already
// returns it.
type sourceOperationDelegationKey struct{}

func sourceOperationProcedure(method string) bool {
	switch method {
	case business.SourceOperationMethod,
		"/saas.accounts.v1.DatasourceService/ListSourceOperations",
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
	delegated, delegation, err := service.ConfirmSourceDelegationParent(ctx, claims.TenantId, claims.OwnerPrincipalId, claims.AuthorizationRevision, operationScopesFromCore(claims.AuthorityScopes), sourceDelegationHops(claims.ActorChain), claims.GetOperationBinding().GetBindingId())
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
	ctx = context.WithValue(ctx, sourceOperationContextKey{}, claims)
	// Kept, not discarded: the source this authority actually came from. Only
	// the delegated shape has one — an ordinary capability is confirmed by
	// requireCurrentAuthority above and borrows nobody's source.
	if delegated && delegation != nil && delegation.SourceID != "" {
		ctx = context.WithValue(ctx, sourceOperationDelegationKey{}, delegation.SourceID)
	}
	return ctx, nil
}

// requireDelegatedSourceMatch refuses a child capability presented for a source
// OTHER than the one whose delegation was confirmed for it.
//
// It is a separate check from requireSourceOperationScope and neither implies
// the other. The scope check asks "may this capability act on this source at
// all", against a per-binding list a composition declared. This asks "is this
// source the one whose owner's authority the capability is carrying" — and the
// answer has to come from the delegation row, because the scope list cannot
// distinguish the delegation's own source from the other sources the same
// binding happens to be scoped to.
//
// Without it, a module holding a child from source A's delegation reconciles
// every other source the binding's scope names, under A's delegator, and
// revoking those sources' delegations does not stop it — the mint never read
// them. Reported as N2 on PR #1115.
//
// An interactive identity, and an ordinary (non-delegated) capability, carry no
// delegation and pass through: there is no borrowed authority to bind.
func requireDelegatedSourceMatch(ctx context.Context, source string) error {
	delegated, ok := ctx.Value(sourceOperationDelegationKey{}).(string)
	if !ok || delegated == "" {
		return nil
	}
	if delegated != source {
		return connect.NewError(connect.CodePermissionDenied,
			errors.New("source delegation does not cover this source"))
	}
	return nil
}

func requireSourceOperationScope(ctx context.Context, org, source, action string) error {
	claims, ok := ctx.Value(sourceOperationContextKey{}).(*basev0.WorkContextV1)
	if !ok {
		return nil
	} // Interactive identity still passes the host RBAC gate.
	if claims.GetTenantId() != org {
		return connect.NewError(connect.CodePermissionDenied, errors.New("source tenant mismatch"))
	}
	// Before the declared scopes: a borrowed authority binds to the source it
	// was borrowed FROM, which the scope list cannot say (N2 on PR #1115).
	if err := requireDelegatedSourceMatch(ctx, source); err != nil {
		return err
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
