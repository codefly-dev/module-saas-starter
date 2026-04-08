package fixtures

import (
	"backend/pkg/business"
	"backend/pkg/gen"
	"context"

	"github.com/codefly-dev/core/wool"
)

// DevAdmin seeds the dev-admin fixture (matching fixtures/dev-admin.yaml).
// TODO: read from YAML instead of hardcoding.
func DevAdmin(ctx context.Context, service *business.Service) error {
	w := wool.Get(ctx).In("fixtures.DevAdmin")
	w.Info("Applying dev-admin fixtures")

	type seedUser struct {
		email      string
		name       string
		providerID string
	}

	users := []seedUser{
		{"admin@acme.com", "Sarah Chen", "dev-admin"},
		{"alice@acme.com", "Alice Johnson", "dev-alice"},
		{"bob@acme.com", "Bob Williams", "dev-bob"},
		{"carol@acme.com", "Carol Davis", "dev-carol"},
	}

	userIDs := make(map[string]string)

	for _, u := range users {
		resp, err := service.RegisterUser(ctx, &gen.RegisterUserRequest{
			PrimaryEmail: u.email,
			Profile:      map[string]string{"name": u.name},
			Identity: &gen.UserIdentity{
				Provider:      "email",
				ProviderId:    u.providerID,
				ProviderEmail: u.email,
				EmailVerified: true,
			},
		})
		if err != nil {
			return w.Wrapf(err, "cannot seed user %s", u.email)
		}
		userIDs[u.email] = resp.User.Uuid
		w.Info("seeded user", wool.Field("email", u.email))
	}

	// Grant super_admin
	err := service.GrantPlatformRole(ctx, userIDs["admin@acme.com"], &gen.GrantPlatformRoleRequest{
		UserId:       userIDs["admin@acme.com"],
		PlatformRole: "super_admin",
	})
	if err != nil {
		w.Warn("cannot grant platform role", wool.Field("error", err.Error()))
	}

	// Create Acme Corp
	orgResp, err := service.CreateOrganization(ctx, userIDs["admin@acme.com"], &gen.CreateOrganizationRequest{
		Name: "Acme Corp",
	})
	if err != nil {
		return w.Wrapf(err, "cannot create org")
	}
	orgID := orgResp.GetOrganization().GetId()
	w.Info("seeded org", wool.Field("name", "Acme Corp"))

	// Add members
	for _, m := range []struct {
		email string
		role  gen.OrgRole
	}{
		{"alice@acme.com", gen.OrgRole_ORG_ROLE_ADMIN},
		{"bob@acme.com", gen.OrgRole_ORG_ROLE_MEMBER},
		{"carol@acme.com", gen.OrgRole_ORG_ROLE_MEMBER},
	} {
		err := service.AddOrgMember(ctx, userIDs["admin@acme.com"], &gen.AddOrgMemberRequest{
			OrgId:  orgID,
			UserId: userIDs[m.email],
			Role:   m.role,
		})
		if err != nil {
			w.Warn("cannot add org member", wool.Field("email", m.email), wool.Field("error", err.Error()))
		}
	}

	// Create teams
	engResp, err := service.CreateTeam(ctx, &gen.CreateTeamRequest{OrgId: orgID, Name: "Engineering"})
	if err == nil {
		for _, email := range []string{"alice@acme.com", "bob@acme.com"} {
			_ = service.AddTeamMember(ctx, userIDs["admin@acme.com"], &gen.AddTeamMemberRequest{
				TeamId: engResp.GetTeam().GetId(), UserId: userIDs[email],
			})
		}
		w.Info("seeded team", wool.Field("name", "Engineering"))
	}

	designResp, err := service.CreateTeam(ctx, &gen.CreateTeamRequest{OrgId: orgID, Name: "Design"})
	if err == nil {
		_ = service.AddTeamMember(ctx, userIDs["admin@acme.com"], &gen.AddTeamMemberRequest{
			TeamId: designResp.GetTeam().GetId(), UserId: userIDs["carol@acme.com"],
		})
		w.Info("seeded team", wool.Field("name", "Design"))
	}

	w.Info("dev-admin fixtures applied", wool.Field("users", 4), wool.Field("org", "Acme Corp"), wool.Field("teams", 2))
	return nil
}
