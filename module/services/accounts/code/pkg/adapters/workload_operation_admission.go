package adapters

import (
	"context"
	"crypto/ed25519"
	"errors"
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
// It consumes nothing. It inspects (core's stateless prefix of Verify:
// signature under this issuer, window, audience, shape) and decodes; it never
// reaches verifyFor, verifyIssued or the replay store, so a single-use
// capability it was shown still verifies at its callee, which remains the one
// party that verifies at full strength.

// AdmitWorkloadOperation answers whether a capability presented with no bearer
// at the declared module route for alias is a module operation context for it.
// A refusal is an answer, not an error: errors are reserved for "could not
// decide", which the gateway answers 503.
func (s *WorkContextAuthorityServer) AdmitWorkloadOperation(
	ctx context.Context, req *gen.AdmitWorkloadOperationRequest,
) (*gen.AdmitWorkloadOperationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if !s.configured() || service == nil {
		return nil, status.Error(codes.Unavailable, "Work Context authority is not configured")
	}
	claims, refusal, err := s.admitWorkloadOperation(ctx, req.GetWorkContext(), req.GetAlias())
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
// the refusal otherwise, and an error only when the live read could not answer.
func (s *WorkContextAuthorityServer) admitWorkloadOperation(
	ctx context.Context, encoded, alias string,
) (*basev0.WorkContextV1, gen.WorkloadOperationRefusal, error) {
	// No operation context is minted for the capability surface, and an empty
	// alias is not a route.
	if requireMintableAudience(alias) != nil {
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_INSPECTED, nil
	}
	inspector := &workcontext.Inspector{
		Issuer: s.issuer,
		Keys:   map[string]ed25519.PublicKey{s.keyID: s.publicKey},
		Now:    s.now,
	}
	if _, err := inspector.Inspect(encoded, alias); err != nil {
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_INSPECTED, nil
	}
	// The signature is this issuer's, so the claims are its own statement.
	decoded, err := workcontext.Decode(encoded)
	if err != nil {
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_NOT_INSPECTED, nil
	}
	claims := decoded.Context()
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
	if !service.IsDeclaredModulePrincipal(owner) {
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_SEAL_NOT_OWNERS, nil
	}
	// The seal must be the ACTIVE installation the owner is installed under in
	// the tenant, read live: the same answer a verifier holds the seal to.
	seal := claims.GetSeal().GetInstallationId()
	live, _, err := service.WorkContextSeal(ctx, claims.GetTenantId(), owner, seal)
	switch {
	case errors.Is(err, business.ErrModuleInstallationInactive), errors.Is(err, business.ErrWorkContextSealUnheld):
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_SEAL_NOT_OWNERS, nil
	case err != nil:
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_UNSPECIFIED, err
	case live == "" || live != seal:
		return nil, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_SEAL_NOT_OWNERS, nil
	}
	return claims, gen.WorkloadOperationRefusal_WORKLOAD_OPERATION_REFUSAL_UNSPECIFIED, nil
}

// isModuleOperationContextShape reports whether a service principal's claims
// are exactly what StartModuleOperationTask signs: the module acting as itself
// (no actor chain, no grant hop, no operation binding, no owner agent), a root
// session of its own task, the binding's headless scopes, idempotent, and no
// longer than the operation-context window. Anything a re-mint, a child or
// another service mint would add is a difference, and a difference refuses.
func isModuleOperationContextShape(claims *basev0.WorkContextV1) bool {
	window := time.Duration(claims.GetExpiresAtUnix()-claims.GetIssuedAtUnix()) * time.Second
	return len(claims.GetActorChain()) == 0 &&
		claims.GrantHop == nil &&
		claims.OperationBinding == nil &&
		claims.OwnerAgentId == nil &&
		claims.ParentSessionId == nil &&
		claims.WorkspaceId == nil &&
		claims.ProjectId == nil &&
		len(claims.GetAttributionTeamIds()) == 0 &&
		len(claims.GetAuthorityScopes()) > 0 &&
		claims.GetReplayPolicy() == workcontext.ReplayIdempotent &&
		window > 0 && window <= business.ModuleOperationContextTTL
}
