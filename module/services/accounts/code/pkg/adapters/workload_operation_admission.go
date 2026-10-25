package adapters

import (
	"context"
	"errors"
	"slices"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The issuer's half of the gateway's bearer-less module arm.
//
// A request at /v1/<alias>/* with no bearer is admitted only for a module
// operation context: the capability StartModuleOperationTask mints for a
// composed module acting as itself, with no person present. The gateway cannot
// tell that shape from a person's capability. Core's Inspector, all a hop runs,
// withholds the owner, its kind, the chain and the binding, and several
// person-owned capabilities are sealed to an installation that is not their
// audience's (a source-operation context, an installation task, an installed
// binding's child). So the gateway asks the issuer, which reads its own claims.
//
// This is an ALLOW-LIST of one shape. Every other capability this issuer mints
// is refused, and a new mint is refused until it is admitted here on purpose;
// TestEveryMintHasAWorkloadOperationVerdict holds every mint to a verdict and
// TestWorkloadOperationTableNamesEveryMintAndOwnerKind fails when a mint or an
// owner kind appears that the table does not name.
//
// It consumes nothing. The capability is READ BACK (core's Authority.ReadBack
// through readBackFor): signature under this issuer, window, the alias as
// audience, and the live revision, seal and epochs, exactly as a verifier holds
// them, without the replay store. The callee remains the one party that
// verifies the capability as presented and spends it.

// workloadTargetInstallations answers the ids of a tenant's ACTIVE
// installations of a target. Nil means the business service's
// ActiveTargetInstallations; a test sets it to answer without a database.
var workloadTargetInstallations func(ctx context.Context, orgID, targetID string) ([]string, error)

func activeTargetInstallations(ctx context.Context, orgID, targetID string) ([]string, error) {
	if workloadTargetInstallations != nil {
		return workloadTargetInstallations(ctx, orgID, targetID)
	}
	return service.ActiveTargetInstallations(ctx, orgID, targetID)
}

// AdmitWorkloadOperation answers whether a capability presented with no bearer
// at the declared module route for alias is a module operation context for it,
// and whether the route's target is installed in its tenant. A refusal is an
// answer, not an error: errors are reserved for "could not decide", which the
// gateway answers 503.
func (s *WorkContextAuthorityServer) AdmitWorkloadOperation(
	ctx context.Context, req *gen.AdmitWorkloadOperationRequest,
) (*gen.AdmitWorkloadOperationResponse, error) {
	// The credential first: a caller without it learns nothing, not even
	// which of its fields would have been refused.
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if err := Validate(req); err != nil {
		return nil, err
	}
	if !s.configured() || service == nil {
		return nil, status.Error(codes.Unavailable, "Work Context authority is not configured")
	}
	claims, refusal, err := s.admitWorkloadOperation(ctx, req.GetWorkContext(), req.GetAlias(), req.GetTargetId())
	if err != nil {
		return nil, status.Error(codes.Unavailable, "module installation authority unavailable")
	}
	if refusal != gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_UNSPECIFIED {
		return &gen.AdmitWorkloadOperationResponse{Refusal: refusal}, nil
	}
	return &gen.AdmitWorkloadOperationResponse{
		Admitted:       true,
		TenantId:       claims.GetTenantId(),
		InstallationId: claims.GetSeal().GetInstallationId(),
		TaskId:         claims.GetTaskId(),
	}, nil
}

// admitWorkloadOperation is the decision. It returns the claims when admitted,
// the refusal otherwise, and an error only when a live read could not answer.
func (s *WorkContextAuthorityServer) admitWorkloadOperation(
	ctx context.Context, encoded, alias, targetID string,
) (*basev0.WorkContextV1, gen.WorkloadOperationRefusal, error) {
	// No operation context is minted for the capability surface, and an empty
	// alias is not a route.
	if requireMintableAudience(alias) != nil {
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_INSPECTED, nil
	}
	read, err := s.readBackFor(ctx, encoded, alias)
	if err != nil {
		refusal, refused := readBackRefusal(err)
		if !refused {
			return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_UNSPECIFIED, err
		}
		return nil, refusal, nil
	}
	claims := read.Context()
	if claims.GetOwnerPrincipalKind() != business.PrincipalKindService {
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_OWNER_NOT_SERVICE, nil
	}
	if !isModuleOperationContextShape(claims) {
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_OPERATION_CONTEXT, nil
	}
	owner := claims.GetOwnerPrincipalId()
	if owner == business.ModulePrincipalID(alias) {
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_OWNER_IS_TARGET, nil
	}
	// The read-back held the seal to the owner's live installation; this says
	// the owner is a module the deployment declares, the only service principal
	// StartModuleOperationTask signs for.
	if !service.IsDeclaredModulePrincipal(owner) {
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_SEAL_NOT_OWNERS, nil
	}
	installed, err := activeTargetInstallations(ctx, claims.GetTenantId(), targetID)
	switch {
	case err != nil:
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_UNSPECIFIED, err
	case len(installed) == 0:
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_TARGET_NOT_INSTALLED, nil
	case slices.Contains(installed, claims.GetSeal().GetInstallationId()):
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_SEAL_ON_TARGET, nil
	}
	return claims, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_UNSPECIFIED, nil
}

// readBackRefusal names the refusal a failed read-back is, and reports false for
// a failure that is not about the capability (a live read that could not
// answer), which the caller answers as undecidable.
func readBackRefusal(err error) (gen.WorkloadOperationRefusal, bool) {
	switch {
	case errors.Is(err, workcontext.ErrInvalid), errors.Is(err, workcontext.ErrNotACoreToken):
		return gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_INSPECTED, true
	case errors.Is(err, workcontext.ErrRevoked),
		// The live module authority no longer stands behind the owner: its
		// installation is gone, or its grant now names another tenant. The
		// capability is stale, not undecidable.
		errors.Is(err, business.ErrModuleInstallationInactive),
		errors.Is(err, business.ErrModuleTenantNotCurrent):
		// Core reports a seal the owner no longer holds as ErrRevoked too
		// (checkSealAgainst), so that case is this one.
		return gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_CURRENT, true
	case errors.Is(err, business.ErrNoOperationBinding), errors.Is(err, ErrNoApprovalGrants):
		// A binding or a grant hop: neither is in an operation context.
		return gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_OPERATION_CONTEXT, true
	}
	return gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_UNSPECIFIED, false
}

// isModuleOperationContextShape reports whether a service principal's claims
// are exactly what StartModuleOperationTask signs: the module acting as itself
// (no actor chain, no grant hop, no owner agent, and no operation binding but
// one of the owner's own installed ones, which that mint names), a root
// session of its own task, the binding's headless scopes, idempotent, and no
// longer than the operation-context window. Anything a re-mint, a child or
// another service mint would add is a difference, and a difference refuses.
func isModuleOperationContextShape(claims *basev0.WorkContextV1) bool {
	window := time.Duration(claims.GetExpiresAtUnix()-claims.GetIssuedAtUnix()) * time.Second
	return len(claims.GetActorChain()) == 0 &&
		claims.GrantHop == nil &&
		(claims.OperationBinding == nil ||
			business.IsModuleOperationBindingOf(claims.GetOperationBinding().GetBindingId(), claims.GetOwnerPrincipalId())) &&
		claims.OwnerAgentId == nil &&
		claims.ParentSessionId == nil &&
		claims.WorkspaceId == nil &&
		claims.ProjectId == nil &&
		len(claims.GetAttributionTeamIds()) == 0 &&
		len(claims.GetAuthorityScopes()) > 0 &&
		claims.GetReplayPolicy() == workcontext.ReplayIdempotent &&
		window > 0 && window <= business.ModuleOperationContextTTL
}
