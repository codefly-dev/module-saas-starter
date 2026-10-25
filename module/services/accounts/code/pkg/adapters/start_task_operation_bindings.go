package adapters

import (
	"context"
	"slices"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// requireOperationBindingMint refuses operation_bindings outside the one shape
// that takes them: a direct human-owned mint. An actor's ceiling and a confined
// credential's consumer ceiling are decided on the scopes a caller names, so
// neither is widened by a name resolved here.
func requireOperationBindingMint(ctx context.Context, req *gen.StartTaskWorkContextRequest) error {
	if len(req.GetOperationBindings()) == 0 {
		return nil
	}
	if req.GetActorPrincipalId() != "" {
		return status.Error(codes.InvalidArgument, "operation_bindings is for a direct human-owned mint, not an actor's")
	}
	if _, confined := accountsauth.VerifiedResourceConfinement(ctx); confined {
		return status.Error(codes.InvalidArgument, "operation_bindings is refused on a resource-confined mint")
	}
	if service == nil {
		return status.Error(codes.Unavailable, "module authority unavailable")
	}
	return nil
}

type operationBindingProjection struct {
	scopes     []*gen.WorkContextScope
	readKinds  []string
	identities []string
}

// withOperationBindingScopes adds the invoke scopes of each named operation
// binding of the audience module to the scopes a caller asked for.
//
// A name resolves through service.ModuleOperationAudience, the resolution the
// module's delegated exchange redeems the binding through: the binding as
// installed at the organisation, narrowed by that installation. So the resource
// ids come from the installation, never from the caller, and the capability
// carries exactly what the exchange will ask of it. Each resource is kept only
// if the person holds it at the binding target, by the same check every mint makes; a binding the
// person holds nothing under is refused by name. A scope the caller already
// named is not added twice.
func (s *WorkContextAuthorityServer) withOperationBindingScopes(ctx context.Context, orgID, audience string, asked []*gen.WorkContextScope, names []string) (*operationBindingProjection, error) {
	ownerID, err := s.authorizeOwner(ctx, orgID)
	if err != nil {
		return nil, err
	}
	module := business.ModuleCaller{PrincipalID: business.ModulePrincipalID(audience), BoundOrg: orgID}
	out := &operationBindingProjection{scopes: slices.Clone(asked)}
	for _, name := range names {
		binding, err := service.ModuleOperationAudience(ctx, module, orgID, audience, name)
		if err != nil {
			return nil, err
		}
		held := 0
		for _, scope := range binding.InvokeScopes {
			kept, err := s.heldBindingScope(ctx, orgID, ownerID, binding.Audience, scope)
			if err != nil {
				return nil, err
			}
			for _, part := range kept {
				held++
				if !slices.ContainsFunc(out.scopes, func(existing *gen.WorkContextScope) bool { return proto.Equal(existing, part) }) {
					out.scopes = append(out.scopes, part)
				}
				if len(part.ResourceIds) == 0 && slices.Contains(part.Actions, "read") && mintContentReads(binding.Audience)(part.ResourceKind) {
					out.readKinds = append(out.readKinds, part.ResourceKind)
				}
			}
		}
		if held == 0 {
			return nil, status.Errorf(codes.PermissionDenied, "the person holds nothing under operation binding %q", name)
		}
		out.identities = append(out.identities, business.ModuleOperationBindingID(orgID, audience, name))
	}
	return out, nil
}

// heldBindingScope projects each action independently at the binding's target.
// Actions combine only when their resource sets agree, so holding read on one
// resource and invoke on another never grants both actions on both resources.
func (s *WorkContextAuthorityServer) heldBindingScope(ctx context.Context, orgID, ownerID, audience string, scope business.ModuleOperationScope) ([]*gen.WorkContextScope, error) {
	candidate := func(action string, ids []string) *gen.WorkContextScope {
		return &gen.WorkContextScope{ResourceKind: scope.ResourceKind, Actions: []string{action}, ResourceIds: ids}
	}
	holds := func(c *gen.WorkContextScope) (bool, error) {
		permissions, _, err := workContextScopes([]*gen.WorkContextScope{c}, mintContentReads(audience))
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
	var out []*gen.WorkContextScope
	for _, action := range scope.Actions {
		var ids []string
		if len(scope.ResourceIDs) == 0 {
			ok, err := holds(candidate(action, nil))
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		} else {
			for _, id := range scope.ResourceIDs {
				ok, err := holds(candidate(action, []string{id}))
				if err != nil {
					return nil, err
				}
				if ok {
					ids = append(ids, id)
				}
			}
			if len(ids) == 0 {
				continue
			}
		}
		index := slices.IndexFunc(out, func(existing *gen.WorkContextScope) bool { return slices.Equal(existing.ResourceIds, ids) })
		if index >= 0 {
			out[index].Actions = append(out[index].Actions, action)
		} else {
			out = append(out, candidate(action, ids))
		}
	}
	return out, nil
}
