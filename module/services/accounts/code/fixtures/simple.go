package fixtures

import (
	"accounts/pkg/business"
	"context"
)

// Simple seeds the simple fixture by reading module/fixtures/simple.yaml.
// Idempotent — safe to re-run on an already-seeded database.
func Simple(ctx context.Context, service *business.Service) error {
	return Seed(ctx, service, "simple")
}
