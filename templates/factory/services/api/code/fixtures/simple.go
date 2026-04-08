package fixtures

import (
	"backend/pkg/business"
	"backend/pkg/gen"
	"context"

	"github.com/codefly-dev/core/wool"
)

// Simple seeds an admin user with a personal org for development.
func Simple(ctx context.Context, service *business.Service) error {
	w := wool.Get(ctx).In("fixtures.Simple")
	w.Info("Applying simple fixtures")

	resp, err := service.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "admin@localhost",
		Profile:      map[string]string{"name": "Local Admin"},
		Identity: &gen.UserIdentity{
			Provider:      "email",
			ProviderId:    "local-admin",
			ProviderEmail: "admin@localhost",
			EmailVerified: true,
		},
	})
	if err != nil {
		return w.Wrapf(err, "cannot seed admin user")
	}

	w.Info("seeded admin user",
		wool.Field("user_id", resp.User.Uuid),
		wool.Field("email", resp.User.PrimaryEmail))

	return nil
}
