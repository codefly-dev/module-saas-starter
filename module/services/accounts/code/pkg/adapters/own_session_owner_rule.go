package adapters

import (
	"context"
	"errors"
	"slices"

	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

// A person's own Work Context mints here (StartTask, StartRootSession and the
// owner's ExchangeAudience) draw on accounts' organization RBAC, beside the
// host Issue the shared authority core decides (IssueForPerson). So they are
// held to the core's rule for what organization roles reach on a person's own
// session (RBAC-4a): at an audience the core composed that checks owners and
// declares no roles, none of the kinds it serves and does not declare shared.
// The rule is the core's own (decide.OwnSession), read against the module as
// the core composed it, never restated here. A role-gated audience, a solution
// or a module page, is decided by its roles, which these mints do not read, so
// the rule leaves it as it was.

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

// ownSessionRule is the core's answer for scopes a person's own session
// carries at m: nil when the rule holds them, otherwise the core's named
// refusal. It starts from decide.Issue's refusal of an ask an organization
// role holds nothing of, and decide.OwnSession answers that refusal itself
// unless the scopes name a kind m owns at an audience that checks owners and
// is not role-gated.
func ownSessionRule(m domain.Module, scopes []*basev0.WorkScopeV1) error {
	refused := domain.Refuse("asked beyond what the owner holds")
	if named := decide.OwnSession(m, scopes, refused); named != refused {
		return named
	}
	return nil
}

// holdOwnSession refuses, PermissionDenied and by the rule's name, a person's
// own capability at audience that carries a kind the core's owner rule keeps
// from organization roles there.
func holdOwnSession(ctx context.Context, audience string, scopes []*basev0.WorkScopeV1) error {
	m, composed, err := composedAudience(ctx, audience)
	if err != nil || !composed {
		return err
	}
	return coreStatus(ownSessionRule(m, scopes))
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
// that declares no roles to audience, narrowed to what the core's owner rule
// holds there: every scope but those of a kind audience owns. When nothing is
// held, it refuses by the rule's name.
func heldThrough(ctx context.Context, audience string, scopes []*basev0.WorkScopeV1) ([]*basev0.WorkScopeV1, error) {
	target, composed, err := composedAudience(ctx, audience)
	if err != nil || !composed {
		return scopes, err
	}
	held := slices.DeleteFunc(slices.Clone(scopes), func(sc *basev0.WorkScopeV1) bool {
		return ownSessionRule(target, []*basev0.WorkScopeV1{sc}) != nil
	})
	if len(held) == 0 && len(scopes) > 0 {
		return nil, coreStatus(ownSessionRule(target, scopes))
	}
	return held, nil
}

// holdOwnSessionExchange holds what a person's own session is re-issued with
// at audience. The owner's own exchange (no module exercising a binding)
// carries it there as their own session, refused by the rule's name when it
// names an owned kind. A module exchanging it through its binding carries only
// what the person holds at the binding's audience, when that module declares
// no roles. Scopes empty re-issue the parent's.
func holdOwnSessionExchange(ctx context.Context, parent *basev0.WorkContextV1, audience string, scopes []*basev0.WorkScopeV1, exercised moduleBindingSeal) ([]*basev0.WorkScopeV1, error) {
	if !ownSession(parent) {
		return scopes, nil
	}
	carried := scopes
	if len(carried) == 0 {
		carried = parent.GetAuthorityScopes()
	}
	if exercised == (moduleBindingSeal{}) {
		return scopes, holdOwnSession(ctx, audience, carried)
	}
	through, err := ownSessionThrough(ctx, parent.GetAudience())
	if err != nil || !through {
		return scopes, err
	}
	return heldThrough(ctx, audience, carried)
}
