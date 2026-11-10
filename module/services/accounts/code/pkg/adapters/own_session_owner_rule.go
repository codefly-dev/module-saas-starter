package adapters

import (
	"context"
	"errors"

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

// composedAudience is the module the core composed under audience. ok is false
// when it composes nothing by that name (the host's own audiences, a solution
// binding's) or when this host builds no core, which composes nothing: no rule
// of the core's then applies. Any other failure to read is Unavailable, since
// a declaration that cannot be read is not one that checks no owner.
func composedAudience(ctx context.Context, audience string) (domain.Module, bool, error) {
	if service == nil {
		return domain.Module{}, false, nil
	}
	core, err := service.AuthorityCore()
	if errors.Is(err, business.ErrAuthorityCoreUnconfigured) {
		return domain.Module{}, false, nil
	}
	if err != nil {
		return domain.Module{}, false, status.Error(codes.Unavailable, "cannot read what the authority core composed")
	}
	m, err := core.Composed(ctx, audience)
	if code, refused := domain.CodeOf(err); refused && code == domain.NotFound {
		return domain.Module{}, false, nil
	}
	if err != nil {
		return domain.Module{}, false, status.Errorf(codes.Unavailable, "cannot read what the authority core composed for %q", audience)
	}
	return m, true, nil
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
