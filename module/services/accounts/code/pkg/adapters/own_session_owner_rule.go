package adapters

import (
	"context"
	"errors"
	"fmt"
	"maps"
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

// A person's Work Context mints here (StartTask, StartRootSession and the
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
// IssueChild); what these mints project and exchange through a role-gated
// module's binding is held to that issue too (heldThroughBinding), whether the
// person acts or an agent acts for them, and through a read binding as
// through an operation binding (heldThroughReadBinding).
//
// A mint that names its scopes (StartTask) is held whoever acts, the person
// or an agent named as its actor, and carries no kind of another module the
// caller names outright (holdMint): the core signs a role's grant on another
// module's kind at that module, never onto a capability addressed to the
// unit, so here such a kind arrives only as the projection of a binding the
// mint names (operation_bindings), held at the binding's target.

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
		if servesKind(m, sc) {
			served = append(served, sc)
		}
	}
	who := authority.Person{ID: person, Organization: org}
	// What the core signs here is discarded, and its refusal is this mint's
	// own, which the mint answers and records: neither is a decision of the
	// journal's (business.AuthorityProbe).
	ctx = business.AuthorityProbe(ctx)
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

// holdMint holds a mint that names its scopes (StartTask) at audience, for
// person in organization org, whether the person acts or an agent acts for
// them: an agent's mint is the person's capability, and holds no more than
// the person's own session does (holdOwnSession), so it is refused at a
// module that declares no roles by the core's name for it and reaches such a
// module only through the binding of an installation whose role the person
// holds.
//
// At a role-gated audience it also refuses a kind the audience does not serve
// that the caller names outright. The core issues a person there the kinds
// the audience serves, on their role, and never signs a role's grant on
// another module's kind onto a capability addressed to the unit: organization
// RBAC naming the kind adds nothing. projected is what the bindings the mint
// names carry (withOperationBindingScopes), each part already held to the
// role at its binding's target, and is the one way such a kind is carried.
// The refusal is the core's own, asked the same question.
func holdMint(ctx context.Context, org, person, audience string, scopes, projected []*basev0.WorkScopeV1) error {
	m, composed, err := composedAudience(ctx, audience)
	if err != nil || !composed {
		return err
	}
	if decide.RoleGated(m) {
		var outright []*basev0.WorkScopeV1
		for _, sc := range scopes {
			if !servesKind(m, sc) && !corescope.Attenuates([]*basev0.WorkScopeV1{sc}, projected) {
				outright = append(outright, sc)
			}
		}
		if len(outright) > 0 {
			core, err := service.AuthorityCore()
			if err != nil {
				return coreStatus(err)
			}
			_, err = core.IssueForPerson(business.AuthorityProbe(ctx), authority.Person{ID: person, Organization: org}, authority.IssueRequest{Audience: m.Principal, Scopes: outright})
			if err == nil {
				// The core issues a person nothing an audience does not
				// serve; were it ever to, this mint still names no binding
				// that carries the kind.
				err = status.Errorf(codes.PermissionDenied, "%s serves no %s: another module's kind is carried only through an operation binding the mint names", m.Name, outright[0].GetResourceKind())
			}
			return coreStatus(err)
		}
	}
	return holdOwnSession(ctx, org, person, audience, scopes)
}

// servesKind reports whether m serves the kind sc names.
func servesKind(m domain.Module, sc *basev0.WorkScopeV1) bool {
	return slices.ContainsFunc(m.ResourceKinds, func(kind *basev0.WorkScopeV1) bool { return kind.GetResourceKind() == sc.GetResourceKind() })
}

// ownSession reports whether wc is a person's own session: owned by a person,
// with no actor acting for them.
func ownSession(wc *basev0.WorkContextV1) bool {
	return wc.GetOwnerPrincipalKind() == business.PrincipalKindHuman && len(wc.GetActorChain()) == 0
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

// heldThroughBinding is what the authority core would issue person, in
// organization org, at the target of binding of m, a role-gated module, on
// the roles they hold on m's installation: the core is asked as a hop asks
// it (IssueForPerson naming the binding, then IssueChild over it), so a
// role's scopes are read at the binding's target (decide.RoleScopesAt), a
// grant addressed to one module is held at that module alone, and the reach
// the installation's consent approved bounds it. Owner selection aside, as
// in holdOwnSession. Both operations a person causes are asked, since these
// mints carry a binding's invoke or its lookup scopes. What the core signs to
// answer is discarded; its refusal is returned as the core names it.
func heldThroughBinding(ctx context.Context, org, person string, m domain.Module, binding string) ([]*basev0.WorkScopeV1, error) {
	core, err := service.AuthorityCore()
	if err != nil {
		return nil, coreStatus(err)
	}
	ctx = business.AuthorityProbe(ctx)
	root, err := core.IssueForPerson(ctx, authority.Person{ID: person, Organization: org}, authority.IssueRequest{Audience: m.Principal, Binding: binding})
	if err != nil {
		return nil, coreStatus(err)
	}
	var held []*basev0.WorkScopeV1
	var refusal error
	for _, operation := range []string{"invoke", "lookup"} {
		child, err := core.IssueChild(ctx, root.Token, authority.Workload{Module: m.Name}, authority.IssueRequest{Binding: binding, Operation: operation})
		if err != nil {
			if refusal == nil {
				refusal = err
			}
			continue
		}
		effective := child.Context.GetAuthorityScopes()
		if chain := child.Context.GetActorChain(); len(chain) > 0 {
			effective = chain[len(chain)-1].GetGrantedScopes()
		}
		held = append(held, effective...)
	}
	if len(held) == 0 {
		return nil, coreStatus(refusal)
	}
	return corescope.Unowned(held), nil
}

// heldThroughReadBinding is what the authority core would issue person at
// audience through m, a role-gated module, when m exercises a read binding
// there. The core declares no read binding: what it issues a person at a
// module through m is what m's operation bindings addressed to that module
// carry (heldThroughBinding), so a read binding carries a person's capability
// only where one of those does, and no further than they do. Without this a
// read binding addressed to a sibling carried there a grant the role holds at
// one module alone, and carried anything an organisation role names for a
// person whose role holds nothing at either. composed is false when the core
// composes nothing by audience (one of the host's own), where no rule of the
// core's applies. A module declaring no operation binding to audience holds
// nothing there for anybody.
func heldThroughReadBinding(ctx context.Context, org, person string, m domain.Module, audience string) (held []*basev0.WorkScopeV1, composed bool, err error) {
	target, composed, err := composedAudience(ctx, audience)
	if err != nil || !composed {
		return nil, composed, err
	}
	for _, id := range slices.Sorted(maps.Keys(m.Bindings)) {
		if m.Bindings[id].Audience != target.Principal {
			continue
		}
		through, err := heldThroughBinding(ctx, org, person, m, id)
		if err != nil && !coreRefused(err) {
			return nil, true, err
		}
		held = append(held, through...)
	}
	return held, true, nil
}

// personsCapability reports whether wc is a person's capability with nobody
// but the person's agents acting on it: their own session, or one a registered
// agent acts on for them (StartTask naming an actor, StartChildSession). A
// module's hop, or a delegation's, is another unit of authority.
func personsCapability(wc *basev0.WorkContextV1) bool {
	if wc.GetOwnerPrincipalKind() != business.PrincipalKindHuman {
		return false
	}
	for _, actor := range wc.GetActorChain() {
		if actor.GetPrincipalKind() != business.PrincipalKindAgent {
			return false
		}
	}
	return true
}

// effectiveScopes is what wc carries in effect: its last hop's granted
// scopes, or its owner's when the owner acts directly.
func effectiveScopes(wc *basev0.WorkContextV1) []*basev0.WorkScopeV1 {
	if chain := wc.GetActorChain(); len(chain) > 0 {
		return chain[len(chain)-1].GetGrantedScopes()
	}
	return wc.GetAuthorityScopes()
}

// coreRefused reports whether err is the core's refusal of the ask, as
// against a failure to read what decides it: a refusal is an answer ("holds
// nothing there"), and a failure is never one.
func coreRefused(err error) bool {
	switch status.Code(err) {
	case codes.PermissionDenied, codes.NotFound, codes.FailedPrecondition:
		return true
	}
	return false
}

// holdOwnSessionExchange holds what a person's capability is re-issued with
// at audience. The owner's own exchange (no module exercising a binding)
// carries their own session there as their own session, refused by the
// rule's name. A module that declares no roles exchanging a person's own
// session through its binding carries nothing of the person's own to a
// module that declares no roles either. A role-gated module exchanging a
// person's capability through its binding carries what the core would issue
// the person over it (heldThroughBinding; through a read binding,
// heldThroughReadBinding): what their role on the module's installation holds
// at the binding's target, so a grant addressed to one module is carried to
// no sibling, and an organisation role over the kind carries nothing a role
// on the installation does not hold. That is decided by the person's role
// whoever acts: a capability an agent acts on for the person carries through
// a role-gated module's binding no more than the person's own session does.
// Scopes empty re-issue the parent's.
func holdOwnSessionExchange(ctx context.Context, parent *basev0.WorkContextV1, audience string, scopes []*basev0.WorkScopeV1, exercised moduleBindingSeal) ([]*basev0.WorkScopeV1, error) {
	if !personsCapability(parent) {
		return scopes, nil
	}
	carried := scopes
	if len(carried) == 0 {
		carried = effectiveScopes(parent)
	}
	if exercised == (moduleBindingSeal{}) {
		if !ownSession(parent) {
			return scopes, nil
		}
		return scopes, holdOwnSession(ctx, parent.GetTenantId(), parent.GetOwnerPrincipalId(), audience, carried)
	}
	m, composed, err := composedAudience(ctx, parent.GetAudience())
	if err != nil || !composed {
		return scopes, err
	}
	if !decide.RoleGated(m) {
		if !ownSession(parent) {
			return scopes, nil
		}
		return heldThrough(ctx, audience, carried)
	}
	var held []*basev0.WorkScopeV1
	through := "a read binding"
	if exercised.BindingID == "" {
		var targeted bool
		if held, targeted, err = heldThroughReadBinding(ctx, parent.GetTenantId(), parent.GetOwnerPrincipalId(), m, audience); err != nil || !targeted {
			return scopes, err
		}
	} else {
		through = fmt.Sprintf("operation binding %q", exercised.BindingID)
		if held, err = heldThroughBinding(ctx, parent.GetTenantId(), parent.GetOwnerPrincipalId(), m, exercised.BindingID); err != nil {
			return nil, err
		}
	}
	if !corescope.Attenuates(corescope.Unowned(carried), held) {
		return nil, status.Errorf(codes.PermissionDenied,
			"the person's role on %s holds less at %s than %s carries there: a role's grant is held at the module it is addressed to, and an organisation role adds nothing to it",
			m.Name, audience, through)
	}
	return scopes, nil
}
