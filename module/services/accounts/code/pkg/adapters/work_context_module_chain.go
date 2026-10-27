package adapters

// The native Work Context authority's re-read of a module chain: every module
// hop a capability carries is held to the binding it hopped through, and a
// module principal is stored under its registry id.

import (
	"context"
	"errors"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

// storedPrincipal is the principal as storage holds it: a module a capability
// names by its name is stored under its registry id (the identity columns and
// grant subjects are ids), and every other principal is itself.
func storedPrincipal(principal string) string {
	if service != nil {
		if id, isModule := service.ModuleRegistryID(principal); isModule {
			return id
		}
	}
	return principal
}

// requireModuleChainStands re-reads every module hop of a capability against
// the binding it hopped through (business.ConfirmModuleChain): core re-reads
// only the binding the capability seals, the last hop's, so a binding narrowed
// or withdrawn upstream would otherwise go unnoticed until the child expired.
//
// Each hop is held to the exact scope set it was issued under (invoke, lookup
// or headless), and the original granted kinds recorded beside its binding.
// Missing journal provenance refuses the chain.
func (s *WorkContextAuthorityServer) requireModuleChainStands(ctx context.Context, wc *basev0.WorkContextV1) error {
	chain := wc.GetActorChain()
	// A chain of hops through bindings seals its last hop's module binding; a
	// context sealed to a source delegation or an installed read is confirmed
	// by its own checker.
	if len(chain) == 0 || !business.NamesModuleOperationBinding(wc.GetOperationBinding().GetBindingId()) {
		return nil
	}
	if service == nil {
		return status.Error(codes.Unavailable, "no module authority is wired, so a module chain cannot be re-read")
	}
	recorded := map[string]business.ActorChainHopBinding{}
	if reader, ok := s.journal.(business.ActorChainHopBindingReader); ok && reader != nil {
		var err error
		if recorded, err = reader.ActorChainHopBindings(ctx, wc.GetTenantId(), journalKeys(wc.GetTaskId(), chain)); err != nil {
			wool.Get(ctx).In("requireModuleChainStands").Warn("the bindings this chain went through could not be read",
				wool.Field("org", wc.GetTenantId()), wool.ErrField(err))
			return status.Error(codes.Unavailable, "cannot read the bindings this chain went through")
		}
	}
	hops := make([]business.ModuleChainHop, 0, len(chain))
	for _, hop := range chain {
		// The per-issue row of a delegation's hop is the one that issue wrote;
		// the hop's own id is every other hop's.
		through, issued := recorded[delegatedHopRowID(hop.GetDelegationId(), wc.GetTaskId())]
		if !issued {
			through = recorded[hop.GetDelegationId()]
		}
		hops = append(hops, business.ModuleChainHop{
			Principal: hop.GetPrincipalId(), Scopes: operationScopesFromCore(hop.GetGrantedScopes()),
			Through: through,
		})
	}
	if err := service.ConfirmModuleChain(ctx, wc.GetTenantId(), wc.GetAudience(), hops); err != nil {
		if errors.Is(err, business.ErrModuleHopStale) {
			return status.Error(codes.PermissionDenied, "a binding a hop of this chain went through no longer grants what it carries")
		}
		return interfaceStatus(err)
	}
	return nil
}
