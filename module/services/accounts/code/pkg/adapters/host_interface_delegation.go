package adapters

// A person's delegation to a module behind the host interface: Delegate,
// GetDelegation, RevokeDelegation, and Issue from a delegation.
//
// The delegation is a standing ceiling, never a grant of its own. Delegate
// holds what it names to what the person holds now, at the module and at each
// audience its bindings reach. An issue from it re-reads the delegation and the
// grantor's current grants through the binding the module exercises, and
// issues for the grantor, with the module as the one acting hop carrying the
// delegation's id: the same hop a module's child derives (hopFrom), from the
// grantor's standing instead of a parent capability. Every later hop of that
// chain is cut to the delegation again (chainDelegations), so no hop below it
// gets what the person did not delegate. Revoking it advances the grantor's
// authorization revision in the statement that stamps it, so every capability
// issued from it fails its next check.

import (
	"context"
	"errors"
	"slices"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Delegate records the signed-in person's grant to a module, within what they
// hold now.
func (s *HostAuthorityServer) Delegate(ctx context.Context, req *authorityv1.DelegateRequest) (*authorityv1.Delegation, error) {
	identity, ok := accountsauth.VerifiedRequestIdentity(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "a delegation is made by the signed-in person; the call carries no bearer")
	}
	module := req.GetModule()
	if service == nil || !service.IsDeclaredModulePrincipal(business.ModulePrincipalID(module)) {
		return nil, status.Errorf(codes.NotFound, "module %q is not composed", module)
	}
	if len(req.GetScopes()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "a delegation names what it grants")
	}
	org, person := identity.OrgID.String(), identity.EffectiveSubject.String()
	if err := requireOrgMember(ctx, person, org); err != nil {
		return nil, err
	}
	_, scopes, err := workContextScopes(scopesToHost(req.GetScopes()), mintContentReads(module))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	audiences := delegationAudiences(module)
	for _, scope := range scopes {
		if len(scope.GetActions()) == 0 {
			return nil, status.Errorf(codes.InvalidArgument, "a delegated scope names its actions: %s", scope.GetResourceKind())
		}
		for _, action := range scope.GetActions() {
			one := &gen.WorkContextScope{ResourceKind: scope.GetResourceKind(), Actions: []string{action}, ResourceIds: scope.GetResourceIds()}
			held, err := s.heldAtAny(ctx, org, person, audiences, one)
			if err != nil {
				return nil, err
			}
			if !held {
				return nil, status.Errorf(codes.PermissionDenied, "%s delegates what they do not hold: %s, at none of %v", person, scopeString(&basev0.WorkScopeV1{
					ResourceKind: one.GetResourceKind(), Actions: one.GetActions(), ResourceIds: one.GetResourceIds(),
				}), audiences)
			}
		}
	}
	d := &business.ModuleDelegation{OrgID: org, PrincipalID: person, ModulePrefix: module, Scopes: operationScopes(scopes), Purpose: req.GetPurpose()}
	if err := service.RecordModuleDelegation(ctx, d); err != nil {
		return nil, delegationStatus(err)
	}
	return delegationToWire(d), nil
}

// GetDelegation reads one delegation for the module it is granted to (by its
// workload credential) or for its grantor (by the bearer). Anyone else is told
// it does not exist.
func (s *HostAuthorityServer) GetDelegation(ctx context.Context, req *authorityv1.GetDelegationRequest) (*authorityv1.Delegation, error) {
	if service == nil {
		return nil, status.Error(codes.Unavailable, "the authority is not wired")
	}
	id := req.GetDelegationId()
	if credential := req.GetActor().GetCredential(); credential != "" {
		prefix, err := service.ModulePrefixOfWorkload(ctx, credential)
		if err != nil {
			return nil, credentialRefusal(err, "actor")
		}
		d, err := service.ModuleDelegation(ctx, "", id)
		if err != nil {
			return nil, delegationStatus(err)
		}
		if d == nil || d.ModulePrefix != prefix {
			return nil, status.Errorf(codes.NotFound, "no delegation %q is granted to %s", id, prefix)
		}
		return delegationToWire(d), nil
	}
	identity, ok := accountsauth.VerifiedRequestIdentity(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "a delegation is read by its module, with its workload credential, or by its grantor, signed in; the call carries neither")
	}
	d, err := service.ModuleDelegation(ctx, identity.OrgID.String(), id)
	if err != nil {
		return nil, delegationStatus(err)
	}
	if d == nil || d.PrincipalID != identity.EffectiveSubject.String() {
		return nil, status.Errorf(codes.NotFound, "no delegation %q of yours", id)
	}
	return delegationToWire(d), nil
}

// RevokeDelegation ends one of the signed-in person's delegations; the next
// issue from it, and every capability issued from it, is refused.
func (s *HostAuthorityServer) RevokeDelegation(ctx context.Context, req *authorityv1.RevokeDelegationRequest) (*authorityv1.RevokeDelegationResponse, error) {
	identity, ok := accountsauth.VerifiedRequestIdentity(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "a delegation is revoked by its grantor, signed in; the call carries no bearer")
	}
	if service == nil {
		return nil, status.Error(codes.Unavailable, "the authority is not wired")
	}
	d, err := service.RevokeModuleDelegation(ctx, identity.OrgID.String(), identity.EffectiveSubject.String(), req.GetDelegationId())
	if err != nil {
		return nil, delegationStatus(err)
	}
	return &authorityv1.RevokeDelegationResponse{Delegation: delegationToWire(d)}, nil
}

// delegationHop is Issue from a delegation: the acting module proves itself,
// the delegation must be granted to it and stand, and the hop is the module's
// through the binding it names, for the grantor. Nothing is read from a live
// capability, so renewing is issuing again.
func (s *HostAuthorityServer) delegationHop(ctx context.Context, id string, req *authorityv1.IssueRequest) (*gen.IssuedWorkContext, error) {
	if req.GetActor().GetCredential() == "" {
		return nil, status.Error(codes.Unauthenticated, "an issue from a delegation names the module acting (actor); the call carries no actor credential")
	}
	prefix, err := service.ModulePrefixOfWorkload(ctx, req.GetActor().GetCredential())
	if err != nil {
		return nil, credentialRefusal(err, "actor")
	}
	d, err := service.ModuleDelegation(ctx, "", id)
	if err != nil {
		return nil, delegationStatus(err)
	}
	if d == nil || d.ModulePrefix != prefix {
		return nil, status.Errorf(codes.NotFound, "no delegation %q is granted to %s", id, prefix)
	}
	if !d.Active() {
		return nil, status.Errorf(codes.PermissionDenied, "delegation %s is revoked", id)
	}
	if req.GetOperationBindingId() == "" {
		return nil, status.Errorf(codes.PermissionDenied, "an issue from a delegation names the binding it exercises; %s named none", prefix)
	}
	if err := requireComposed(req.GetAudience()); err != nil {
		return nil, err
	}
	if err := requireDeclaredHopBinding(prefix, req); err != nil {
		return nil, err
	}
	if err := requireVocabularyAudience(ctx, req.GetAudience()); err != nil {
		return nil, err
	}
	// The grantor's standing, as the root the module's one hop derives from:
	// a fresh task in the delegation's organisation, bounded only by the
	// issuer's ceiling.
	now := time.Now()
	if s.workContexts.now != nil {
		now = s.workContexts.now()
	}
	standing := &basev0.WorkContextV1{
		TenantId: d.OrgID, OrganizationId: proto.String(d.OrgID), TaskId: uuid.NewString(),
		OwnerPrincipalId: d.PrincipalID, OwnerPrincipalKind: proto.String(business.PrincipalKindHuman),
		ExpiresAtUnix: now.Add(workcontext.DefaultMaxTTL).Unix(),
	}
	issued, err := s.hopFrom(ctx, prefix, standing, req, d)
	if err != nil {
		return nil, err
	}
	// A revocation that committed while the hop was signed may have left it
	// sealed to the revision before; refuse it here rather than hand out a
	// capability its next check refuses.
	if after, err := service.ModuleDelegation(ctx, "", id); err != nil {
		return nil, delegationStatus(err)
	} else if !after.Active() {
		return nil, status.Errorf(codes.PermissionDenied, "delegation %s is revoked", id)
	}
	return issued, nil
}

// chainDelegations is every stored delegation a hop of claims acts under, and
// issuing, each confirmed for the chain: active, its grantor the owner, in the
// owner's organisation, and granted to the module on that hop.
func (s *HostAuthorityServer) chainDelegations(ctx context.Context, claims *basev0.WorkContextV1, issuing *business.ModuleDelegation) ([]*business.ModuleDelegation, error) {
	hops := map[string]string{} // delegation id -> the module acting under it, by name
	ids := make([]string, 0, len(claims.GetActorChain()))
	for _, hop := range claims.GetActorChain() {
		if id := hop.GetDelegationId(); id != "" {
			hops[id] = hop.GetPrincipalId()
			ids = append(ids, id)
		}
	}
	var stored []*business.ModuleDelegation
	if len(ids) > 0 {
		var err error
		if stored, err = service.ModuleDelegations(ctx, claims.GetTenantId(), ids); err != nil {
			return nil, delegationStatus(err)
		}
	}
	for _, d := range stored {
		switch {
		case !d.Active():
			return nil, status.Errorf(codes.PermissionDenied, "delegation %s is revoked", d.ID)
		case d.PrincipalID != claims.GetOwnerPrincipalId() || d.OrgID != claims.GetTenantId() ||
			d.ModulePrefix != hops[d.ID]:
			return nil, status.Errorf(codes.PermissionDenied, "delegation %s does not authorize this actor chain", d.ID)
		}
	}
	if issuing != nil {
		stored = append(stored, issuing)
	}
	return stored, nil
}

// delegationAudiences is where a person may hold what they delegate to
// module: the module itself and every audience its bindings reach.
func delegationAudiences(module string) []string {
	audiences := []string{module}
	for _, binding := range service.ModulePrincipals()[business.ModulePrincipalID(module)].OperationAudiences {
		if !slices.Contains(audiences, binding.Audience) {
			audiences = append(audiences, binding.Audience)
		}
	}
	slices.Sort(audiences[1:])
	return audiences
}

// heldAtAny reports whether the person holds scope at one of audiences, by
// the check every mint makes.
func (s *HostAuthorityServer) heldAtAny(ctx context.Context, org, person string, audiences []string, scope *gen.WorkContextScope) (bool, error) {
	for _, audience := range audiences {
		held, err := s.workContexts.personHolds(ctx, org, person, audience, scope)
		if err != nil || held {
			return held, err
		}
	}
	return false, nil
}

// intersectScopes is what both scope sets allow, kind by kind.
func intersectScopes(a, b []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	var out []*basev0.WorkScopeV1
	for _, x := range a {
		for _, y := range b {
			if x.GetResourceKind() != y.GetResourceKind() {
				continue
			}
			if both := intersectScope(x, y); both != nil {
				out = append(out, both)
			}
		}
	}
	return out
}

func delegatedScopes(d *business.ModuleDelegation) []*basev0.WorkScopeV1 {
	out := make([]*basev0.WorkScopeV1, 0, len(d.Scopes))
	for _, scope := range d.Scopes {
		out = append(out, &basev0.WorkScopeV1{ResourceKind: scope.ResourceKind, Actions: slices.Clone(scope.Actions), ResourceIds: slices.Clone(scope.ResourceIDs)})
	}
	return out
}

func operationScopes(scopes []*basev0.WorkScopeV1) []business.ModuleOperationScope {
	out := make([]business.ModuleOperationScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, business.ModuleOperationScope{ResourceKind: scope.GetResourceKind(), Actions: slices.Clone(scope.GetActions()), ResourceIDs: slices.Clone(scope.GetResourceIds())})
	}
	return out
}

func delegationToWire(d *business.ModuleDelegation) *authorityv1.Delegation {
	out := &authorityv1.Delegation{
		DelegationId: d.ID, OrganizationId: d.OrgID, Grantor: d.PrincipalID, Module: d.ModulePrefix,
		Purpose: d.Purpose, Active: d.Active(),
	}
	for _, scope := range d.Scopes {
		out.Scopes = append(out.Scopes, &authorityv1.WorkScopeV1{ResourceKind: scope.ResourceKind, Actions: slices.Clone(scope.Actions), ResourceIds: slices.Clone(scope.ResourceIDs)})
	}
	return out
}

// delegationStatus names a delegation store refusal on the wire.
func delegationStatus(err error) error {
	switch {
	case errors.Is(err, business.ErrModuleDelegationNotFound):
		return status.Error(codes.NotFound, "no such delegation of yours")
	case errors.Is(err, business.ErrModuleDelegationInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, business.ErrPolicyLogUnreachable):
		return status.Errorf(codes.Unavailable, "the revocation cannot be witnessed, so it is refused: %v", err)
	}
	return interfaceStatus(err)
}
