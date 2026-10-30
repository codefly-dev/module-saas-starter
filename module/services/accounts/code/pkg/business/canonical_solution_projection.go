package business

import (
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

// canonicalSolutionEntitlement projects a decision already made by HeldInstallations.
// The catalogue supplies routing identity, never a grant. Match its verified
// binding principal and generation to the core's delivery before exposing it.
// A solution has no legacy scope node; inventing one would imply an RBAC grant.
// A module page that declares roles is projected exactly as a solution is
// (decide.RoleGated); a module without roles, which every module without a
// page is, never is here.
func canonicalSolutionEntitlement(org string, held authority.HeldInstallation, module domain.Module, target *AvailableSolutionTarget, bindingPrincipal string) *gen.SolutionEntitlement {
	inst := held.Installation
	if target == nil || inst.ID == "" || inst.Organization != org || inst.Status != domain.Active || len(held.Roles) == 0 ||
		!decide.RoleGated(module) || module.Name != inst.Target || module.Principal == "" ||
		target.Kind != module.Kind || target.TargetID == "" || target.BindingID == "" || target.RouteAlias != module.Name ||
		bindingPrincipal != module.Principal || target.AppliedGeneration == 0 || target.AppliedGeneration != module.Generation {
		return nil
	}
	return &gen.SolutionEntitlement{TargetId: target.TargetID, InstallationId: inst.ID, Healthy: true}
}
