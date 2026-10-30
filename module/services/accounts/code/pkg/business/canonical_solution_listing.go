package business

import (
	"context"
	"slices"
	"strings"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

type canonicalSolutionReader interface {
	CanonicalSolutionSummaries(context.Context, string, string, string, gen.InstallationStatus, int) ([]*gen.InstallationSummary, error)
}

func (s *Service) canonicalSolutionSummaries(ctx context.Context, org, id, cursor string, status gen.InstallationStatus, limit int) ([]*gen.InstallationSummary, error) {
	store, ok := s.store.(canonicalSolutionReader)
	if !ok {
		return nil, nil
	} // Non-canonical test stores retain their own read contract.
	return store.CanonicalSolutionSummaries(ctx, org, id, cursor, status, limit)
}

func mergeInstallationSummaries(modules, solutions []*gen.InstallationSummary) []*gen.InstallationSummary {
	all := append(slices.Clone(modules), solutions...)
	slices.SortFunc(all, func(a, b *gen.InstallationSummary) int {
		return strings.Compare(a.GetInstallation().GetId(), b.GetInstallation().GetId())
	})
	return all
}
