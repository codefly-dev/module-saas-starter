package business

import (
	"context"
	"time"

	"accounts/pkg/datasource/connector"
)

// The connector budget: every provider credential's operations are metered in
// one window shared by every source and every replica that uses it. A sync a
// person started may spend the whole quota; background work (the periodic
// reconcile, webhook deliveries) only its declared share, so it always leaves
// room for a person. A provider's own rate limit blocks the credential until
// the provider said it resets.

// DatasourceBudgetStore is the durable meter, on the control plane.
type DatasourceBudgetStore interface {
	// SpendDatasourceBudget spends one operation of key's window if fewer
	// than limit have been spent in it, opening a new window once the current
	// one is window old. It reports whether it spent, when the window resets,
	// and when a provider block lifts (zero when there is none, or it passed).
	SpendDatasourceBudget(ctx context.Context, key string, limit int, window time.Duration, now time.Time) (spent bool, resetAt, blockedUntil time.Time, err error)
	// BlockDatasourceBudget stops key being spent before until.
	BlockDatasourceBudget(ctx context.Context, key string, until time.Time) error
}

// SetDatasourceBudgetStore installs the meter. Without one connectors run
// unmetered; the host wires it at start.
func (s *Service) SetDatasourceBudgetStore(store DatasourceBudgetStore) {
	s.datasourceBudgets = store
}

// datasourceScheduler is the host's connector.Scheduler over its budget store.
type datasourceScheduler struct{ s *Service }

func (d datasourceScheduler) Acquire(ctx context.Context, src connector.Source, budget connector.Budget, priority connector.Priority) error {
	store := d.s.datasourceBudgets
	if store == nil || src.CredentialKey == "" {
		return nil
	}
	limit := budget.OperationsPerWindow
	if priority != connector.PriorityInteractive {
		limit = budget.OperationsPerWindow * budget.BackgroundSharePercent / 100
		if limit < 1 {
			limit = 1
		}
	}
	now := time.Now().UTC()
	var spent bool
	var resetAt, blockedUntil time.Time
	if err := d.s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		spent, resetAt, blockedUntil, err = store.SpendDatasourceBudget(ctx, src.CredentialKey, limit, budget.Window, now)
		return err
	}); err != nil {
		return err
	}
	switch {
	case !blockedUntil.IsZero():
		return &connector.RateLimitedError{ResetAt: blockedUntil, Scope: "credential", Yielded: true}
	case !spent:
		return &connector.RateLimitedError{ResetAt: resetAt, Scope: "credential", Yielded: true}
	}
	return nil
}

func (d datasourceScheduler) Blocked(ctx context.Context, src connector.Source, limited *connector.RateLimitedError) error {
	store := d.s.datasourceBudgets
	if store == nil || src.CredentialKey == "" || limited.ResetAt.IsZero() {
		return nil
	}
	return d.s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return store.BlockDatasourceBudget(ctx, src.CredentialKey, limited.ResetAt)
	})
}

// datasourceCredentialKey names the credential a source's operations spend:
// an App installation is shared by every source it covers, a pasted token is
// the source's own, and an unauthenticated public read is metered for the
// whole deployment, as the provider meters it by address.
func datasourceCredentialKey(source *DatasourceSource) string {
	switch {
	case source.Provider != DatasourceProviderGitHub:
		return source.Provider + ":source:" + source.ID
	case source.GitHubInstallationID != "":
		return "github:installation:" + source.GitHubInstallationID
	case source.GitHubCredentialKind == githubCredentialKindPublic:
		return "github:public"
	default:
		return "github:source:" + source.ID
	}
}
