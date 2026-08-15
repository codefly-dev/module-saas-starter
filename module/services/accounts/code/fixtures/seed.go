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
	Users         []fixtureUser           `yaml:"users"`
	Organizations []fixtureOrg            `yaml:"organizations"`
	Teams         []fixtureTeam           `yaml:"teams"`
	Agents        []fixtureAgent          `yaml:"agents"`
	Roles         []fixtureRole           `yaml:"roles"`
	Assignments   []fixtureRoleAssignment `yaml:"assignments"`
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

type fixtureAgent struct {
	Org             string `yaml:"org"`
	AgentIdentifier string `yaml:"agent_identifier"`
	DisplayName     string `yaml:"display_name"`
	CreatedBy       string `yaml:"created_by"`
}

type fixturePermission struct {
	Resource string `yaml:"resource"`
	Action   string `yaml:"action"`
}

type fixtureRole struct {
	Org         string              `yaml:"org"`
	Name        string              `yaml:"name"`
	Description string              `yaml:"description"`
	Permissions []fixturePermission `yaml:"permissions"`
}

type fixtureRoleAssignment struct {
	Org             string `yaml:"org"`
	Role            string `yaml:"role"`
	AgentIdentifier string `yaml:"agent_identifier"`
	Scope           string `yaml:"scope"`
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
	selected := strings.TrimSpace(codefly.Fixture())
	if selected == "" {
		return "", nil
	}
	if !fixtureNamePattern.MatchString(selected) {
		return "", fmt.Errorf("Codefly selected invalid fixture name %q", selected)
	}
	for _, name := range names {
		if name == selected {
			return selected, nil
		}
	}
	return "", fmt.Errorf("Codefly selected fixture %q, but %s does not contain %s.yaml", selected, directory, selected)
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
		wool.Field("teams", len(f.Teams)),
		wool.Field("agents", len(f.Agents)),
		wool.Field("roles", len(f.Roles)),
		wool.Field("assignments", len(f.Assignments)))

	userIDs, err := seedUsers(ctx, w, service, f.Users)
	if err != nil {
		return err
	}
	orgIDs := seedOrganizations(ctx, w, service, f.Organizations, userIDs)
	orgOwnerIDs := make(map[string]string, len(f.Organizations))
	for _, organization := range f.Organizations {
		if ownerID, ok := userIDs[organization.Owner]; ok {
			orgOwnerIDs[organization.Name] = ownerID
		}
	}
	seedTeams(ctx, w, service, f.Teams, orgIDs, userIDs)
	agentIDs, err := seedAgents(ctx, w, service, f.Agents, orgIDs, userIDs)
	if err != nil {
		return err
	}
	roleIDs, err := seedRoles(ctx, w, service, f.Roles, orgIDs, orgOwnerIDs)
	if err != nil {
		return err
	}
	if err := seedRoleAssignments(ctx, w, service, f.Assignments, orgIDs, agentIDs, roleIDs); err != nil {
		return err
	}

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
	organizationSlugIndexes := make(map[string]int, len(f.Organizations))
	for i, org := range f.Organizations {
		if org.Name == "" {
			return fmt.Errorf("organization[%d]: name is required", i)
		}
		if org.Owner == "" {
			return fmt.Errorf("organization[%d] (%s): owner is required", i, org.Name)
		}
		slug := business.Slugify(org.Name)
		if slug == "" {
			return fmt.Errorf("organization[%d] (%s): name yields an empty slug", i, org.Name)
		}
		if previous, exists := organizationSlugIndexes[slug]; exists {
			return fmt.Errorf(
				"organization[%d] (%s): slug %q collides with organization[%d]",
				i,
				org.Name,
				slug,
				previous,
			)
		}
		organizationSlugIndexes[slug] = i
	}
	for i, agent := range f.Agents {
		if strings.TrimSpace(agent.Org) == "" {
			return fmt.Errorf("agent[%d]: org is required", i)
		}
		if strings.TrimSpace(agent.AgentIdentifier) == "" {
			return fmt.Errorf("agent[%d]: agent_identifier is required", i)
		}
		if strings.TrimSpace(agent.CreatedBy) == "" {
			return fmt.Errorf("agent[%d] (%s): created_by is required", i, agent.AgentIdentifier)
		}
	}
	for i, role := range f.Roles {
		if strings.TrimSpace(role.Org) == "" {
			return fmt.Errorf("role[%d]: org is required", i)
		}
		if strings.TrimSpace(role.Name) == "" {
			return fmt.Errorf("role[%d]: name is required", i)
		}
		if len(role.Permissions) == 0 {
			return fmt.Errorf("role[%d] (%s): at least one permission is required", i, role.Name)
		}
		for j, permission := range role.Permissions {
			if strings.TrimSpace(permission.Resource) == "" || strings.TrimSpace(permission.Action) == "" {
				return fmt.Errorf("role[%d] (%s) permission[%d]: resource and action are required", i, role.Name, j)
			}
		}
	}
	for i, assignment := range f.Assignments {
		if strings.TrimSpace(assignment.Org) == "" ||
			strings.TrimSpace(assignment.Role) == "" ||
			strings.TrimSpace(assignment.AgentIdentifier) == "" {
			return fmt.Errorf("assignment[%d]: org, role, and agent_identifier are required", i)
		}
	}
	return nil
}

func orgRoleFromString(s string, w *wool.Wool) string {
	switch s {
	case "admin", "member", "owner":
		return s
	default:
		w.Warn("unknown org role, defaulting to member", wool.Field("role", s))
		return "member"
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
			// Fixtures are desired state, not create-only samples. Converge the
			// platform role on every activation so authentication and admin
			// authorization cannot disagree for the same fixture identity.
			if u.Role == "super_admin" {
				if err := service.Store().GrantPlatformRole(
					ctx,
					existing.Uuid,
					u.Role,
					existing.Uuid,
				); err != nil {
					return nil, w.Wrapf(
						err,
						"cannot converge platform role for fixture user %s",
						u.Email,
					)
				}
			}
			w.Info("fixture user converged", wool.Field("email", u.Email))
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
				return nil, w.Wrapf(err, "cannot grant platform role for fixture user %s", u.Email)
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
			role := orgRoleFromString(m.Role, w)
			// Fixture membership is bootstrap state, so it bypasses the runtime
			// seat-admission quota under the audited system scope.
			err := service.Store().As(business.System()).Within(ctx, func(ctx context.Context) error {
				return service.Store().AddOrgMember(ctx, orgID, memberID, role)
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

func fixtureScopedKey(org, value string) string {
	return org + "\x00" + value
}

func seedAgents(
	ctx context.Context,
	w *wool.Wool,
	service *business.Service,
	agents []fixtureAgent,
	orgIDs, userIDs map[string]string,
) (map[string]string, error) {
	agentIDs := make(map[string]string, len(agents))
	for _, agent := range agents {
		orgID, ok := orgIDs[agent.Org]
		if !ok {
			return nil, fmt.Errorf("agent %q references unknown organization %q", agent.AgentIdentifier, agent.Org)
		}
		creatorID, ok := userIDs[agent.CreatedBy]
		if !ok {
			return nil, fmt.Errorf("agent %q references unknown creator %q", agent.AgentIdentifier, agent.CreatedBy)
		}
		principal, err := service.CreateAgentPrincipal(ctx, business.CreateAgentRequest{
			OrgID:           orgID,
			AgentIdentifier: agent.AgentIdentifier,
			DisplayName:     agent.DisplayName,
			CreatedBy:       creatorID,
		})
		if err != nil {
			return nil, w.Wrapf(err, "cannot seed agent %s", agent.AgentIdentifier)
		}
		agentIDs[fixtureScopedKey(agent.Org, agent.AgentIdentifier)] = principal.ID
		w.Info("seeded or reused agent principal",
			wool.Field("org", agent.Org),
			wool.Field("agent_identifier", agent.AgentIdentifier))
	}
	return agentIDs, nil
}

func seedRoles(
	ctx context.Context,
	w *wool.Wool,
	service *business.Service,
	roles []fixtureRole,
	orgIDs, orgOwnerIDs map[string]string,
) (map[string]string, error) {
	roleIDs := make(map[string]string, len(roles))
	for _, fixtureRole := range roles {
		orgID, ok := orgIDs[fixtureRole.Org]
		if !ok {
			return nil, fmt.Errorf("role %q references unknown organization %q", fixtureRole.Name, fixtureRole.Org)
		}
		ownerID, ok := orgOwnerIDs[fixtureRole.Org]
		if !ok {
			return nil, fmt.Errorf("role %q cannot resolve an organization owner", fixtureRole.Name)
		}

		var existingRoles []*gen.Role
		if err := service.Store().WithOrgTx(ctx, orgID, func(ctx context.Context) error {
			var listErr error
			existingRoles, listErr = service.Store().ListRoles(ctx, orgID)
			return listErr
		}); err != nil {
			return nil, w.Wrapf(err, "cannot list fixture roles for %s", fixtureRole.Org)
		}
		var existing *gen.Role
		for _, role := range existingRoles {
			if role.OrgId == orgID && role.Name == fixtureRole.Name {
				existing = role
				break
			}
		}
		permissions := make([]*gen.Permission, 0, len(fixtureRole.Permissions))
		for _, permission := range fixtureRole.Permissions {
			permissions = append(permissions, &gen.Permission{
				Resource: permission.Resource,
				Action:   permission.Action,
			})
		}
		if existing != nil {
			if !sameFixturePermissions(existing.Permissions, permissions) {
				return nil, fmt.Errorf(
					"fixture role %q in %q exists with different permissions",
					fixtureRole.Name,
					fixtureRole.Org,
				)
			}
			roleIDs[fixtureScopedKey(fixtureRole.Org, fixtureRole.Name)] = existing.Id
			w.Info("role already exists, reusing",
				wool.Field("org", fixtureRole.Org),
				wool.Field("name", fixtureRole.Name))
			continue
		}
		response, err := service.CreateRole(ctx, ownerID, &gen.CreateRoleRequest{
			OrgId:       orgID,
			Name:        fixtureRole.Name,
			Description: fixtureRole.Description,
			Permissions: permissions,
		})
		if err != nil {
			return nil, w.Wrapf(err, "cannot seed role %s", fixtureRole.Name)
		}
		roleIDs[fixtureScopedKey(fixtureRole.Org, fixtureRole.Name)] = response.Role.Id
		w.Info("seeded role", wool.Field("org", fixtureRole.Org), wool.Field("name", fixtureRole.Name))
	}
	return roleIDs, nil
}

func sameFixturePermissions(left, right []*gen.Permission) bool {
	canonical := func(permissions []*gen.Permission) []string {
		values := make([]string, 0, len(permissions))
		for _, permission := range permissions {
			values = append(values, permission.GetResource()+"\x00"+permission.GetAction())
		}
		sort.Strings(values)
		return values
	}
	leftValues := canonical(left)
	rightValues := canonical(right)
	if len(leftValues) != len(rightValues) {
		return false
	}
	for index := range leftValues {
		if leftValues[index] != rightValues[index] {
			return false
		}
	}
	return true
}

func seedRoleAssignments(
	ctx context.Context,
	w *wool.Wool,
	service *business.Service,
	assignments []fixtureRoleAssignment,
	orgIDs, agentIDs, roleIDs map[string]string,
) error {
	for _, assignment := range assignments {
		orgID, ok := orgIDs[assignment.Org]
		if !ok {
			return fmt.Errorf("assignment references unknown organization %q", assignment.Org)
		}
		agentID, ok := agentIDs[fixtureScopedKey(assignment.Org, assignment.AgentIdentifier)]
		if !ok {
			return fmt.Errorf("assignment references unknown agent %q in %q", assignment.AgentIdentifier, assignment.Org)
		}
		roleID, ok := roleIDs[fixtureScopedKey(assignment.Org, assignment.Role)]
		if !ok {
			return fmt.Errorf("assignment references unknown role %q in %q", assignment.Role, assignment.Org)
		}

		var existing []*gen.RoleAssignment
		if err := service.Store().WithOrgTx(ctx, orgID, func(ctx context.Context) error {
			var listErr error
			existing, listErr = service.Store().ListRoleAssignments(
				ctx,
				orgID,
				agentID,
				gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			)
			return listErr
		}); err != nil {
			return w.Wrapf(err, "cannot list fixture role assignments")
		}
		found := false
		for _, current := range existing {
			if current.RoleId == roleID && current.Scope == assignment.Scope {
				found = true
				break
			}
		}
		if found {
			w.Info("role assignment already exists, reusing",
				wool.Field("org", assignment.Org),
				wool.Field("agent_identifier", assignment.AgentIdentifier),
				wool.Field("role", assignment.Role))
			continue
		}
		if _, err := service.AssignRole(ctx, &gen.AssignRoleRequest{
			SubjectId:   agentID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			RoleId:      roleID,
			OrgId:       orgID,
			Scope:       assignment.Scope,
		}); err != nil {
			return w.Wrapf(err, "cannot assign fixture role %s", assignment.Role)
		}
		w.Info("seeded role assignment",
			wool.Field("org", assignment.Org),
			wool.Field("agent_identifier", assignment.AgentIdentifier),
			wool.Field("role", assignment.Role))
	}
	return nil
}
