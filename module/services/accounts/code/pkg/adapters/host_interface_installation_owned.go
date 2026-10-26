package adapters

// A capability an installation's agent owns (the coordinator's decision of
// 2026-10-25): an installation acting for itself is the owner of what it is
// issued, not its human owner of record with the agent acting, because no
// person is acting. Its authority is the installation's standing grants, read
// live: at the issue (forInstallation), at every Check, and at every hop below
// it, where rule A reads the installation's grants as it reads a person's.

import (
	"context"
	"errors"
	"slices"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// requireInstallationStands re-verifies a capability an installation's agent
// owns against live state, beyond the seal and revision core has checked: the
// installation that agent acts for is active, its agent is live, an owner of
// record is still an organisation admin, and its standing grants still admit
// what the capability carries (the last hop's grant on a chained one, whose
// hops must not be revoked). At its own audience the agent's ceiling holds
// too. It answers the installation and what was resolved for it.
func (s *WorkContextAuthorityServer) requireInstallationStands(ctx context.Context, wc *basev0.WorkContextV1) (string, *business.InstallationAuthorityFacts, error) {
	tenant, agent := wc.GetTenantId(), wc.GetOwnerPrincipalId()
	if wc.GetOwnerPrincipalKind() != business.PrincipalKindAgent {
		return "", nil, status.Errorf(codes.PermissionDenied, "this context is owned by a %s, not an installation's agent", wc.GetOwnerPrincipalKind())
	}
	installation, err := s.authority.WorkContextAgentInstallation(ctx, tenant, agent)
	var missing *business.StoreError
	switch {
	case errors.As(err, &missing) && missing.StoreErrorType == business.ErrTypeNotFound:
		return "", nil, status.Errorf(codes.PermissionDenied, "the installation agent %s acts for no active installation: %v", agent, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", nil, status.FromContextError(err).Err()
	case err != nil:
		return "", nil, status.Errorf(codes.Unavailable, "cannot read the installation agent %s acts for", agent)
	}
	carried := effectiveScopes(wc)
	facts, err := s.resolveInstallationAuthority(ctx, tenant, installation, permissionsFromCoreScopes(carried, recheckContentReads()))
	if err != nil {
		return "", nil, err
	}
	if facts.Actor == nil || facts.Actor.ID != agent {
		return "", nil, status.Errorf(codes.PermissionDenied, "installation %s no longer acts through agent %s", installation, agent)
	}
	if chain := wc.GetActorChain(); len(chain) > 0 {
		if err := s.requireChainNotRevoked(ctx, tenant, chain); err != nil {
			return "", nil, err
		}
	} else if err := enforceActorCeiling(ctx, facts.Actor, wc.GetAudience(), carried); err != nil {
		return "", nil, err
	}
	return installation, facts, nil
}

// installationHeldThrough is rule A for a parent an installation's agent
// owns: of what the binding may do (through), what that installation's
// standing grants admit at the audience now, action by action, within the
// kinds the agent may be delegated. The parent is re-verified first, so a
// hop stands on a parent that still stands.
func (s *WorkContextAuthorityServer) installationHeldThrough(ctx context.Context, parent *basev0.WorkContextV1, audience string, through []*basev0.WorkScopeV1) ([]*basev0.WorkScopeV1, error) {
	installation, facts, err := s.requireInstallationStands(ctx, parent)
	if err != nil {
		return nil, err
	}
	var held []*basev0.WorkScopeV1
	for _, scope := range through {
		if len(facts.Actor.AllowedScopes) > 0 && !slices.Contains(facts.Actor.AllowedScopes, scope.GetResourceKind()) {
			continue
		}
		var actions []string
		for _, action := range scope.GetActions() {
			admitted, err := s.installationAdmits(ctx, parent.GetTenantId(), installation, audience,
				&gen.WorkContextScope{ResourceKind: scope.GetResourceKind(), Actions: []string{action}, ResourceIds: scope.GetResourceIds()})
			if err != nil {
				return nil, err
			}
			if admitted {
				actions = append(actions, action)
			}
		}
		if len(actions) > 0 {
			held = append(held, &basev0.WorkScopeV1{ResourceKind: scope.GetResourceKind(), Actions: actions, ResourceIds: scope.GetResourceIds()})
		}
	}
	return held, nil
}

// installationAdmits answers whether an installation's standing grants admit
// one scope at the audience, by the check its mint makes.
func (s *WorkContextAuthorityServer) installationAdmits(ctx context.Context, orgID, installationID, audience string, scope *gen.WorkContextScope) (bool, error) {
	permissions, _, err := workContextScopes([]*gen.WorkContextScope{scope}, mintContentReads(audience))
	if err != nil {
		return false, status.Error(codes.Internal, err.Error())
	}
	_, err = s.resolveInstallationAuthority(ctx, orgID, installationID, permissions)
	switch status.Code(err) {
	case codes.OK:
		return true, nil
	case codes.PermissionDenied:
		return false, nil
	}
	return false, err
}
