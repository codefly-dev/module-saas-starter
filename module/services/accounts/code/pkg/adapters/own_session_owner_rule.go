package adapters

import (
	"context"
	"errors"
	"slices"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	corescope "github.com/codefly-dev/authority/libraries/authority-core/go/scope"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

// A person's own Work Context mints here (StartTask, StartRootSession and the
// owner's ExchangeAudience) draw on accounts' organization RBAC, beside the
// host Issue the shared authority core decides (IssueForPerson). An
// organisation role decides administration only and reaches no module, so at
// an audience the core composed these mints are held to what the core itself
// would issue the person on their own session (holdOwnSession): nothing at a
// module that declares no roles, refused by the core's own name for it
// (decide.NoRoles), and at a role-gated one, a solution or a module page,
// what their role on its installation holds. A module that declares no roles
// is reached through the binding of an installation whose role the person
// holds, which the core issues (IssueForPerson naming the binding,
// IssueChild).

// composedAudience is the module the core composed under audience, read by
// principal as the core reads an audience. ok is false when the core composes
// nothing by that principal and this host serves no module by it (the host's
// own audiences, a solution binding's), or when this host builds no core,
// which composes nothing: no rule of the core's then applies.
func composedAudience(ctx context.Context, audience string) (domain.Module, bool, error) {
	if service == nil {
		return domain.Module{}, false, nil
	}
	m, err := service.ComposedAudience(ctx, audience)
	if errors.Is(err, business.ErrAuthorityCoreUnconfigured) {
		return domain.Module{}, false, nil
	}
	served := false
	if notComposed(err) {
		var serr error
		if served, serr = service.ServesModule(ctx, audience); serr != nil {
			return domain.Module{}, false, status.Errorf(codes.Unavailable, "cannot read whether this host serves %q", audience)
		}
	}
	return composedBy(m, err, served, audience)
}

// composedBy is what the core's read of audience answered. A module this host
// serves that the core does not compose (a delivery the core has not composed
// yet) is Unavailable, as is any other failure to read: a declaration that
// cannot be read is not one that checks no owner.
func composedBy(m domain.Module, err error, served bool, audience string) (domain.Module, bool, error) {
	if notComposed(err) {
		if !served {
			return domain.Module{}, false, nil
		}
		return domain.Module{}, false, status.Errorf(codes.Unavailable,
			"this host serves %q and the authority core does not compose it yet, so what it checks is unknown", audience)
	}
	if err != nil {
		return domain.Module{}, false, status.Errorf(codes.Unavailable, "cannot read what the authority core composed for %q", audience)
	}
	return m, true, nil
}

// notComposed reports whether err is the core's answer that it composes no
// module by that principal.
func notComposed(err error) bool {
	code, refused := domain.CodeOf(err)
	return errors.Is(err, domain.ErrNotFound) || (refused && code == domain.NotFound)
}

// ownSessionRule is the core's answer for a person's own session at m: its
// named refusal (decide.NoRoles) at a module that declares no roles, where a
// person's own session holds nothing whatever is asked, and nil at a
// role-gated one, which its roles decide.
func ownSessionRule(m domain.Module) error {
	if !decide.RoleGated(m) {
		return decide.NoRoles(m)
	}
	return nil
}

// holdOwnSession holds person's own capability at audience, in organization
// org and carrying scopes, to what the authority core would issue them there
// on their own session: the core is asked (IssueForPerson) and its refusal is
// this mint's, by the core's own name for it. So a composed module that
// declares no roles holds nothing for anybody, and a role-gated one holds
// what the person's role on its installation holds and nothing an
// organisation role would add. Owner selection aside: this mint names no
// owner, and the audience selects one when it verifies. The core is asked about the kinds the audience
// serves: a scope of another module's kind is inert at this audience, and is
// decided where a binding carries it. What the core signs to answer is
// discarded: the capability handed out is the one this mint makes.
func holdOwnSession(ctx context.Context, org, person, audience string, scopes []*basev0.WorkScopeV1) error {
	m, composed, err := composedAudience(ctx, audience)
	if err != nil || !composed {
		return err
	}
	core, err := service.AuthorityCore()
	if err != nil {
		return coreStatus(err)
	}
	var served []*basev0.WorkScopeV1
	for _, sc := range scopes {
		if slices.ContainsFunc(m.ResourceKinds, func(kind *basev0.WorkScopeV1) bool { return kind.GetResourceKind() == sc.GetResourceKind() }) {
			served = append(served, sc)
		}
	}
	who := authority.Person{ID: person, Organization: org}
	_, err = core.IssueForPerson(ctx, who, authority.IssueRequest{Audience: m.Principal, Scopes: served})
	if err == nil || len(served) == 0 {
		return coreStatus(err)
	}
	// These mints name no owner: their scopes carry a kind, its actions and
	// its resources, and the audience selects owners when it verifies. At a
	// module page that checks owners the core holds a role's own kinds on the
	// installation's principal, so it refuses the ask above for naming no
	// owner. That is not a refusal of what the person holds: the ask is held
	// to everything the core would issue them there, owner aside, and the
	// core's first refusal stands when it is beyond that too.
	whole, wholeErr := core.IssueForPerson(ctx, who, authority.IssueRequest{Audience: m.Principal})
	if wholeErr == nil && corescope.Attenuates(corescope.Unowned(served), corescope.Unowned(whole.Context.GetAuthorityScopes())) {
		return nil
	}
	return coreStatus(err)
}

// ownSession reports whether wc is a person's own session: owned by a person,
// with no actor acting for them.
func ownSession(wc *basev0.WorkContextV1) bool {
	return wc.GetOwnerPrincipalKind() == business.PrincipalKindHuman && len(wc.GetActorChain()) == 0
}

// ownSessionThrough reports whether a person's own session reaches past
// module through its bindings as their own session, as the core reads a hop
// (throughSolutions): through a composed module that declares no roles. A
// role-gated module is decided by its roles, which these mints do not read.
func ownSessionThrough(ctx context.Context, module string) (bool, error) {
	m, composed, err := composedAudience(ctx, module)
	return composed && !decide.RoleGated(m), err
}

// heldThrough is scopes, carried by a person's own session through a module
// that declares no roles to audience: nothing at an audience that declares no
// roles either, refused by the rule's name.
func heldThrough(ctx context.Context, audience string, scopes []*basev0.WorkScopeV1) ([]*basev0.WorkScopeV1, error) {
	target, composed, err := composedAudience(ctx, audience)
	if err != nil || !composed {
		return scopes, err
	}
	if err := ownSessionRule(target); err != nil && len(scopes) > 0 {
		return nil, coreStatus(err)
	}
	return scopes, nil
}

// holdOwnSessionExchange holds what a person's own session is re-issued with
// at audience. The owner's own exchange (no module exercising a binding)
// carries it there as their own session, refused by the rule's name. A module
// that declares no roles exchanging it through its binding carries nothing of
// the person's own to a module that declares no roles either. Scopes empty
// re-issue the parent's.
func holdOwnSessionExchange(ctx context.Context, parent *basev0.WorkContextV1, audience string, scopes []*basev0.WorkScopeV1, exercised moduleBindingSeal) ([]*basev0.WorkScopeV1, error) {
	if !ownSession(parent) {
		return scopes, nil
	}
	carried := scopes
	if len(carried) == 0 {
		carried = parent.GetAuthorityScopes()
	}
	if exercised == (moduleBindingSeal{}) {
		return scopes, holdOwnSession(ctx, parent.GetTenantId(), parent.GetOwnerPrincipalId(), audience, carried)
	}
	through, err := ownSessionThrough(ctx, parent.GetAudience())
	if err != nil || !through {
		return scopes, err
	}
	return heldThrough(ctx, audience, carried)
}
