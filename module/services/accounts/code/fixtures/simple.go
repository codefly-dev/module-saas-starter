package fixtures

import (
	"accounts/pkg/business"
	"context"

	"github.com/codefly-dev/core/wool"
)

// Simple seeds the simple fixture by reading module/fixtures/simple.yaml.
// Idempotent — safe to re-run on an already-seeded database.
func Simple(ctx context.Context, service *business.Service) error {
	w := wool.Get(ctx).In("fixtures.Simple")

	f, err := loadFixtureFile(fixturePath("simple"))
	if err != nil {
		return w.Wrapf(err, "cannot load simple fixture")
	}

	w.Info("Applying simple fixtures", wool.Field("users", len(f.Users)))

	_, err = seedUsers(ctx, w, service, f.Users)
	if err != nil {
		return err
	}

	return nil
}
