package fixtures

import (
	"accounts/pkg/business"
	"context"
)

// DevAdmin seeds the dev-admin fixture by reading module/fixtures/dev-admin.yaml.
// Idempotent — safe to re-run on an already-seeded database.
func DevAdmin(ctx context.Context, service *business.Service) error {
	return Seed(ctx, service, "dev-admin")
}
