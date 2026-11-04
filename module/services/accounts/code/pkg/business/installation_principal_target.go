package business

import (
	"context"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

// ResolveInstallationPrincipal resolves the exact signed principal through the
// existing authority registry and installable catalogue. A composed name, release,
// binding or principal-shaped UUID is never itself a host target identity.
// Both binding requirements and presence edges use this same resolution.
func (s *Service) ResolveInstallationPrincipal(ctx context.Context, org, principal string) (*AvailableSolutionTarget, error) {
	snapshot := s.moduleRegistrySnapshot()
	if err := s.fenceModuleRegistrySnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	grant, exists := snapshot.registry[ModulePrincipalID(principal)]
	if !exists || grant.Prefix != principal || grant.PresenceBinding == "" || !moduleAvailableToOrganization(grant, org) {
		return nil, domain.NotNow("INSTALLATION_DEPENDENCY_MISSING: no live host target resolves signed principal %q", principal)
	}
	var targets []*AvailableSolutionTarget
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		targets, err = s.store.ListAvailableSolutionTargets(ctx, AvailableSolutionQuery{OrgID: org, BindingID: grant.PresenceBinding, Limit: 2})
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, domain.NotNow("INSTALLATION_DEPENDENCY_MISSING: no live host target resolves signed principal %q", principal)
	}
	if len(targets) > 1 {
		return nil, domain.NotNow("INSTALLATION_DEPENDENCY_AMBIGUOUS: multiple live targets resolve signed principal %q", principal)
	}
	if targets[0].AppliedGeneration != grant.PresenceGeneration {
		return nil, domain.NotNow("INSTALLATION_DELIVERY_CHANGED: signed principal %q has no current catalogue projection", principal)
	}
	if err := s.fenceModuleRegistrySnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	return targets[0], nil
}
