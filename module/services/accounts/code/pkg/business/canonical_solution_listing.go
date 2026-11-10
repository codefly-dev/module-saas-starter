package business

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

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
// whose core is not configured keeps the store's answer. The installations
// and the composition are read once per listing; each answer is kept per
// installation revision and composition (upgradeCache), so a listing read
// again computes no reach that cannot have moved.
func (s *Service) upgradesAvailable(ctx context.Context, summaries []*gen.InstallationSummary) error {
	core, err := s.AuthorityCore()
	cores, ok := s.store.(AuthorityCoreStore)
	if err != nil || !ok {
		return nil
	}
	pending := map[string]*gen.Installation{}
	for _, summary := range summaries {
		if i := summary.GetInstallation(); i.GetStatus() == gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE && !i.GetUpgradeAvailable() {
			pending[i.GetId()] = i
		}
	}
	if len(pending) == 0 {
		return nil
	}
	held := map[string]domain.Installation{}
	var composition string
	if err := cores.AuthorityCoreWithConsent(nil).Read(ctx, func(tx authority.Tx) error {
		modules, err := tx.Modules()
		if err != nil {
			return err
		}
		composition = compositionStamp(modules)
		for id := range pending {
			inst, err := tx.Installation(id)
			if err != nil {
				return err
			}
			held[id] = inst
		}
		return nil
	}); err != nil {
		return err
	}
	for id, i := range pending {
		inst := held[id]
		key := fmt.Sprintf("%s@%d@%d@%s", inst.ID, inst.Revision, inst.Consent.Generation, inst.Consent.ReachDigest)
		if upgrade, ok := s.upgradeCache.get(composition, key); ok {
			i.UpgradeAvailable = upgrade
			continue
		}
		if i.UpgradeAvailable, err = core.UpgradeAvailable(ctx, inst); err != nil {
			return err
		}
		s.upgradeCache.put(composition, key, i.UpgradeAvailable)
	}
	return nil
}

// compositionStamp names one composition: every composed module at its
// generation. Any delivery or withdrawal changes it.
func compositionStamp(modules []domain.Module) string {
	parts := make([]string, 0, len(modules))
	for _, m := range modules {
		parts = append(parts, fmt.Sprintf("%s@%d", m.Name, m.Generation))
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

// upgradeCache keeps upgrade_available per installation revision, consent
// generation and approved reach for one composition; a new composition starts
// it afresh.
type upgradeCache struct {
	mu          sync.Mutex
	composition string
	answers     map[string]bool
}

func (c *upgradeCache) get(composition, key string) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.composition != composition {
		return false, false
	}
	answer, ok := c.answers[key]
	return answer, ok
}

func (c *upgradeCache) put(composition, key string, answer bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.composition != composition {
		c.composition, c.answers = composition, map[string]bool{}
	}
	c.answers[key] = answer
}

func mergeInstallationSummaries(modules, solutions []*gen.InstallationSummary) []*gen.InstallationSummary {
	all := append(slices.Clone(modules), solutions...)
	slices.SortFunc(all, func(a, b *gen.InstallationSummary) int {
		return strings.Compare(a.GetInstallation().GetId(), b.GetInstallation().GetId())
	})
	return all
}
