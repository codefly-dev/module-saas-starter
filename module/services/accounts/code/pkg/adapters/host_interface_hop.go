package adapters

// A module's hop through a binding it declares, behind the host interface
// (HOST_INTERFACE.md gap 14): a module calling a module on its owner's behalf.
//
// The owner's decision of 2026-10-25 (child issuance A): the child is built
// from the owner's CURRENT grants at the binding's audience, narrowed to what
// the binding declares, with the parent's actor chain replayed and the acting
// module as the last hop. A module holds nothing of its own; it inherits who it
// acts for, which is what lets a chain of modules cross audiences.
//
// Where the binding is held: where the host already holds every module's
// delivered contract, the module principal registry, as installed in this
// organisation (Service.ModuleOperationAudience, the binding the delegated
// exchange redeems). The child is sealed to the installation that binding is
// granted within (the live answer for module-operation/<module>/<binding>), so
// core's rule that a sealed binding belongs to the capability's installation
// holds by construction rather than by the owner's seal.
//
// One rule on top of A: a parent that was narrowed (an action dropped, the
// resources named) stays narrowed. No hop below it gets back what a hop above
// it gave up, so the child carries every narrowing in its own authority scopes
// for the next hop to be held to.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
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

// bindingHop issues the child of a verified parent addressed to the module
// prefix names, through that module's binding req names, to req's audience
// (requireDeclaredHopBinding has held both to the declaration).
func (s *HostAuthorityServer) bindingHop(ctx context.Context, prefix string, parent *workcontext.Verified, req *authorityv1.IssueRequest) (*gen.IssuedWorkContext, error) {
	claims := parent.Context()
	tenant, owner := claims.GetTenantId(), claims.GetOwnerPrincipalId()
	actor := business.ModulePrincipalID(prefix)
	bindingID := req.GetOperationBindingId()
	if claims.GetOwnerPrincipalKind() != business.PrincipalKindHuman {
		return nil, status.Errorf(codes.PermissionDenied, "a hop through a binding inherits its owner's current grants, which this host reads for a person; this context is owned by a %s", claims.GetOwnerPrincipalKind())
	}
	// The binding as installed in this organisation: an installation narrows
	// its declaration, and a headless-only binding does nothing as a person.
	installed, err := service.ModuleOperationAudience(ctx, business.ModuleCaller{PrincipalID: actor, BoundOrg: tenant}, tenant, prefix, bindingID)
	if err != nil {
		return nil, interfaceStatus(err)
	}
	through, err := hopOperationScopes(installed, bindingID, req.GetOperation(), req.GetAudience())
	if err != nil {
		return nil, err
	}
	// The owner's authority is re-read as the owner: the parent token is what
	// proves the owner here.
	ctx = accountsauth.WithVerifiedDatabaseIdentity(ctx, owner, tenant)
	if err := s.workContexts.requireParentStands(ctx, claims); err != nil {
		return nil, err
	}
	held, err := s.workContexts.ownerHoldsThrough(ctx, tenant, owner, req.GetAudience(), through)
	if err != nil {
		return nil, err
	}
	if len(held) == 0 {
		return nil, status.Errorf(codes.PermissionDenied, "%s holds nothing at %s through operation binding %q", owner, req.GetAudience(), bindingID)
	}
	narrowed := parentNarrowings(claims)
	ceiling := narrowed.keep(held)
	if len(ceiling) == 0 {
		return nil, status.Errorf(codes.PermissionDenied, "the parent was narrowed away from everything %s holds at %s through operation binding %q", owner, req.GetAudience(), bindingID)
	}
	scopes := ceiling
	if len(req.GetScopes()) > 0 {
		_, asked, err := workContextScopes(scopesToHost(req.GetScopes()), mintContentReads(req.GetAudience()))
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		for _, scope := range asked {
			if !workcontext.ScopeContained(scope, held) {
				return nil, status.Errorf(codes.PermissionDenied, "a child widens what its owner holds through operation binding %q: %s", bindingID, scopeString(scope))
			}
			if !workcontext.ScopeContained(scope, ceiling) {
				return nil, status.Errorf(codes.PermissionDenied, "a child widens what its parent narrowed: %s", scopeString(scope))
			}
		}
		scopes = asked
	}
	return s.workContexts.mintBindingHop(ctx, claims, actor, bindingID, req.GetAudience(), scopes, narrowed.outside(scopes), workContextTTL(int32(req.GetTtlSeconds())))
}

// requireDeclaredHopBinding refuses, before the parent is verified (and so
// before a single-use parent is spent), a binding the acting module does not
// declare and an audience that is not the binding's.
func requireDeclaredHopBinding(prefix string, req *authorityv1.IssueRequest) error {
	bindingID := req.GetOperationBindingId()
	declared, ok := service.ModulePrincipals()[business.ModulePrincipalID(prefix)].OperationAudiences[bindingID]
	if !ok {
		return status.Errorf(codes.NotFound, "operation binding %q is not declared by %s", bindingID, prefix)
	}
	if declared.Audience != req.GetAudience() {
		return status.Errorf(codes.PermissionDenied, "operation binding %q is addressed to %s, not %s", bindingID, declared.Audience, req.GetAudience())
	}
	return nil
}

// hopOperationScopes is what the installed binding may do for one operation
// of a hop: invoke (the default) or lookup. Headless is a workload's own.
func hopOperationScopes(installed business.ModuleOperationAudience, bindingID, operation, audience string) ([]*basev0.WorkScopeV1, error) {
	lookup := false
	switch operation {
	case "", "invoke":
	case "lookup":
		if len(installed.LookupScopes) == 0 {
			return nil, status.Errorf(codes.PermissionDenied, "operation binding %q declares no lookup", bindingID)
		}
		lookup = true
	case "headless":
		return nil, status.Errorf(codes.InvalidArgument, "a hop acts for its owner; headless is a workload's own operation on binding %q", bindingID)
	default:
		return nil, status.Errorf(codes.InvalidArgument, "operation %q is none of invoke, headless and lookup", operation)
	}
	_, scopes, err := workContextScopes(installed.WireScopes(lookup), mintContentReads(audience))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return scopes, nil
}

// requireParentStands refuses a parent whose authority moved since it was
// issued: the owner's revision for an owner-only parent, as every derivation
// checks, and the journal's revocation of any hop for a chained one (a module
// actor holds no row-backed authority to re-resolve; its binding is re-read
// live by core on the hop that names it).
func (s *WorkContextAuthorityServer) requireParentStands(ctx context.Context, parent *basev0.WorkContextV1) error {
	if len(parent.GetActorChain()) == 0 {
		_, err := s.requireCurrentAuthority(ctx, parent.GetTenantId(), parent)
		return err
	}
	return s.requireChainNotRevoked(ctx, parent.GetTenantId(), parent.GetActorChain())
}

// mintBindingHop mints the child: the owner's session sealed to the
// installation the binding is granted within, carrying scopes and the
// narrowings of every other kind; the parent's actors replayed; then the
// acting module's hop exercising the binding, granted scopes. It never
// outlives its parent and descends from the parent's session.
func (s *WorkContextAuthorityServer) mintBindingHop(
	ctx context.Context, parent *basev0.WorkContextV1, actor, bindingID, audience string,
	scopes, carried []*basev0.WorkScopeV1, ttl time.Duration,
) (*gen.IssuedWorkContext, error) {
	tenant := parent.GetTenantId()
	sealed := business.ModuleOperationBindingID(actor, bindingID)
	binding, err := s.seals.WorkContextOperationBinding(ctx, tenant, sealed)
	switch {
	case errors.Is(err, business.ErrNoOperationBinding):
		return nil, status.Errorf(codes.PermissionDenied, "operation binding %q is not installed for %s in this organisation", bindingID, actor)
	case err != nil:
		return nil, mapWorkContextError(ctx, err)
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	remaining := time.Unix(parent.GetExpiresAtUnix(), 0).Sub(now)
	if ttl == 0 {
		ttl = workContextDefaultTTL
	}
	ttl = min(ttl, remaining)
	if ttl < time.Second {
		return nil, status.Error(codes.PermissionDenied, "the parent expires before a child can be issued")
	}
	authority := append(cloneWorkScopes(scopes), carried...)
	hops := make([]delegationHop, 0, len(parent.GetActorChain())+1)
	for _, hop := range parent.GetActorChain() {
		hops = append(hops, delegationHop{
			PrincipalID: hop.GetPrincipalId(), PrincipalKind: hop.GetPrincipalKind(), AgentID: hop.GetAgentId(),
			DelegationID: hop.GetDelegationId(), GrantedScopes: cloneWorkScopes(authority),
		})
	}
	hops = append(hops, delegationHop{
		PrincipalID: actor, PrincipalKind: business.PrincipalKindService, DelegationID: uuid.NewString(),
		GrantedScopes: cloneWorkScopes(scopes), OperationBindingID: sealed,
	})
	token, signed, err := s.mint(ctx, mintRequest{
		Start: workcontext.StartInput{
			TenantID: tenant, OwnerPrincipalID: parent.GetOwnerPrincipalId(), OwnerPrincipalKind: parent.GetOwnerPrincipalKind(),
			OwnerAgentID: parent.GetOwnerAgentId(), OrganizationID: parent.GetOrganizationId(), TaskID: parent.GetTaskId(),
			Audience: audience, AuthorityScopes: authority, AttributionTeamIDs: append([]string(nil), parent.GetAttributionTeamIds()...),
			WorkspaceID: parent.GetWorkspaceId(), ProjectID: parent.GetProjectId(), InstallationID: binding.InstallationID,
		},
		Hops: hops, DerivedFrom: parent.GetSessionId(), Replay: workcontext.ReplayIdempotent, TTL: ttl,
	})
	if err != nil {
		return nil, mapWorkContextError(ctx, err)
	}
	if err := s.journalActorHop(ctx, signed); err != nil {
		return nil, err
	}
	return issuedWorkContext(token, signed), nil
}

// narrowings is, per resource kind, the least a parent carries of it across
// every scope set it holds (its authority scopes and each hop's grant), each
// set read as core reads it: the first scope of a kind. A kind mapped to nil
// was narrowed to nothing. A kind the parent does not carry at all is no
// narrowing: that is how a chain crosses into an audience whose kinds the
// parent never named.
type narrowings map[string]*basev0.WorkScopeV1

func parentNarrowings(parent *basev0.WorkContextV1) narrowings {
	sets := [][]*basev0.WorkScopeV1{parent.GetAuthorityScopes()}
	for _, hop := range parent.GetActorChain() {
		sets = append(sets, hop.GetGrantedScopes())
	}
	out := narrowings{}
	for _, set := range sets {
		seen := map[string]bool{}
		for _, scope := range set {
			kind := scope.GetResourceKind()
			if seen[kind] {
				continue
			}
			seen[kind] = true
			if bound, ok := out[kind]; ok {
				out[kind] = intersectScope(bound, scope)
			} else {
				out[kind] = proto.CloneOf(scope)
			}
		}
	}
	return out
}

// keep is scopes with each kind the parent narrowed held to that narrowing.
func (n narrowings) keep(scopes []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	var out []*basev0.WorkScopeV1
	for _, scope := range scopes {
		bound, narrowed := n[scope.GetResourceKind()]
		if !narrowed {
			out = append(out, scope)
			continue
		}
		if kept := intersectScope(bound, scope); kept != nil {
			out = append(out, kept)
		}
	}
	return out
}

// outside is the narrowing of every kind scopes does not name, by kind: what
// the child carries so the next hop is held to it too.
func (n narrowings) outside(scopes []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	named := map[string]bool{}
	for _, scope := range scopes {
		named[scope.GetResourceKind()] = true
	}
	var out []*basev0.WorkScopeV1
	for kind, bound := range n {
		if bound != nil && !named[kind] {
			out = append(out, proto.CloneOf(bound))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetResourceKind() < out[j].GetResourceKind() })
	return out
}

// intersectScope is what both scopes of one kind allow: the actions in both,
// and the resources in both, where naming none means any. Nothing in common is
// nil, never a scope over all of the kind.
func intersectScope(a, b *basev0.WorkScopeV1) *basev0.WorkScopeV1 {
	if a == nil || b == nil {
		return nil
	}
	var actions []string
	for _, action := range a.GetActions() {
		if slices.Contains(b.GetActions(), action) {
			actions = append(actions, action)
		}
	}
	if len(actions) == 0 {
		return nil
	}
	ids := a.GetResourceIds()
	switch {
	case len(ids) == 0:
		ids = b.GetResourceIds()
	case len(b.GetResourceIds()) > 0:
		var both []string
		for _, id := range ids {
			if slices.Contains(b.GetResourceIds(), id) {
				both = append(both, id)
			}
		}
		if len(both) == 0 {
			return nil
		}
		ids = both
	}
	return &basev0.WorkScopeV1{ResourceKind: a.GetResourceKind(), Actions: actions, ResourceIds: slices.Clone(ids)}
}

func scopeString(scope *basev0.WorkScopeV1) string {
	out := fmt.Sprintf("%s:%s", scope.GetResourceKind(), strings.Join(scope.GetActions(), ","))
	if ids := scope.GetResourceIds(); len(ids) > 0 {
		out += "[" + strings.Join(ids, ",") + "]"
	}
	return out
}
