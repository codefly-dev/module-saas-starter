package adapters

import (
	"context"
	"slices"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// A solution's roles decide what a person holds through it
// (business.PersonSolutionRoles): at the solution's audience, the person's
// roles' grants on its own kinds; at a module the solution reaches for them,
// those grants within the binding. Organisation roles answer only where no
// solution in the path declares roles.

// solutionRoleReader answers what a person holds through a solution's roles.
type solutionRoleReader interface {
	PersonSolutionRoles(ctx context.Context, orgID, personID, principal string) (*business.SolutionAuthority, []*basev0.WorkScopeV1, error)
}

func (s *HostAuthorityServer) solutionRoles() solutionRoleReader {
	if s.roles != nil {
		return s.roles
	}
	return service
}

// solutionPersonIssue issues a person's capability at a solution whose roles
// decide it: everything those roles grant on the solution's own kinds for an
// empty ask, an ask within it otherwise.
func (s *HostAuthorityServer) solutionPersonIssue(ctx context.Context, orgID, audience string, authority *business.SolutionAuthority, held []*basev0.WorkScopeV1, asked []*gen.WorkContextScope, ttl int32) (*gen.IssuedWorkContext, error) {
	held = authority.Own(held)
	if len(held) == 0 {
		return nil, status.Errorf(codes.PermissionDenied, "the person holds nothing at %s: no role of %s is theirs at its installation", audience, audience)
	}
	scopes := held
	if len(asked) > 0 {
		_, wanted, err := workContextScopes(asked, mintContentReads(audience))
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		for _, scope := range wanted {
			if !workcontext.ScopeContained(scope, held) {
				return nil, status.Errorf(codes.PermissionDenied, "the person's roles at %s do not grant %s", audience, scopeString(scope))
			}
		}
		scopes = wanted
	}
	return s.workContexts.startRoleTask(ctx, orgID, audience, scopes, ttl)
}

// solutionBindingIssue issues a person's capability at a solution whose roles
// decide it, naming one of the solution's bindings: beside the roles' grants on
// the solution's own kinds it carries what those roles grant within the
// binding, as installed in the organisation, so the solution's hop through it
// is held to the person. An empty ask is all of that; an ask beyond it is
// refused by name, and a person whose roles grant nothing through the binding
// is refused for that.
func (s *HostAuthorityServer) solutionBindingIssue(ctx context.Context, orgID, audience, bindingID string, authority *business.SolutionAuthority, held []*basev0.WorkScopeV1, asked []*gen.WorkContextScope, ttl int32) (*gen.IssuedWorkContext, error) {
	installed, err := service.ModuleOperationAudience(ctx, business.ModuleCaller{PrincipalID: business.ModulePrincipalID(audience), BoundOrg: orgID}, orgID, audience, bindingID)
	if err != nil {
		return nil, interfaceStatus(err)
	}
	invokes, err := hopOperationScopes(installed, bindingID, "invoke", installed.Audience)
	if err != nil {
		return nil, err
	}
	through := business.IntersectWorkScopes(held, invokes)
	if len(through) == 0 {
		return nil, status.Errorf(codes.PermissionDenied, "the person holds nothing at %s through operation binding %q: no role of %s grants them what it carries", installed.Audience, bindingID, audience)
	}
	ceiling := append(authority.Own(held), through...)
	scopes := ceiling
	if len(asked) > 0 {
		_, wanted, err := workContextScopes(asked, mintContentReads(audience))
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		for _, scope := range wanted {
			if !workcontext.ScopeContained(scope, ceiling) {
				return nil, status.Errorf(codes.PermissionDenied, "asked beyond what the person's roles at %s grant through operation binding %q: %s", audience, bindingID, scopeString(scope))
			}
		}
		scopes = wanted
	}
	return s.workContexts.startRoleTask(ctx, orgID, audience, scopes, ttl)
}

// startRoleTask mints a person's capability whose scopes their solution roles
// were held to: the membership, revision and team attribution are the owner's
// own, read as every mint reads them, and the capability is sealed to the
// audience's installation, whose revision moves with its exposure.
func (s *WorkContextAuthorityServer) startRoleTask(ctx context.Context, orgID, audience string, scopes []*basev0.WorkScopeV1, ttl int32) (*gen.IssuedWorkContext, error) {
	if _, confined := accountsauth.VerifiedResourceConfinement(ctx); confined {
		return nil, status.Errorf(codes.PermissionDenied, "a resource-confined credential mints only within its consumer's ceiling, and %s's roles are no such ceiling", audience)
	}
	if err := requireVocabularyAudience(ctx, audience); err != nil {
		return nil, err
	}
	ownerID, err := s.authorizeOwner(ctx, orgID)
	if err != nil {
		return nil, err
	}
	facts, err := s.resolveAuthority(ctx, orgID, ownerID, "", nil)
	if err != nil {
		return nil, err
	}
	installation, err := s.audienceInstallation(ctx, orgID, audience)
	if err != nil {
		return nil, err
	}
	token, signed, err := s.mint(ctx, mintRequest{
		Start: workcontext.StartInput{
			TenantID: orgID, OwnerPrincipalID: ownerID, OwnerPrincipalKind: business.PrincipalKindHuman, OrganizationID: orgID,
			TaskID: uuid.NewString(), Audience: audience, AuthorityScopes: scopes, AttributionTeamIDs: facts.AttributionTeamIDs,
			InstallationID: installation,
		},
		Session: callerSession(ctx), Replay: workcontext.ReplayIdempotent, TTL: workContextTTL(ttl),
	})
	if err != nil {
		return nil, mapWorkContextError(ctx, err)
	}
	return issuedWorkContext(token, signed), nil
}

// solutionCeiling is what the owner's roles grant through every solution on a
// hop's path (the acting module and each module actor before it), met across
// them; bounded is false when no solution on the path declares roles.
func (s *HostAuthorityServer) solutionCeiling(ctx context.Context, orgID, owner string, path []string) (ceiling []*basev0.WorkScopeV1, bounded bool, err error) {
	seen := map[string]bool{}
	for _, principal := range path {
		if seen[principal] {
			continue
		}
		seen[principal] = true
		authority, held, err := s.solutionRoles().PersonSolutionRoles(ctx, orgID, owner, principal)
		if err != nil {
			return nil, false, interfaceStatus(err)
		}
		if authority == nil {
			continue
		}
		if bounded {
			ceiling = business.IntersectWorkScopes(ceiling, held)
		} else {
			ceiling = held
		}
		bounded = true
	}
	return ceiling, bounded, nil
}

// solutionPath is the acting module and every module actor the parent names.
func solutionPath(parent *basev0.WorkContextV1, acting string) []string {
	path := []string{acting}
	for _, hop := range parent.GetActorChain() {
		if hop.GetPrincipalKind() == business.PrincipalKindService {
			path = append(path, hop.GetPrincipalId())
		}
	}
	return path
}

// solutionHoldsThrough is what the owner holds at audience through a binding
// (through) when a solution's roles decide it: the roles of the solutions on
// the path within the binding, and, at a solution audience that declares
// roles, that solution's own grant as well. decided is false when organisation
// roles answer instead.
func (s *HostAuthorityServer) solutionHoldsThrough(ctx context.Context, parent *basev0.WorkContextV1, acting, audience string, through []*basev0.WorkScopeV1) (held []*basev0.WorkScopeV1, decided bool, err error) {
	tenant, owner := parent.GetTenantId(), parent.GetOwnerPrincipalId()
	ceiling, bounded, err := s.solutionCeiling(ctx, tenant, owner, solutionPath(parent, acting))
	if err != nil {
		return nil, false, err
	}
	target, targetHeld, err := s.solutionRoles().PersonSolutionRoles(ctx, tenant, owner, audience)
	if err != nil {
		return nil, false, interfaceStatus(err)
	}
	if !bounded && target == nil {
		return nil, false, nil
	}
	held = slices.Clone(through)
	if bounded {
		held = business.IntersectWorkScopes(held, ceiling)
	}
	if target != nil {
		held = business.IntersectWorkScopes(held, target.Own(targetHeld))
	}
	return held, true, nil
}

// solutionParentStands is requireParentStands for an owner-only parent issued
// at a solution whose roles decided it: what it carries must still be what the
// owner's roles grant there, at the owner's current revision. decided is false
// for any other parent.
func (s *HostAuthorityServer) solutionParentStands(ctx context.Context, parent *basev0.WorkContextV1) (decided bool, err error) {
	if len(parent.GetActorChain()) > 0 {
		return false, nil
	}
	tenant, owner := parent.GetTenantId(), parent.GetOwnerPrincipalId()
	authority, held, err := s.solutionRoles().PersonSolutionRoles(ctx, tenant, owner, parent.GetAudience())
	if err != nil {
		return true, interfaceStatus(err)
	}
	if authority == nil {
		return false, nil
	}
	if !workcontext.ScopesAttenuate(parent.GetAuthorityScopes(), authority.Own(held)) {
		return true, status.Errorf(codes.PermissionDenied, "the parent carries more than %s's roles at %s now grant", owner, parent.GetAudience())
	}
	facts, err := s.workContexts.resolveAuthority(ctx, tenant, owner, "", nil)
	if err != nil {
		return true, err
	}
	if facts.EffectiveRevision() != parent.GetAuthorizationRevision() {
		return true, status.Error(codes.FailedPrecondition, "parent Work Context authorization revision is stale")
	}
	return true, nil
}
