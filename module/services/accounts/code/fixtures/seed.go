package fixtures

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/codefly-dev/core/wool"
	codefly "github.com/codefly-dev/sdk-go"
	"gopkg.in/yaml.v3"
)

// fixtureFile mirrors module/fixtures/*.yaml.
type fixtureFile struct {
	Users         []fixtureUser `yaml:"users"`
	Organizations []fixtureOrg  `yaml:"organizations"`
	Teams         []fixtureTeam `yaml:"teams"`
}

type fixtureUser struct {
	Email        string `yaml:"email"`
	Name         string `yaml:"name"`
	Role         string `yaml:"role"`
	Provider     string `yaml:"provider"`
	ProviderID   string `yaml:"provider_id"`
	FixtureToken string `yaml:"fixture_token"`
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

var fixtureNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func fixtureDirectory() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve fixture working directory: %w", err)
	}
	for {
		candidate := filepath.Join(dir, "fixtures")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			matches, globErr := filepath.Glob(filepath.Join(candidate, "*.yaml"))
			if globErr == nil && len(matches) > 0 {
				return candidate, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot find module fixtures directory from %q", dir)
		}
		dir = parent
	}
}

// SelectedName discovers product-added fixture files and asks the Codefly SDK
// which one the runtime selected. Consumers add YAML only; Starter never reads
// Codefly runtime environment variables or hard-codes product fixture names.
func SelectedName() (string, error) {
	directory, err := fixtureDirectory()
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", fmt.Errorf("cannot read fixture directory %q: %w", directory, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		if fixtureNamePattern.MatchString(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if codefly.WithFixture(name) {
			return name, nil
		}
	}
	return "", nil
}

// FixturePath resolves a validated fixture name to its module-owned YAML file.
func FixturePath(name string) (string, error) {
	if !fixtureNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid fixture name %q", name)
	}
	if override := os.Getenv("DEV_FIXTURE_PATH"); override != "" {
		return override, nil
	}
	directory, err := fixtureDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, name+".yaml"), nil
}

// Seed loads and converges any explicitly selected module fixture. Product
// consumers can add fixtures/<name>.yaml without modifying Starter-owned Go
// code; the same name is also consumed by the development auth validator.
func Seed(ctx context.Context, service *business.Service, name string) error {
	path, err := FixturePath(name)
	if err != nil {
		return err
	}

	w := wool.Get(ctx).In("fixtures.Seed")
	f, err := loadFixtureFile(path)
	if err != nil {
		return w.Wrapf(err, "cannot load %s fixture", name)
	}

	w.Info("Applying fixtures",
		wool.Field("fixture", name),
		wool.Field("users", len(f.Users)),
		wool.Field("orgs", len(f.Organizations)),
		wool.Field("teams", len(f.Teams)))

	userIDs, err := seedUsers(ctx, w, service, f.Users)
	if err != nil {
		return err
	}
	orgIDs := seedOrganizations(ctx, w, service, f.Organizations, userIDs)
	seedTeams(ctx, w, service, f.Teams, orgIDs, userIDs)

	w.Info("fixtures applied", wool.Field("fixture", name))
	return nil
}

// fixturePath resolves the YAML fixture relative to the accounts service's working
// directory. Codefly starts the binary in services/accounts/code/, so the module
// fixtures live at ../../../fixtures/.
func fixturePath(name string) string {
	path, _ := FixturePath(name)
	return path
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
// email -> user UUID. Uses the store directly for both RegisterUser and
// GrantPlatformRole:
//   - GrantPlatformRole bypasses the auth check that requires an existing
//     admin to authorize the grant — bootstrap chicken-and-egg.
//   - RegisterUser bypasses business.Service.RegisterUser's side effect
//     of auto-creating a "Personal" organization with the user as owner.
//     Without this, every fixture user ends up in TWO orgs (Personal,
//     plus the shared Acme Corp), and ensureOrg picks the OLDEST by
//     joined_at — so Bob (a "member" in the fixture intent) logs in as
//     OWNER of his Personal org and the role-gate UI grants him admin
//     surface he should not see.
func seedUsers(ctx context.Context, w *wool.Wool, service *business.Service, users []fixtureUser) (map[string]string, error) {
	userIDs := make(map[string]string, len(users))
	for _, u := range users {
		// Fixture bootstrap has no caller identity. The identity tables are
		// RLS-protected, so an unscoped lookup appears empty and a second run
		// then collides on the unique provider identity. Use the explicit
		// system bypass for this convergence check.
		var existing *gen.User
		err := service.Store().WithControlPlane(ctx, func(ctx context.Context) error {
			var lookupErr error
			existing, lookupErr = service.Store().GetUserByIdentity(ctx, &gen.UserIdentity{
				Provider:   u.Provider,
				ProviderId: u.ProviderID,
			})
			return lookupErr
		})
		if err != nil {
			return nil, w.Wrapf(err, "cannot look up fixture user %s", u.Email)
		}
		if existing != nil {
			userIDs[u.Email] = existing.Uuid
			w.Info("user already exists, skipping", wool.Field("email", u.Email))
			continue
		}

		userID := business.NewIDString()
		identityID := business.NewIDString()
		user := &gen.User{
			Uuid:         userID,
			PrimaryEmail: u.Email,
			Status:       gen.UserStatus_USER_STATUS_ACTIVE,
			Profile:      map[string]string{"name": u.Name},
		}
		identity := &gen.UserIdentity{
			Uuid:          identityID,
			UserUuid:      userID,
			Provider:      u.Provider,
			ProviderId:    u.ProviderID,
			ProviderEmail: u.Email,
			EmailVerified: true,
		}
		if err := service.Store().RegisterUser(ctx, user, identity); err != nil {
			return nil, w.Wrapf(err, "cannot seed user %s", u.Email)
		}
		userIDs[u.Email] = userID
		w.Info("seeded user", wool.Field("email", u.Email))

		// Grant platform role via store directly (bypasses auth check for
		// bootstrap — no admin exists yet to authorize the first grant).
		// granted_by is a nullable uuid column referencing users(uuid);
		// pass the user's own uuid so the row represents a self-grant at
		// fixture time. "fixture-seed" as a string fails the uuid cast.
		if u.Role == "super_admin" {
			if err := service.Store().GrantPlatformRole(ctx, userID, u.Role, userID); err != nil {
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

		var existingOrgs []*gen.Organization
		listErr := service.Store().WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			existingOrgs, err = service.Store().ListOrganizationsForUser(ctx, ownerID)
			return err
		})
		if listErr != nil {
			w.Warn("cannot list fixture organizations", wool.Field("name", org.Name), wool.Field("error", listErr.Error()))
			continue
		}
		for _, existing := range existingOrgs {
			if existing.Name == org.Name {
				orgIDs[org.Name] = existing.Id
				w.Info("org already exists, reusing", wool.Field("name", org.Name))
				break
			}
		}

		if _, found := orgIDs[org.Name]; !found {
			orgResp, err := service.CreateOrganization(ctx, ownerID, &gen.CreateOrganizationRequest{Name: org.Name})
			if err != nil {
				w.Warn("cannot create org, skipping", wool.Field("name", org.Name), wool.Field("error", err.Error()))
				continue
			}
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
		teamID := ""
		var existingTeams []*gen.Team
		if err := service.Store().WithControlPlane(ctx, func(ctx context.Context) error {
			var listErr error
			existingTeams, listErr = service.Store().ListTeams(ctx, orgID)
			return listErr
		}); err != nil {
			w.Warn("cannot list fixture teams", wool.Field("name", team.Name), wool.Field("error", err.Error()))
			continue
		}
		for _, existing := range existingTeams {
			if existing.Name == team.Name {
				teamID = existing.Id
				w.Info("team already exists, reusing", wool.Field("name", team.Name))
				break
			}
		}
		if teamID == "" {
			teamResp, err := service.CreateTeam(ctx, "seed", &gen.CreateTeamRequest{OrgId: orgID, Name: team.Name})
			if err != nil {
				w.Warn("cannot create team, skipping", wool.Field("name", team.Name), wool.Field("error", err.Error()))
				continue
			}
			teamID = teamResp.GetTeam().GetId()
			w.Info("seeded team", wool.Field("name", team.Name))
		}

		for _, email := range team.Members {
			memberID, ok := userIDs[email]
			if !ok {
				w.Warn("team member not found", wool.Field("email", email))
				continue
			}
			// Bootstrap seeding carries no tenant context, so the org-scoped
			// team_members RLS policy rejects a bare insert (SQLSTATE 42501).
			// Seed under the audited System bypass — same bootstrap rationale
			// as RegisterUser / GrantPlatformRole above.
			if err := service.Store().WithControlPlane(ctx, func(ctx context.Context) error {
				return service.Store().AddTeamMember(ctx, teamID, memberID, "member")
			}); err != nil {
				w.Warn("cannot add team member", wool.Field("email", email), wool.Field("error", err.Error()))
			}
		}
	}
}
