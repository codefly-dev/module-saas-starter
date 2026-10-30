package business

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

// canonicalSolutionEntitlements uses the same member decision as the shared
// Setup UI, for every installation whose roles decide: a solution's, and a
// module page's that declares roles. Installation ownership and its machine role grant nobody access.
// The caller here is the gateway's authenticated effective subject, admitted by
// SolutionEntitlementServer's internal credential guard, not browser input.
func (s *Service) canonicalSolutionEntitlements(ctx context.Context, snapshot *installedModuleRegistry, org, subject string) ([]*gen.SolutionEntitlement, error) {
	core, err := s.AuthorityCore()
	if err != nil {
		return nil, err
	}
	held, err := core.HeldInstallations(ctx, authority.Person{ID: subject, Organization: org}, org)
	if err != nil {
		return nil, err
	}
	var result []*gen.SolutionEntitlement
	for _, h := range held {
		module, err := core.Composed(ctx, h.Installation.Target)
		if code, ok := domain.CodeOf(err); ok && code == domain.NotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !decide.RoleGated(module) {
			continue
		}
		installation, err := s.roleGatedInstallation(ctx, org, module, h.Installation)
		if err != nil {
			return nil, err
		}
		if installation == nil {
			continue
		}
		var targets []*AvailableSolutionTarget
		err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			targets, err = s.store.ListAvailableSolutionTargets(ctx, AvailableSolutionQuery{OrgID: org, TargetID: installation.GetTargetId(), Limit: 1})
			return err
		})
		if err != nil {
			return nil, err
		}
		if len(targets) != 1 || targets[0].Kind != module.Kind || targets[0].RouteAlias != module.Name {
			continue
		}
		target := targets[0]
		_, grant, err := installationRegistryEntry(snapshot, target.BindingID, org)
		if err != nil {
			return nil, err
		}
		// Registry keys are this host's UUID projection. Prefix retains the
		// delivered principal that the shared authority names on the wire.
		if entry := canonicalSolutionEntitlement(org, h, module, target, grant.Prefix); entry != nil {
			result = append(result, entry)
		}
	}
	if err := s.fenceModuleRegistrySnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	return result, nil
}

// roleGatedInstallation is this host's active, healthy record of held, an
// installation whose roles decide, or nil. A solution's is the core's
// canonical record, at the revision the core read; a module page's is
// accounts' own module installation, which the core reads as its own.
func (s *Service) roleGatedInstallation(ctx context.Context, org string, module domain.Module, held domain.Installation) (*gen.Installation, error) {
	if module.Kind == domain.KindSolution {
		canonical, err := s.canonicalSolutionSummaries(ctx, org, held.ID, "", gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, 1)
		if err != nil || len(canonical) != 1 || canonical[0].Health != gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY ||
			canonical[0].GetInstallation().GetRevision() != held.Revision {
			return nil, err
		}
		return canonical[0].GetInstallation(), nil
	}
	var installation *gen.Installation
	var health gen.InstallationHealth
	if err := s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		var err error
		installation, health, err = s.installationStore().GetInstallation(ctx, org, held.ID)
		return err
	}); err != nil {
		return nil, err
	}
	if installation.GetStatus() != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE || health != gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY {
		return nil, nil
	}
	return installation, nil
}
