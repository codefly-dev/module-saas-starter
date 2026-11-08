package business

import (
	"context"
	"slices"
	"strings"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

type canonicalSolutionReader interface {
	CanonicalSolutionSummaries(context.Context, string, string, string, gen.InstallationStatus, int) ([]*gen.InstallationSummary, error)
}

func (s *Service) canonicalSolutionSummaries(ctx context.Context, org, id, cursor string, status gen.InstallationStatus, limit int) ([]*gen.InstallationSummary, error) {
	store, ok := s.store.(canonicalSolutionReader)
	if !ok {
		return nil, nil
	} // Non-canonical test stores retain their own read contract.
	summaries, err := store.CanonicalSolutionSummaries(ctx, org, id, cursor, status, limit)
	if err != nil {
		return nil, err
	}
	return summaries, s.upgradesAvailable(ctx, summaries)
}

// upgradesAvailable sets each active summary's upgrade_available as the core
// answers it (Service.UpgradeAvailable): its target delivered past the
// approved generation, as the store's read already says, or what its roles
// reach in the composition now widened past the approved reach (a module it
// reaches redelivered), or a consent recorded before reach was kept. A host
// whose core is not configured keeps the store's answer.
func (s *Service) upgradesAvailable(ctx context.Context, summaries []*gen.InstallationSummary) error {
	core, err := s.AuthorityCore()
	cores, ok := s.store.(AuthorityCoreStore)
	if err != nil || !ok {
		return nil
	}
	for _, summary := range summaries {
		i := summary.GetInstallation()
		if i.GetStatus() != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE || i.GetUpgradeAvailable() {
			continue
		}
		var inst domain.Installation
		if err := cores.AuthorityCoreWithConsent(nil).Read(ctx, func(tx authority.Tx) error {
			inst, err = tx.Installation(i.GetId())
			return err
		}); err != nil {
			return err
		}
		if i.UpgradeAvailable, err = core.UpgradeAvailable(ctx, inst); err != nil {
			return err
		}
	}
	return nil
}

func mergeInstallationSummaries(modules, solutions []*gen.InstallationSummary) []*gen.InstallationSummary {
	all := append(slices.Clone(modules), solutions...)
	slices.SortFunc(all, func(a, b *gen.InstallationSummary) int {
		return strings.Compare(a.GetInstallation().GetId(), b.GetInstallation().GetId())
	})
	return all
}
