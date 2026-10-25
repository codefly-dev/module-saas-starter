package adapters

import (
	"context"
	"slices"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// personHoldings is what a person's empty ask means at an audience: everything
// they hold there (HOST_INTERFACE.md gap 11).
//
// Nothing here decides authority. The candidates are every (kind, action) this
// host can name at the audience: the kinds the audience module declares as its
// content, each with the actions the composed permission vocabulary, the
// organisation's roles and the composition's bindings to the audience name on
// it. Each candidate is kept only if the person holds it by the check every mint
// makes (personHolds); one the person holds only on named resources is kept on
// exactly those resources, as their resource-limited grants list them. StartTask
// then decides the ask again as it decides any other. A person who holds
// nothing there is refused by name.
func (s *WorkContextAuthorityServer) personHoldings(ctx context.Context, orgID, audience string) ([]*gen.WorkContextScope, error) {
	ownerID, err := s.authorizeOwner(ctx, orgID)
	if err != nil {
		return nil, err
	}
	kinds, err := service.ModuleContentResources(audience)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "audience %q declares no content: %v", audience, err)
	}
	roles, err := service.ListRoles(ctx, &gen.ListRolesRequest{OrgId: orgID})
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	var held []*gen.WorkContextScope
	for _, kind := range kinds {
		var actions []string
		for _, action := range candidateActions(kind, audience, roles.GetRoles()) {
			holds, err := s.personHolds(ctx, orgID, ownerID, audience, &gen.WorkContextScope{ResourceKind: kind, Actions: []string{action}})
			if err != nil {
				return nil, err
			}
			if holds {
				actions = append(actions, action)
			} else if _, ok := s.authority.(scopedResourceReader); ok {
				_, ids, err := s.ownerResourceGrants(ctx, orgID, ownerID, kind, action)
				if err != nil {
					return nil, err
				}
				if len(ids) > 0 {
					held = append(held, &gen.WorkContextScope{ResourceKind: kind, Actions: []string{action}, ResourceIds: ids})
				}
			}
		}
		if len(actions) > 0 {
			held = append(held, &gen.WorkContextScope{ResourceKind: kind, Actions: actions})
		}
	}
	if len(held) == 0 {
		return nil, status.Errorf(codes.PermissionDenied, "the owner holds nothing at this audience: %s", audience)
	}
	return held, nil
}

// candidateActions is every action this host names on kind at audience, sorted
// and unique. A wildcard names no action: it is what a role grants, never what
// a capability carries.
func candidateActions(kind, audience string, roles []*gen.Role) []string {
	actions := business.VocabularyActions(kind)
	for _, role := range roles {
		for _, p := range role.GetPermissions() {
			if p.GetResource() == kind && p.GetAction() != "*" {
				actions = append(actions, p.GetAction())
			}
		}
	}
	for _, grant := range service.ModulePrincipals() {
		for _, binding := range grant.OperationAudiences {
			if binding.Audience != audience {
				continue
			}
			for _, scopes := range [][]business.ModuleOperationScope{binding.InvokeScopes, binding.HeadlessScopes, binding.LookupScopes} {
				for _, scope := range scopes {
					if scope.ResourceKind == kind {
						actions = append(actions, scope.Actions...)
					}
				}
			}
		}
	}
	slices.Sort(actions)
	return slices.Compact(actions)
}

// personHolds reports whether the person holds scope at audience, by the check
// every mint makes: the scope's permissions resolved against their current
// authority, with the audience's own content admitted through the content-read
// branch. Refused is false; a check that could not answer is the error.
func (s *WorkContextAuthorityServer) personHolds(ctx context.Context, orgID, ownerID, audience string, scope *gen.WorkContextScope) (bool, error) {
	permissions, _, err := workContextScopes([]*gen.WorkContextScope{scope}, mintContentReads(audience))
	if err != nil {
		return false, status.Error(codes.Internal, err.Error())
	}
	_, err = s.resolveAuthority(ctx, orgID, ownerID, "", permissions)
	switch status.Code(err) {
	case codes.OK:
		return true, nil
	case codes.PermissionDenied:
		return false, nil
	}
	return false, err
}

// ownerHoldsThrough is what the owner holds at audience now, within through
// (a binding's declared scopes): each of its actions kept only if the owner
// holds it there by personHolds, the check every mint makes. The caller has
// installed the owner's identity on ctx.
func (s *WorkContextAuthorityServer) ownerHoldsThrough(ctx context.Context, orgID, ownerID, audience string, through []*basev0.WorkScopeV1) ([]*basev0.WorkScopeV1, error) {
	var held []*basev0.WorkScopeV1
	for _, scope := range through {
		var actions []string
		for _, action := range scope.GetActions() {
			holds, err := s.personHolds(ctx, orgID, ownerID, audience, &gen.WorkContextScope{
				ResourceKind: scope.GetResourceKind(), Actions: []string{action}, ResourceIds: scope.GetResourceIds(),
			})
			if err != nil {
				return nil, err
			}
			if holds {
				actions = append(actions, action)
			}
		}
		if len(actions) > 0 {
			held = append(held, &basev0.WorkScopeV1{ResourceKind: scope.GetResourceKind(), Actions: actions, ResourceIds: scope.GetResourceIds()})
		}
	}
	return held, nil
}
