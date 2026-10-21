package business

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// GrantScopeTxForTest runs the scope-grant body — the subject's tenancy under
// the membership lock, the row and its trigger-acquired locks, the event and the
// audit record — inside a transaction the test holds open, so a test can place a
// concurrent removal or uninstall at an exact point around the write. Compiled
// into tests only.
func (s *Service) GrantScopeTxForTest(ctx context.Context, actorID string, req *gen.GrantScopeRequest) (*gen.ScopeGrant, error) {
	return s.grantScopeTx(ctx, actorID, req, "")
}
