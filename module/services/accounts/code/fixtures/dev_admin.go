package fixtures

import (
	"accounts/pkg/business"
	"context"

	"github.com/codefly-dev/core/wool"
)

// DevAdmin seeds the dev-admin fixture by reading module/fixtures/dev-admin.yaml.
// Idempotent — safe to re-run on an already-seeded database.
func DevAdmin(ctx context.Context, service *business.Service) error {
	w := wool.Get(ctx).In("fixtures.DevAdmin")

	f, err := loadFixtureFile(fixturePath("dev-admin"))
	if err != nil {
		return w.Wrapf(err, "cannot load dev-admin fixture")
	}

	w.Info("Applying dev-admin fixtures",
		wool.Field("users", len(f.Users)),
		wool.Field("orgs", len(f.Organizations)),
		wool.Field("teams", len(f.Teams)))

	userIDs, err := seedUsers(ctx, w, service, f.Users)
	if err != nil {
		return err
	}

	orgIDs := seedOrganizations(ctx, w, service, f.Organizations, userIDs)
	seedTeams(ctx, w, service, f.Teams, orgIDs, userIDs)

	w.Info("dev-admin fixtures applied",
		wool.Field("users", len(f.Users)),
		wool.Field("orgs", len(f.Organizations)),
		wool.Field("teams", len(f.Teams)))
	return nil
}
