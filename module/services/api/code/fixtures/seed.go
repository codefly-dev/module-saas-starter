package fixtures

import (
	"api/pkg/business"
	"api/pkg/gen"
	"context"
	"fmt"
	"os"

	"github.com/codefly-dev/core/wool"
	"gopkg.in/yaml.v3"
)

// fixtureFile mirrors module/fixtures/*.yaml.
type fixtureFile struct {
	Users         []fixtureUser `yaml:"users"`
	Organizations []fixtureOrg  `yaml:"organizations"`
	Teams         []fixtureTeam `yaml:"teams"`
}

type fixtureUser struct {
	Email      string `yaml:"email"`
	Name       string `yaml:"name"`
	Role       string `yaml:"role"`
	Provider   string `yaml:"provider"`
	ProviderID string `yaml:"provider_id"`
}

type fixtureOrg struct {
	Name    string             `yaml:"name"`
	Owner   string             `yaml:"owner"`
	Members []fixtureOrgMember `yaml:"members"`
}

type fixtureOrgMember struct {
	Email string `yaml:"email"`
	Role  string `yaml:"role"`
}

type fixtureTeam struct {
	Name    string   `yaml:"name"`
	Org     string   `yaml:"org"`
	Members []string `yaml:"members"`
}

// fixturePath resolves the YAML fixture relative to the api service's working
// directory. Codefly starts the binary in services/api/code/, so the module
// fixtures live at ../../../fixtures/.
func fixturePath(name string) string {
	if env := os.Getenv("DEV_FIXTURE_PATH"); env != "" {
		return env
	}
	return fmt.Sprintf("../../../fixtures/%s.yaml", name)
}

func loadFixtureFile(path string) (*fixtureFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read fixture %q: %w", path, err)
	}
	var f fixtureFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("cannot parse fixture %q: %w", path, err)
	}
	if err := validateFixture(&f); err != nil {
		return nil, fmt.Errorf("invalid fixture %q: %w", path, err)
	}
	return &f, nil
}

func validateFixture(f *fixtureFile) error {
	for i, u := range f.Users {
		if u.Email == "" {
			return fmt.Errorf("user[%d]: email is required", i)
		}
		if u.Provider == "" {
			return fmt.Errorf("user[%d] (%s): provider is required", i, u.Email)
		}
		if u.ProviderID == "" {
			return fmt.Errorf("user[%d] (%s): provider_id is required", i, u.Email)
		}
	}
	for i, org := range f.Organizations {
		if org.Name == "" {
			return fmt.Errorf("organization[%d]: name is required", i)
		}
		if org.Owner == "" {
			return fmt.Errorf("organization[%d] (%s): owner is required", i, org.Name)
		}
	}
	return nil
}

func orgRoleFromString(s string, w *wool.Wool) gen.OrgRole {
	switch s {
	case "admin":
		return gen.OrgRole_ORG_ROLE_ADMIN
	case "member":
		return gen.OrgRole_ORG_ROLE_MEMBER
	case "owner":
		return gen.OrgRole_ORG_ROLE_OWNER
	default:
		w.Warn("unknown org role, defaulting to member", wool.Field("role", s))
		return gen.OrgRole_ORG_ROLE_MEMBER
	}
}

// seedUsers creates users from the fixture, idempotently. Returns a map of
// email -> user UUID. Uses the store directly for GrantPlatformRole to avoid
// the bootstrap chicken-and-egg problem (no admin exists to authorize the
// first grant).
func seedUsers(ctx context.Context, w *wool.Wool, service *business.Service, users []fixtureUser) (map[string]string, error) {
	userIDs := make(map[string]string, len(users))
	for _, u := range users {
		// Check if user already exists via their identity.
		existing, _ := service.Store().GetUserByIdentity(ctx, &gen.UserIdentity{
			Provider:   u.Provider,
			ProviderId: u.ProviderID,
		})
		if existing != nil {
			userIDs[u.Email] = existing.Uuid
			w.Info("user already exists, skipping", wool.Field("email", u.Email))
			continue
		}

		resp, err := service.RegisterUser(ctx, &gen.RegisterUserRequest{
			PrimaryEmail: u.Email,
			Profile:      map[string]string{"name": u.Name},
			Identity: &gen.UserIdentity{
				Provider:      u.Provider,
				ProviderId:    u.ProviderID,
				ProviderEmail: u.Email,
				EmailVerified: true,
			},
		})
		if err != nil {
			return nil, w.Wrapf(err, "cannot seed user %s", u.Email)
		}
		userIDs[u.Email] = resp.User.Uuid
		w.Info("seeded user", wool.Field("email", u.Email))

		// Grant platform role via store directly (bypasses auth check for
		// bootstrap — no admin exists yet to authorize the first grant).
		if u.Role == "super_admin" {
			if err := service.Store().GrantPlatformRole(ctx, resp.User.Uuid, u.Role, "fixture-seed"); err != nil {
				w.Warn("cannot grant platform role", wool.Field("email", u.Email), wool.Field("error", err.Error()))
			}
		}
	}
	return userIDs, nil
}

// seedOrganizations creates orgs from the fixture, idempotently. On re-run,
// looks up existing orgs by querying the owner's org list.
func seedOrganizations(ctx context.Context, w *wool.Wool, service *business.Service, orgs []fixtureOrg, userIDs map[string]string) map[string]string {
	orgIDs := make(map[string]string, len(orgs))
	for _, org := range orgs {
		ownerID, ok := userIDs[org.Owner]
		if !ok {
			w.Warn("org owner not found, skipping", wool.Field("owner", org.Owner))
			continue
		}

		orgResp, err := service.CreateOrganization(ctx, ownerID, &gen.CreateOrganizationRequest{
			Name: org.Name,
		})
		if err != nil {
			// Org may already exist — try to find it via the owner's org list.
			w.Info("org creation failed, looking up existing", wool.Field("name", org.Name))
			existingOrgs, listErr := service.Store().ListOrganizationsForUser(ctx, ownerID)
			if listErr == nil {
				for _, existing := range existingOrgs {
					if existing.Name == org.Name {
						orgIDs[org.Name] = existing.Id
						w.Info("found existing org", wool.Field("name", org.Name))
						break
					}
				}
			}
			if _, found := orgIDs[org.Name]; !found {
				w.Warn("cannot create or find org, skipping", wool.Field("name", org.Name), wool.Field("error", err.Error()))
				continue
			}
		} else {
			orgIDs[org.Name] = orgResp.GetOrganization().GetId()
			w.Info("seeded org", wool.Field("name", org.Name))
		}

		orgID := orgIDs[org.Name]
		for _, m := range org.Members {
			memberID, ok := userIDs[m.Email]
			if !ok {
				w.Warn("org member not found", wool.Field("email", m.Email))
				continue
			}
			err := service.AddOrgMember(ctx, ownerID, &gen.AddOrgMemberRequest{
				OrgId:  orgID,
				UserId: memberID,
				Role:   orgRoleFromString(m.Role, w),
			})
			if err != nil {
				w.Warn("cannot add org member", wool.Field("email", m.Email), wool.Field("error", err.Error()))
			}
		}
	}
	return orgIDs
}

// seedTeams creates teams from the fixture, idempotently.
func seedTeams(ctx context.Context, w *wool.Wool, service *business.Service, teams []fixtureTeam, orgIDs, userIDs map[string]string) {
	for _, team := range teams {
		orgID, ok := orgIDs[team.Org]
		if !ok {
			w.Warn("team org not found, skipping", wool.Field("org", team.Org))
			continue
		}
		teamResp, err := service.CreateTeam(ctx, &gen.CreateTeamRequest{OrgId: orgID, Name: team.Name})
		if err != nil {
			w.Warn("team may already exist, skipping", wool.Field("name", team.Name), wool.Field("error", err.Error()))
			continue
		}
		w.Info("seeded team", wool.Field("name", team.Name))

		for _, email := range team.Members {
			memberID, ok := userIDs[email]
			if !ok {
				w.Warn("team member not found", wool.Field("email", email))
				continue
			}
			if err := service.Store().AddTeamMember(ctx, teamResp.GetTeam().GetId(), memberID, "member"); err != nil {
				w.Warn("cannot add team member", wool.Field("email", email), wool.Field("error", err.Error()))
			}
		}
	}
}
