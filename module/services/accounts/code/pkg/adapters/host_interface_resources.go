package adapters

import (
	"context"
	"slices"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
)

// scopedResourceReader lists the resources a principal holds a kind and action
// on through grants limited to one resource each.
type scopedResourceReader interface {
	WorkContextScopedResourceIDs(ctx context.Context, orgID, principalID, kind, action string) ([]string, error)
}

// ListResources answers which resources of a kind a capability covers for an
// action, decided now from its owner's grants rather than carried in it. The
// capability must hold the action on the kind. A person's answer is what their
// grants cover: their solution roles' where a solution on the path declares
// roles, their organisation grants otherwise. Any other owner is bounded by
// the capability alone. A capability naming resource ids covers only those the
// owner's grants cover. Asking spends no single-use capability.
func (s *HostAuthorityServer) ListResources(ctx context.Context, req *authorityv1.ListResourcesRequest) (*authorityv1.ListResourcesResponse, error) {
	kind, action := req.GetResourceKind(), req.GetAction()
	if req.GetAudience() == "" || kind == "" || action == "" {
		return nil, status.Error(codes.InvalidArgument, "a resource question names the audience, a kind and an action")
	}
	capability, err := s.workContexts.readBackFor(ctx, req.GetToken(), req.GetAudience())
	if err != nil {
		return nil, interfaceStatus(err)
	}
	wc := capability.Context()
	if err := requireModuleChainStands(ctx, wc); err != nil {
		return nil, err
	}
	// An installation's agent owns its capability by its installation's
	// standing, re-read here as Check re-reads it.
	if wc.GetOwnerPrincipalKind() == business.PrincipalKindAgent {
		if _, _, err := s.workContexts.requireInstallationStands(ctx, wc); err != nil {
			return nil, err
		}
	}
	effective := effectiveScopes(wc)
	held, whole, ids := false, false, []string{}
	for _, scope := range effective {
		if scope.GetResourceKind() != kind || !slices.Contains(scope.GetActions(), action) {
			continue
		}
		held = true
		whole = whole || len(scope.GetResourceIds()) == 0
		ids = append(ids, scope.GetResourceIds()...)
	}
	if !held {
		return nil, status.Errorf(codes.PermissionDenied, "the capability holds no %s:%s", kind, action)
	}
	all := []*authorityv1.ResourceGrant{{Match: &authorityv1.ResourceGrant_All{All: true}}}
	if wc.GetOwnerPrincipalKind() != business.PrincipalKindHuman {
		if whole {
			return &authorityv1.ListResourcesResponse{Grants: all}, nil
		}
		return &authorityv1.ListResourcesResponse{Grants: idGrants(ids)}, nil
	}
	tenant, owner := wc.GetTenantId(), wc.GetOwnerPrincipalId()
	ctx = accountsauth.WithVerifiedDatabaseIdentity(ctx, owner, tenant)
	ceiling, bounded, err := s.solutionCeiling(ctx, tenant, owner, solutionPath(wc, wc.GetAudience()))
	if err != nil {
		return nil, err
	}
	if bounded {
		return &authorityv1.ListResourcesResponse{Grants: roleResources(business.IntersectWorkScopes(effective, ceiling), kind, action)}, nil
	}
	coveredAll, covered, err := s.workContexts.ownerResourceGrants(ctx, tenant, owner, kind, action)
	if err != nil {
		return nil, err
	}
	switch {
	case coveredAll && whole:
		return &authorityv1.ListResourcesResponse{Grants: all}, nil
	case coveredAll:
		return &authorityv1.ListResourcesResponse{Grants: idGrants(ids)}, nil
	case whole:
		return &authorityv1.ListResourcesResponse{Grants: idGrants(covered)}, nil
	}
	var both []string
	for _, id := range ids {
		if slices.Contains(covered, id) {
			both = append(both, id)
		}
	}
	return &authorityv1.ListResourcesResponse{Grants: idGrants(both)}, nil
}

// ownerResourceGrants is what the owner's organisation grants cover of kind
// for action: every resource when they hold it organisation-wide (by the check
// every mint makes, content-read standing aside), the ids of their
// resource-limited grants otherwise.
func (s *WorkContextAuthorityServer) ownerResourceGrants(ctx context.Context, orgID, ownerID, kind, action string) (bool, []string, error) {
	_, err := s.resolveAuthority(ctx, orgID, ownerID, "", []business.WorkContextPermission{{ResourceKind: kind, Action: action}})
	switch status.Code(err) {
	case codes.OK:
		return true, nil, nil
	case codes.PermissionDenied:
	default:
		return false, nil, err
	}
	reader, ok := s.authority.(scopedResourceReader)
	if !ok {
		return false, nil, status.Error(codes.Unavailable, "this host's authority store lists no resource-limited grants")
	}
	ids, err := reader.WorkContextScopedResourceIDs(ctx, orgID, ownerID, kind, action)
	if err != nil {
		return false, nil, status.Error(codes.Unavailable, err.Error())
	}
	return false, ids, nil
}

// effectiveScopes are what the capability's current actor holds: the last
// hop's grant, or the owner's authority when no hop narrowed it.
func effectiveScopes(wc *basev0.WorkContextV1) []*basev0.WorkScopeV1 {
	if chain := wc.GetActorChain(); len(chain) > 0 {
		return chain[len(chain)-1].GetGrantedScopes()
	}
	return wc.GetAuthorityScopes()
}

// roleResources is what role scopes cover of kind for action: every resource
// for a scope naming none, its ids otherwise.
func roleResources(scopes []*basev0.WorkScopeV1, kind, action string) []*authorityv1.ResourceGrant {
	var ids []string
	for _, scope := range scopes {
		if scope.GetResourceKind() != kind || !slices.Contains(scope.GetActions(), action) {
			continue
		}
		if len(scope.GetResourceIds()) == 0 {
			return []*authorityv1.ResourceGrant{{Match: &authorityv1.ResourceGrant_All{All: true}}}
		}
		ids = append(ids, scope.GetResourceIds()...)
	}
	return idGrants(ids)
}

func idGrants(ids []string) []*authorityv1.ResourceGrant {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	out := []*authorityv1.ResourceGrant{}
	for _, id := range slices.Compact(ids) {
		out = append(out, &authorityv1.ResourceGrant{Match: &authorityv1.ResourceGrant_Id{Id: id}})
	}
	return out
}
