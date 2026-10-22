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

// withOperationBindingScopes adds the invoke scopes of each named operation
// binding of the audience module to the scopes a caller asked for.
//
// A name resolves through service.ModuleOperationAudience, the resolution the
// module's delegated exchange redeems the binding through: the binding as
// installed at the organisation, narrowed by that installation. So the resource
// ids come from the installation, never from the caller, and the capability
// carries exactly what the exchange will ask of it. Each resource is kept only
// if the person holds it, by the same check every mint makes; a binding the
// person holds nothing under is refused by name. A scope the caller already
// named is not added twice.
func (s *WorkContextAuthorityServer) withOperationBindingScopes(ctx context.Context, orgID, audience string, asked []*gen.WorkContextScope, names []string) ([]*gen.WorkContextScope, error) {
	ownerID, err := s.authorizeOwner(ctx, orgID)
	if err != nil {
		return nil, err
	}
	module := business.ModuleCaller{PrincipalID: business.ModulePrincipalID(audience), BoundOrg: orgID}
	out := slices.Clone(asked)
	for _, name := range names {
		binding, err := service.ModuleOperationAudience(ctx, module, orgID, audience, name)
		if err != nil {
			return nil, err
		}
		held := 0
		for _, scope := range binding.InvokeScopes {
			kept, err := s.heldBindingScope(ctx, orgID, ownerID, audience, scope)
			if err != nil {
				return nil, err
			}
			if kept == nil {
				continue
			}
			held++
			if !slices.ContainsFunc(out, func(existing *gen.WorkContextScope) bool { return proto.Equal(existing, kept) }) {
				out = append(out, kept)
			}
		}
		if held == 0 {
			return nil, status.Errorf(codes.PermissionDenied, "the person holds nothing under operation binding %q", name)
		}
	}
	return out, nil
}

// heldBindingScope is the part of one binding scope the person holds: every
// action of the scope, on each resource it names, or on its kind when it names
// none. Nil when the person holds none of it.
func (s *WorkContextAuthorityServer) heldBindingScope(ctx context.Context, orgID, ownerID, audience string, scope business.ModuleOperationScope) (*gen.WorkContextScope, error) {
	candidate := func(ids []string) *gen.WorkContextScope {
		return &gen.WorkContextScope{ResourceKind: scope.ResourceKind, Actions: slices.Clone(scope.Actions), ResourceIds: ids}
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
	if len(scope.ResourceIDs) == 0 {
		if ok, err := holds(candidate(nil)); !ok || err != nil {
			return nil, err
		}
		return candidate(nil), nil
	}
	var ids []string
	for _, id := range scope.ResourceIDs {
		ok, err := holds(candidate([]string{id}))
		if err != nil {
			return nil, err
		}
		if ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return candidate(ids), nil
}
