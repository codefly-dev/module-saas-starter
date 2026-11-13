package business

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/wool"
)

// InstallAccess is the access an administrator's install asks for beside its
// review: the team each declared role goes to where the administrator names
// one (RoleTeams, by role name), and the roles the installing person takes
// (InstallerRoles). Access is only ever by grant: the installer holds exactly
// the roles whose teams they join.
type InstallAccess struct {
	RoleTeams      map[string]string
	InstallerRoles []string
}

// InstallationAccess is the access an install checked and recorded on its
// plan, so a pending attempt that resumes applies the same one: the team of
// the organization the administrator named for a role (Teams), the
// conventional team name of every other declared role (Conventional), both by
// role name, and the person who joins the teams of InstallerRoles. Nothing
// is created or granted until the installation is active
// (applyInstallationAccess): a refused or pending install leaves no team.
type InstallationAccess struct {
	Teams          map[string]string `json:",omitempty"`
	Conventional   map[string]string `json:",omitempty"`
	Installer      string            `json:",omitempty"`
	InstallerRoles []string          `json:",omitempty"`
}

// AccessProblem is one part of an installation's access its assignment could
// not apply, by role, in words. The installation stands: an administrator
// grants the rest in Set up.
type AccessProblem struct {
	Role, Problem string
}

func (p AccessProblem) String() string { return p.Role + ": " + p.Problem }

// AccessProblemsToWire is problems as Installation.access_problems states them.
func AccessProblemsToWire(problems []AccessProblem) []string {
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, p.String())
	}
	return out
}

// isConventionalTeam reports whether team is the organization's team named
// name: its name, whatever its case, or a root team whose path is the slug of
// name. Teams are unique by path, so a team named otherwise ("Target Viewer")
// that holds the slug is the one a create of name would collide with.
func isConventionalTeam(team *gen.Team, name string) bool {
	if strings.EqualFold(strings.TrimSpace(team.GetName()), name) {
		return true
	}
	return team.GetParentTeamId() == "" && team.GetPath() != "" && team.GetPath() == Slugify(name)
}

// resolveInstallationAccess is the access caller's install of target records:
// for every role of roleTeams (the core's role → conventional team of what is
// being approved: authority.RoleTeams of the offer, or RoleTeamsOf an active
// installation), the team requested names for it, which must be a team of
// org, or else its conventional team, found or created once the installation
// is active. Everything requested is checked here; no team is created and no
// team's members change.
func (s *Service) resolveInstallationAccess(ctx context.Context, caller authority.Person, org, target string, roleTeams map[string]string, requested InstallAccess) (*InstallationAccess, error) {
	roles := slices.Sorted(maps.Keys(roleTeams))
	access := &InstallationAccess{Teams: map[string]string{}, Conventional: map[string]string{}, Installer: caller.ID,
		InstallerRoles: slices.Sorted(slices.Values(requested.InstallerRoles))}
	for _, role := range access.InstallerRoles {
		if !slices.Contains(roles, role) {
			return nil, domain.Malformed("%s declares no role %q", target, role)
		}
	}
	for role := range requested.RoleTeams {
		if !slices.Contains(roles, role) {
			return nil, domain.Malformed("%s declares no role %q", target, role)
		}
	}
	var teams []*gen.Team
	if len(requested.RoleTeams) > 0 {
		listed, err := s.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: org})
		if err != nil {
			return nil, err
		}
		teams = listed.GetTeams()
		for role, id := range requested.RoleTeams {
			if !slices.ContainsFunc(teams, func(t *gen.Team) bool { return t.GetId() == id }) {
				return nil, domain.Refuse("%q is not a team of this organization", id)
			}
			access.Teams[role] = id
		}
	}
	for _, role := range roles {
		if _, named := requested.RoleTeams[role]; !named {
			access.Conventional[role] = roleTeams[role]
		}
	}
	return access, refuseSharedTeam(access, teams)
}

// refuseSharedTeam refuses an access that gives one team a role the installer
// took and one they did not: joining that team for the first would give them
// the second. A conventional name matches an existing team as the install
// would find it (isConventionalTeam); a team created later is the role's own.
func refuseSharedTeam(access *InstallationAccess, teams []*gen.Team) error {
	if len(access.Teams) == 0 {
		return nil
	}
	teamOf := map[string]*gen.Team{}
	for _, role := range accessRoles(access) {
		if id, named := access.Teams[role]; named {
			if i := slices.IndexFunc(teams, func(t *gen.Team) bool { return t.GetId() == id }); i >= 0 {
				teamOf[role] = teams[i]
			}
		} else if i := slices.IndexFunc(teams, func(t *gen.Team) bool { return isConventionalTeam(t, access.Conventional[role]) }); i >= 0 {
			teamOf[role] = teams[i]
		}
	}
	for _, ticked := range access.InstallerRoles {
		for _, unticked := range accessRoles(access) {
			if slices.Contains(access.InstallerRoles, unticked) || teamOf[ticked] == nil || teamOf[unticked] == nil ||
				teamOf[ticked].GetId() != teamOf[unticked].GetId() {
				continue
			}
			return domain.Malformed("%s carries both %s and %s; give them separate teams or tick both", teamOf[ticked].GetName(), ticked, unticked)
		}
	}
	return nil
}

// applyInstallationAccess applies access once inst is active, as caller:
// each role's team is resolved (the team named for it while it still exists,
// else its conventional team: created, or found only when this installation already granted it the role), each role is granted to its
// team, then the installer joins the team of each role they took. Before
// activation nothing is applied; the resumed attempt that activates inst
// applies it. Every step is check-before-write, so applying it again changes
// nothing. A role whose team, grant or membership fails is reported, and the
// other roles are applied all the same: the installation is active, so its
// access problems are an answer, never an error.
func (s *Service) applyInstallationAccess(ctx context.Context, caller authority.Person, inst domain.Installation, access *InstallationAccess) []AccessProblem {
	if access == nil || inst.Status != domain.Active || len(accessRoles(access)) == 0 {
		return nil
	}
	var problems []AccessProblem
	report := func(role, format string, args ...any) {
		problems = append(problems, AccessProblem{Role: role, Problem: fmt.Sprintf(format, args...)})
	}
	listed, err := s.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: inst.Organization})
	if err != nil {
		for _, role := range accessRoles(access) {
			report(role, "the organization's teams could not be read: %v", err)
		}
		return problems
	}
	teams := listed.GetTeams()
	resolved := map[string]string{}
	for _, role := range accessRoles(access) {
		if id, named := access.Teams[role]; named {
			if slices.ContainsFunc(teams, func(t *gen.Team) bool { return t.GetId() == id }) {
				resolved[role] = id
				continue
			}
			// The team chosen is gone. Fail closed: the role goes only to a
			// conventional team created now, whose members are nobody's but the
			// installer's choice; an existing one has members nobody chose for
			// this role, so it is granted nothing.
			name := authority.ConventionalTeamName(inst.Target, role)
			if slices.ContainsFunc(teams, func(t *gen.Team) bool { return isConventionalTeam(t, name) }) {
				report(role, "the team chosen for it no longer exists, and %s already exists with members nobody chose for this role; grant the role in Set up", name)
				continue
			}
			team, err := s.conventionalTeam(ctx, caller, inst.Organization, name, &teams)
			if err != nil {
				report(role, "the team chosen for it no longer exists, and %s could not be created: %v", name, err)
				continue
			}
			resolved[role] = team
			report(role, "the team chosen for it no longer exists; granted to %s, created for it", name)
			continue
		}
		name := access.Conventional[role]
		if name == "" {
			name = authority.ConventionalTeamName(inst.Target, role)
		}
		// A team that already holds the conventional name was not made for this
		// role, whoever is in it today: a role is never granted to a team
		// nobody chose for it, as when the chosen team is gone (above). A team
		// this installation already granted the role to is the team of an
		// earlier application of this same access, so applying again changes
		// nothing.
		if i := slices.IndexFunc(teams, func(t *gen.Team) bool { return isConventionalTeam(t, name) }); i >= 0 {
			held, err := s.teamHoldsRole(ctx, caller, inst, role, teams[i].GetId())
			if err != nil {
				report(role, "the roles of %s could not be read: %v", name, err)
				continue
			}
			if !held {
				report(role, "%s already exists and was not made for this role; grant the role in Set up", name)
				continue
			}
		}
		team, err := s.conventionalTeam(ctx, caller, inst.Organization, name, &teams)
		if err != nil {
			report(role, "the team %s could not be created: %v", name, err)
			continue
		}
		resolved[role] = team
	}
	for _, role := range slices.Sorted(maps.Keys(resolved)) {
		grant := authority.RoleRequest{Organization: inst.Organization, InstallationID: inst.ID, Role: role, TeamID: resolved[role]}
		if err := s.GrantInstallationRoles(ctx, caller, []authority.RoleRequest{grant}); err != nil {
			report(role, "the role could not be granted to its team: %v", err)
		}
	}
	if len(access.InstallerRoles) > 0 {
		joined, err := s.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: inst.Organization, MemberId: access.Installer})
		for _, role := range access.InstallerRoles {
			team, ok := resolved[role]
			switch {
			case !ok:
				continue // Reported above: the role has no team.
			case err != nil:
				report(role, "the installer's teams could not be read: %v", err)
				continue
			case slices.ContainsFunc(joined.GetTeams(), func(t *gen.Team) bool { return t.GetId() == team }):
				continue // A member already, in whatever team role they hold.
			}
			if err := s.AddTeamMember(ctx, caller.ID, &gen.AddTeamMemberRequest{TeamId: team, UserId: access.Installer}); err != nil {
				report(role, "the installer could not join its team: %v", err)
			}
		}
	}
	if len(problems) > 0 {
		log := wool.Get(ctx).In("applyInstallationAccess")
		for _, p := range problems {
			log.Warn("an installation's access was not fully applied", wool.Field("installation", inst.ID), wool.Field("role", p.Role), wool.Field("problem", p.Problem))
		}
	}
	return problems
}

// teamHoldsRole reports whether inst already grants role to team.
func (s *Service) teamHoldsRole(ctx context.Context, caller authority.Person, inst domain.Installation, role, team string) (bool, error) {
	core, err := s.AuthorityCore()
	if err != nil {
		return false, err
	}
	grants, err := core.ListRoleGrants(ctx, caller, inst.Organization, inst.ID)
	if err != nil {
		return false, err
	}
	return slices.Contains(grants, domain.RoleGrant{InstallationID: inst.ID, Role: role, TeamID: team}), nil
}

// accessRoles are the roles access assigns, sorted.
func accessRoles(access *InstallationAccess) []string {
	roles := slices.Collect(maps.Keys(access.Teams))
	for role := range access.Conventional {
		if !slices.Contains(roles, role) {
			roles = append(roles, role)
		}
	}
	slices.Sort(roles)
	return roles
}

// conventionalTeam is the id of org's team named name (isConventionalTeam),
// created through CreateTeam when absent and added to teams. A create that
// fails is answered by the team another writer created meanwhile, when the
// organization now holds one.
func (s *Service) conventionalTeam(ctx context.Context, caller authority.Person, org, name string, teams *[]*gen.Team) (string, error) {
	find := func() string {
		if i := slices.IndexFunc(*teams, func(t *gen.Team) bool { return isConventionalTeam(t, name) }); i >= 0 {
			return (*teams)[i].GetId()
		}
		return ""
	}
	if id := find(); id != "" {
		return id, nil
	}
	created, err := s.CreateTeam(ctx, caller.ID, &gen.CreateTeamRequest{OrgId: org, Name: name})
	if err == nil {
		*teams = append(*teams, created.GetTeam())
		return created.GetTeam().GetId(), nil
	}
	listed, listErr := s.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: org})
	if listErr != nil {
		return "", err
	}
	*teams = listed.GetTeams()
	if id := find(); id != "" {
		return id, nil
	}
	return "", err
}

// GrantInstallationRoles grants, as caller, each of grants its installation
// does not hold yet, so granting the same set again changes and journals
// nothing (the core journals every grant it writes, a repeated one too).
func (s *Service) GrantInstallationRoles(ctx context.Context, caller authority.Person, grants []authority.RoleRequest) error {
	core, err := s.AuthorityCore()
	if err != nil {
		return err
	}
	held := map[string][]domain.RoleGrant{}
	for _, grant := range grants {
		granted, read := held[grant.InstallationID]
		if !read {
			if granted, err = core.ListRoleGrants(ctx, caller, grant.Organization, grant.InstallationID); err != nil {
				return err
			}
			held[grant.InstallationID] = granted
		}
		if slices.Contains(granted, domain.RoleGrant{InstallationID: grant.InstallationID, Role: grant.Role, PersonID: grant.PersonID, TeamID: grant.TeamID}) {
			continue
		}
		if _, err := core.GrantRole(ctx, caller, grant); err != nil {
			return fmt.Errorf("grant %s: %w", grant.Role, err)
		}
	}
	return nil
}

// SetUpModulePage sets up who may use the module page targetID at org, as
// caller, in one act: every module is installed by composition and a module
// page's roles decide who may use it, through grants on that composition
// installation, so its set up assigns access exactly as a solution's install
// does (resolveInstallationAccess, applyInstallationAccess) on the
// installation composition made, with no install of its own and no consent:
// a module page holds no standing authority. A target that is a solution, or
// a module declaring no roles, is refused; so is a module page composition
// has not installed in org yet. Setting it up again changes nothing that is
// already so.
func (s *Service) SetUpModulePage(ctx context.Context, caller authority.Person, org, targetID string, requested InstallAccess) (InstalledSolution, error) {
	available, err := s.availableTarget(ctx, org, targetID)
	if err != nil {
		return InstalledSolution{}, err
	}
	if available.Kind != "module" {
		return InstalledSolution{}, domain.Refuse("%s is not a module page: a solution's access is assigned at its install", available.RouteAlias)
	}
	core, err := s.AuthorityCore()
	if err != nil {
		return InstalledSolution{}, err
	}
	m, err := core.Composed(ctx, available.RouteAlias)
	if err != nil {
		return InstalledSolution{}, err
	}
	if len(m.Roles) == 0 {
		return InstalledSolution{}, domain.Refuse("%s declares no roles: there is no access of a person to set up", m.Name)
	}
	installed, err := core.Installations(ctx, caller, org)
	if err != nil {
		return InstalledSolution{}, err
	}
	i := slices.IndexFunc(installed, func(inst domain.Installation) bool {
		// The composition installation: active and installed by nobody. Its
		// receipt says nothing of who installed it: composition's
		// (domain.ComposedConsent), none yet, or, once an administrator
		// approved its upgrade, that administrator's operation, which the
		// core records leaving the installer as it was (none).
		return inst.Target == m.Name && inst.InstalledBy == "" && inst.Status == domain.Active
	})
	if i < 0 {
		return InstalledSolution{}, domain.NotNow("%s is not installed in this organization yet: composition installs it (a solution that reaches it, or supporting module administration); set up its access once it is", m.Name)
	}
	// Every role the installation's consent approved, never one a delivery
	// since asks and nobody approved yet: the core grants no such role.
	roleTeams, err := core.RoleTeamsOf(ctx, installed[i].ID)
	if err != nil {
		return InstalledSolution{}, err
	}
	access, err := s.resolveInstallationAccess(ctx, caller, org, m.Name, roleTeams, requested)
	if err != nil {
		return InstalledSolution{}, err
	}
	return InstalledSolution{Installation: installed[i], AccessProblems: s.applyInstallationAccess(ctx, caller, installed[i], access)}, nil
}

// ApplyInstallAccess assigns, as caller, the access requested on inst, an
// installation already active: exactly what an administrator's install assigns
// on activation (resolveInstallationAccess, applyInstallationAccess), for the
// installation that install made, over every role its consent approved. Every
// step is check-before-write, so applying it again changes nothing. A target
// declaring no roles has nothing to assign.
func (s *Service) ApplyInstallAccess(ctx context.Context, caller authority.Person, inst domain.Installation, requested InstallAccess) ([]AccessProblem, error) {
	core, err := s.AuthorityCore()
	if err != nil {
		return nil, err
	}
	roleTeams, err := core.RoleTeamsOf(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	if len(roleTeams) == 0 {
		return nil, domain.Refuse("%s declares no roles: there is no access of a person to assign", inst.Target)
	}
	access, err := s.resolveInstallationAccess(ctx, caller, inst.Organization, inst.Target, roleTeams, requested)
	if err != nil {
		return nil, err
	}
	return s.applyInstallationAccess(ctx, caller, inst, access), nil
}

// InstallationRoleTeams are the teams holding role on the installation, as
// caller reads its grants: where an administrator adds the people who take
// that role (a team member holds what the team is granted).
func (s *Service) InstallationRoleTeams(ctx context.Context, caller authority.Person, org, installationID, role string) ([]string, error) {
	core, err := s.AuthorityCore()
	if err != nil {
		return nil, err
	}
	grants, err := core.ListRoleGrants(ctx, caller, org, installationID)
	if err != nil {
		return nil, err
	}
	var teams []string
	for _, grant := range grants {
		if grant.Role == role && grant.TeamID != "" && !slices.Contains(teams, grant.TeamID) {
			teams = append(teams, grant.TeamID)
		}
	}
	return teams, nil
}
