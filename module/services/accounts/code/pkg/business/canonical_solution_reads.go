package business

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
)

// canonicalSolutionEntitlements uses the same member decision as the shared
// Setup UI. Installation ownership and its machine role grant nobody access.
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
		canonical, err := s.canonicalSolutionSummaries(ctx, org, h.Installation.ID, "", gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, 1)
		if err != nil {
			return nil, err
		}
		if len(canonical) != 1 || canonical[0].Health != gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY ||
			canonical[0].GetInstallation().GetRevision() != h.Installation.Revision {
			continue
		}
		module, err := core.Composed(ctx, h.Installation.Target)
		if err != nil {
			return nil, err
		}
		var targets []*AvailableSolutionTarget
		err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			targets, err = s.store.ListAvailableSolutionTargets(ctx, AvailableSolutionQuery{OrgID: org, TargetID: canonical[0].GetInstallation().GetTargetId(), Limit: 1})
			return err
		})
		if err != nil {
			return nil, err
		}
		if len(targets) != 1 || targets[0].Kind != "solution" || targets[0].RouteAlias != module.Name {
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
