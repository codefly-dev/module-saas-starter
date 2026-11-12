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
// SolutionEntitlementServer's internal credential guard, not browser input,
// asking from the session the gateway verified: the core reads them under that
// session's role view, so a cast is answered as its team (entitlementViewerOf).
func (s *Service) canonicalSolutionEntitlements(ctx context.Context, snapshot *installedModuleRegistry, who authority.Person) ([]*gen.SolutionEntitlement, error) {
	org := who.Organization
	core, err := s.AuthorityCore()
	if err != nil {
		return nil, err
	}
	held, err := core.HeldInstallations(ctx, who, org)
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
// canonical record, read at the revision the core read, its health the
// record's own, following the target's release line. A module page's is its
// composition installation, this host's own module record, entitling by
// grants alone: active is all it needs.
func (s *Service) roleGatedInstallation(ctx context.Context, org string, module domain.Module, held domain.Installation) (*gen.Installation, error) {
	if module.Kind != domain.KindSolution {
		inst, _, err := s.GetInstallation(ctx, org, held.ID)
		if err != nil || inst.GetStatus() != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
			return nil, err
		}
		return inst, nil
	}
	canonical, err := s.canonicalSolutionSummaries(ctx, org, held.ID, "", gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, 1)
	if err != nil || len(canonical) != 1 || canonical[0].Health != gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY ||
		canonical[0].GetInstallation().GetRevision() != held.Revision {
		return nil, err
	}
	return canonical[0].GetInstallation(), nil
}
