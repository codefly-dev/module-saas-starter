package fixtures

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/wool"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// fixtureInstallation is one installation a fixture declares: the organisation
// installing, the target by the name the composition gives it, and the
// administrator who installs it and is its owner of record.
//
// A role-gated target (a solution, or a module page declaring roles) is
// installed exactly as an administrator installs it in the admin area: a
// solution through its reviewed install (InstallReviewedSolution), a module
// page by composition and then its Set up (SetUpModulePage), each with the
// access the row asks for — every declared role to its `<target>-<role>` team,
// created for it, unless the row names a team for its `role`; the installer
// joining the teams of `installer_roles`. Then, as an administrator adds people
// to a team, each person under `members` joins the team holding that role. A
// person holds what the installation's roles give their teams, and nothing
// else.
//
// `role` is the standing role the installer selects where the target's
// declaration selects none (a roleless module installed by name, or a solution
// declaring no installation role), and the role `teams` are granted. A
// declaration that selects one (`installation_role: none` included) is never
// overridden.
//
// A row whose target is EveryTarget stands for every role-gated target the
// composition delivers to this host (a solution, a module page declaring
// roles), whatever the composition is: it is expanded, at every delivery
// pass, into one row per such target the fixture does not name itself, with
// the `members` of the roles that target declares. It names no role, teams or
// installer roles, since those are one target's. It is how one fixture seeds
// the same people onto every composition without naming a target this host
// cannot know.
type fixtureInstallation struct {
	Org    string `yaml:"org"`
	Target string `yaml:"target"`
	Role   string `yaml:"role"`
	Owner  string `yaml:"owner"`
	// Teams names the organisation's existing team `role` goes to, as an
	// administrator may name one instead of the conventional team (one team
	// per role). A host without the authority core exposes the installation to
	// each team named instead.
	Teams []string `yaml:"teams,omitempty"`
	// InstallerRoles are the declared roles the installing administrator takes:
	// they join those roles' teams.
	InstallerRoles []string `yaml:"installer_roles,omitempty"`
	// Members are, by declared role, the people an administrator adds to the
	// team holding that role.
	Members map[string][]string `yaml:"members,omitempty"`
}

// EveryTarget is the target of a row standing for every role-gated target
// the composition delivers.
const EveryTarget = "*"

func (i fixtureInstallation) describe() string {
	return fmt.Sprintf("%q at %q by %s", i.Target, i.Org, i.Owner)
}

// validateInstallations holds each declared installation to the fixture it is
// declared in: the organisation, target and owner named, a role wherever a
// team is to hold one, and an owner the fixture makes an administrator of the
// organisation, as an install requires. Refused here, a typo is read at boot
// instead of as an install refused at every pass.
func validateInstallations(f *fixtureFile) error {
	indexes := make(map[[2]string]int, len(f.Installations))
	for i, installation := range f.Installations {
		if strings.TrimSpace(installation.Org) == "" || strings.TrimSpace(installation.Target) == "" || strings.TrimSpace(installation.Owner) == "" {
			return fmt.Errorf("installation[%d]: org, target and owner are required", i)
		}
		if installation.Target == EveryTarget && (installation.Role != "" || len(installation.Teams) > 0 || len(installation.InstallerRoles) > 0) {
			return fmt.Errorf("installation[%d] (%s): a row standing for every target names only its members: a role, teams and installer roles are one target's", i, installation.describe())
		}
		if strings.TrimSpace(installation.Role) == "" && len(installation.Teams) > 0 {
			return fmt.Errorf("installation[%d] (%s): teams are granted the installation's role, so a row naming teams names a role", i, installation.describe())
		}
		for role, people := range installation.Members {
			if strings.TrimSpace(role) == "" || len(people) == 0 {
				return fmt.Errorf("installation[%d] (%s): members name a role and the people who take it", i, installation.describe())
			}
			for _, email := range people {
				if !fixtureMember(f, installation.Org, email) {
					return fmt.Errorf("installation[%d] (%s): %s is not a member of %q in this fixture, and only a member joins its team",
						i, installation.describe(), email, installation.Org)
				}
			}
		}
		if !fixtureAdministers(f, installation.Org, installation.Owner) {
			return fmt.Errorf("installation[%d] (%s): %s is not an owner or admin of %q in this fixture, and only an administrator installs",
				i, installation.describe(), installation.Owner, installation.Org)
		}
		key := [2]string{installation.Org, installation.Target}
		if previous, exists := indexes[key]; exists {
			return fmt.Errorf("installation[%d]: duplicate identity (org, target) of installation[%d]", i, previous)
		}
		indexes[key] = i
	}
	return nil
}

// fixtureMember reports whether the fixture makes email a member (or the
// owner) of org.
func fixtureMember(f *fixtureFile, org, email string) bool {
	for _, organization := range f.Organizations {
		if organization.Name != org {
			continue
		}
		if organization.Owner == email {
			return true
		}
		for _, member := range organization.Members {
			if member.Email == email {
				return true
			}
		}
	}
	return false
}

func fixtureAdministers(f *fixtureFile, org, email string) bool {
	for _, organization := range f.Organizations {
		if organization.Name != org {
			continue
		}
		if organization.Owner == email {
			return true
		}
		for _, member := range organization.Members {
			if member.Email == email && (member.Role == "admin" || member.Role == "owner") {
				return true
			}
		}
	}
	return false
}

// Installations are a seeded fixture's declared installations, performed
// through the host's own install paths as each installation's owner: the
// reviewed install for a role-gated target (coreInstall), the install by name
// (business.Service.InstallFixtureTarget) for a roleless module. Neither
// depends on the rows' order: a solution's review installs its roleless
// closure itself and never a module page it reaches. A target is installable
// only once the composition has delivered it to this host, which happens after
// the seed, so they are converged after every delivery pass rather than at
// seed time.
type Installations struct {
	service *business.Service

	mu      sync.Mutex
	pending []pendingInstallation
	// every are the rows standing for every role-gated target (EveryTarget),
	// expanded at each pass into pending rows for the targets delivered since;
	// expanded records, by organisation and target, each row so made or
	// declared, so a target is expanded once and a declared row is kept.
	every    []pendingInstallation
	expanded map[[2]string]struct{}
}

type pendingInstallation struct {
	declared fixtureInstallation
	orgID    string
	ownerID  string
	// teamIDs are the declared teams not exposed yet, allTeamIDs every one.
	teamIDs, allTeamIDs []string
	// memberIDs are, by declared role, the people who join its team.
	memberIDs map[string][]string
	// waiting is set once the first pass found the target undelivered, so the
	// wait is said once rather than at every pass.
	waiting bool
}

func newInstallations(service *business.Service, declared []fixtureInstallation, orgIDs, userIDs, teamIDs map[string]string) (*Installations, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	installations := &Installations{service: service, expanded: map[[2]string]struct{}{}}
	for _, installation := range declared {
		orgID, ok := orgIDs[installation.Org]
		if !ok {
			return nil, fmt.Errorf("installation %s references unknown organization %q", installation.describe(), installation.Org)
		}
		ownerID, ok := userIDs[installation.Owner]
		if !ok {
			return nil, fmt.Errorf("installation %s references unknown owner %q", installation.describe(), installation.Owner)
		}
		row := pendingInstallation{declared: installation, orgID: orgID, ownerID: ownerID}
		for _, team := range installation.Teams {
			teamID, ok := teamIDs[fixtureScopedKey(installation.Org, team)]
			if !ok {
				return nil, fmt.Errorf("installation %s references team %q, which the fixture does not seed in %q", installation.describe(), team, installation.Org)
			}
			row.teamIDs = append(row.teamIDs, teamID)
			row.allTeamIDs = append(row.allTeamIDs, teamID)
		}
		for role, people := range installation.Members {
			for _, email := range people {
				userID, ok := userIDs[email]
				if !ok {
					return nil, fmt.Errorf("installation %s references unknown member %q", installation.describe(), email)
				}
				if row.memberIDs == nil {
					row.memberIDs = map[string][]string{}
				}
				row.memberIDs[role] = append(row.memberIDs[role], userID)
			}
		}
		if installation.Target == EveryTarget {
			installations.every = append(installations.every, row)
			continue
		}
		installations.pending = append(installations.pending, row)
		installations.expanded[[2]string{row.orgID, installation.Target}] = struct{}{}
	}
	return installations, nil
}

// expandEvery turns each row standing for every target into one pending row
// per role-gated target the composition has delivered to the row's
// organisation and no row names yet. A target delivered later is expanded by
// a later pass; a refusal to list is reported and tried again next pass.
func (i *Installations) expandEvery(ctx context.Context) error {
	if len(i.every) == 0 {
		return nil
	}
	core, err := i.service.AuthorityCore()
	if err != nil {
		return nil // Without the core there is no role-gated target to stand for.
	}
	var failures []error
	for _, template := range i.every {
		for token := ""; ; {
			available, next, err := i.service.ListAvailableSolutions(ctx, template.orgID, token, 0)
			if err != nil {
				failures = append(failures, fmt.Errorf("fixture installation %s: list the composition's targets: %w", template.declared.describe(), err))
				break
			}
			for _, target := range available {
				key := [2]string{template.orgID, target.RouteAlias}
				if _, done := i.expanded[key]; done {
					continue
				}
				m, err := core.Composed(ctx, target.RouteAlias)
				if err != nil || (m.Kind != domain.KindSolution && len(m.Roles) == 0) {
					continue // Not composed yet, or role-gated by nothing: no team to seed.
				}
				i.pending = append(i.pending, everyRow(template, m))
				i.expanded[key] = struct{}{}
			}
			if token = next; token == "" {
				break
			}
		}
	}
	return errors.Join(failures...)
}

// everyRow is the row a template standing for every target makes for one: the
// template's organisation, owner and members, the members cut to the roles
// the target declares (a role it does not declare has no team to join).
func everyRow(template pendingInstallation, m domain.Module) pendingInstallation {
	row := template
	row.declared.Target = m.Name
	row.memberIDs = nil
	for role, people := range template.memberIDs {
		if _, declared := m.Roles[role]; !declared {
			continue
		}
		if row.memberIDs == nil {
			row.memberIDs = map[string][]string{}
		}
		row.memberIDs[role] = people
	}
	return row
}

// Converge installs every declared installation whose target this host now
// offers. The host's own setup is explicit consent: creation records it and a
// reseed re-consents an existing module at its current ceiling through the same
// admin install path. A target not delivered yet stays pending for the next
// pass; any other refusal is returned, naming
// the installation, and stays pending too. An installation performed or found
// is not asked again by this process.
func (i *Installations) Converge(ctx context.Context) error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	// Converge runs inside a delivery pass: no install step waits on a
	// solution's setup here (a pending one is resumed by the next pass).
	ctx = business.WithoutSetupWait(ctx)
	w := wool.Get(ctx).In("fixtures.Installations")
	var failures []error
	if err := i.expandEvery(ctx); err != nil {
		failures = append(failures, err)
	}
	remaining := i.pending[:0]
	for _, row := range i.pending {
		if active, err := i.coreInstall(ctx, row); active || err != nil {
			if err != nil {
				switch {
				case errors.Is(err, errCoreTargetNotComposed):
					// Said once per row: the composition delivers the rest.
					if !row.waiting {
						w.Info("fixture installation waits for the composition", wool.Field("installation", row.declared.describe()), wool.ErrField(err))
						row.waiting = true
					}
				case !setupPending(err):
					failures = append(failures, fmt.Errorf("fixture installation %s: %w", row.declared.describe(), err))
				}
				remaining = append(remaining, row)
			}
			continue
		}
		// A module is installed by composition in every organization: a row
		// naming one that another owner holds is satisfied by it, never
		// installed again under the row's own owner or role.
		installation, installed, err := i.composedModule(ctx, row)
		if err == nil && installation == nil {
			installation, installed, err = i.service.InstallFixtureTarget(ctx, row.ownerID, row.orgID, row.declared.Target, row.declared.Role, row.ownerID)
		}
		switch {
		case errors.Is(err, business.ErrInstallTargetNotPresent):
			if !row.waiting {
				w.Info("fixture installation waits for its target to be delivered", wool.Field("installation", row.declared.describe()), wool.ErrField(err))
				row.waiting = true
			}
			remaining = append(remaining, row)
		case err != nil:
			failures = append(failures, fmt.Errorf("fixture installation %s: %w", row.declared.describe(), err))
			remaining = append(remaining, row)
		default:
			if installed {
				w.Info("fixture installation installed", wool.Field("installation", row.declared.describe()), wool.Field("installation_id", installation.GetId()))
			}
			// Each declared team is exposed once the installation admits it (an
			// installation still pending its install step does not); one not
			// exposed yet keeps the row for the next pass.
			unexposed := row.teamIDs[:0]
			for _, teamID := range row.teamIDs {
				if _, err := i.service.ExposeHostInstallation(ctx, row.ownerID, row.orgID, installation.GetId(), teamID); err != nil {
					failures = append(failures, fmt.Errorf("fixture installation %s: expose to team %s: %w", row.declared.describe(), teamID, err))
					unexposed = append(unexposed, teamID)
				}
			}
			if row.teamIDs = unexposed; len(row.teamIDs) > 0 {
				remaining = append(remaining, row)
			}
		}
	}
	i.pending = remaining
	return errors.Join(failures...)
}

// composedModule is the installation of the module row names that another
// owner holds at its organization (composition's, under the organization's
// owner of record): the row is satisfied by it, never installed again under
// the row's own owner. nil when the target is no module, holds none yet, or
// is the row owner's own, which the fixture consents to again as before.
func (i *Installations) composedModule(ctx context.Context, row pendingInstallation) (*gen.Installation, bool, error) {
	installation, err := i.service.ActiveOrPendingModuleInstallation(ctx, row.orgID, row.declared.Target)
	if errors.Is(err, business.ErrInstallTargetNotPresent) {
		return nil, false, nil
	}
	if err != nil || installation == nil || installation.GetOwnerPrincipalId() == row.ownerID {
		return nil, false, err
	}
	return installation, false, nil
}

// setupPending reports a solution row whose setup has not answered yet: the
// next pass resumes it, so it is no failure of this one.
func setupPending(err error) bool {
	return strings.Contains(err.Error(), "INSTALLATION_SETUP_PENDING")
}

// errCoreTargetNotComposed is a declared installation whose target the
// authority core has not composed yet; the next pass installs it there.
var errCoreTargetNotComposed = errors.New("the authority core has not composed this target yet")

// coreInstall installs a role-gated target as an administrator does in the
// admin area, with the access the row asks for (installAccess): a solution
// through its reviewed install (InstallReviewedSolution), which converges its
// closure of supporting modules with it and assigns the access once active; a
// module page by composition and then its Set up (modulePageInstall). An
// installation the core holds active already is assigned the same access
// again, check-before-write, so a pass over a converged row changes nothing;
// one it holds pending is resumed from the review it was opened under while
// that review is the owner's and still current, and superseded by a fresh one
// otherwise. Then the row's members join their roles' teams (joinMembers). A
// module without roles answers false: Converge installs it by name, as
// composition does.
func (i *Installations) coreInstall(ctx context.Context, row pendingInstallation) (bool, error) {
	core, err := i.service.AuthorityCore()
	if errors.Is(err, business.ErrAuthorityCoreUnconfigured) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A target the core has not composed yet is waited on: the pass that
	// applies its delivery composes it.
	m, err := core.Composed(ctx, row.declared.Target)
	if code, ok := domain.CodeOf(err); ok && code == domain.NotFound {
		return false, fmt.Errorf("%w: %s", errCoreTargetNotComposed, row.declared.Target)
	}
	if err != nil {
		return false, fmt.Errorf("the authority core: %w", err)
	}
	if m.Kind != domain.KindSolution && len(m.Roles) == 0 {
		if len(row.declared.InstallerRoles) > 0 || len(row.declared.Members) > 0 {
			return false, fmt.Errorf("%s declares no roles: installer_roles and members have no team to join", m.Name)
		}
		return false, nil
	}
	// Installed once every module the target reaches is composed. A role
	// reaches a module through a binding only once that module is composed,
	// and an installed target is held at the reach it had when installed. A
	// module page's composition receipt is taken again when a module it
	// reaches is composed for the first time; a solution's consent is an
	// administrator's, so that module's arrival offers an upgrade instead,
	// every grant kept at the narrower reach until it is approved. Set up
	// against its whole reach, a target's people hold what it reads through
	// its bindings from the first pass that installs it. A later pass
	// installs it.
	missing, err := i.uncomposedReach(ctx, m)
	if err != nil {
		return false, fmt.Errorf("the authority core: %w", err)
	}
	if len(missing) > 0 {
		return false, fmt.Errorf("%w: %s reaches %s, not composed yet", errCoreTargetNotComposed, m.Name, strings.Join(missing, ", "))
	}
	access := installAccess(row)
	caller := authority.Person{ID: row.ownerID, Organization: row.orgID}
	if m.Kind != domain.KindSolution {
		return i.modulePageInstall(ctx, row, caller, m, access)
	}
	all, err := core.Installations(ctx, caller, row.orgID)
	if err != nil {
		return false, fmt.Errorf("the authority core: %w", err)
	}
	// A declaration that selects no installation role leaves it to the
	// installer: the row's role. One that selects a role is never overridden.
	role := ""
	if m.InstallationRole == "" {
		role = row.declared.Role
	}
	inst, held := heldInstallation(all, authority.StableInstallationID(row.orgID, row.declared.Target))
	var problems []business.AccessProblem
	if held && inst.Status == domain.Active {
		if problems, err = i.service.ApplyInstallAccess(ctx, caller, inst, access); err != nil {
			return false, fmt.Errorf("the authority core: %w", err)
		}
	} else {
		review, err := i.reviewFor(ctx, caller, m, role, inst, held)
		if err != nil {
			return false, err
		}
		installed, err := i.service.InstallReviewedSolutionAs(ctx, caller, row.orgID, review, role, access)
		if err != nil {
			return false, fmt.Errorf("the authority core: %w", err)
		}
		if inst, problems = installed.Installation, installed.AccessProblems; inst.Status != domain.Active {
			return false, fmt.Errorf("%w: %s is pending its install step", errCoreTargetNotComposed, row.declared.Target)
		}
	}
	if err := accessRefused(problems); err != nil {
		return false, err
	}
	if err := i.alsoGrant(ctx, row, caller, inst.ID); err != nil {
		return false, err
	}
	return true, i.joinMembers(ctx, row, caller, inst.ID)
}

// uncomposedReach is each audience m's bindings name, and onward each one a
// module reached names, that the core does not compose yet, sorted. A role
// reaches a module through a binding only once that module is composed
// (decide.ReachOf), so the module's delivery widens the role.
func (i *Installations) uncomposedReach(ctx context.Context, m domain.Module) ([]string, error) {
	var missing []string
	seen := map[string]bool{m.Principal: true}
	for queue := []domain.Module{m}; len(queue) > 0; queue = queue[1:] {
		for _, id := range slices.Sorted(maps.Keys(queue[0].Bindings)) {
			audience := queue[0].Bindings[id].Audience
			if seen[audience] {
				continue
			}
			seen[audience] = true
			reached, err := i.service.ComposedAudience(ctx, audience)
			if code, ok := domain.CodeOf(err); errors.Is(err, domain.ErrNotFound) || (ok && code == domain.NotFound) {
				missing = append(missing, audience)
				continue
			}
			if err != nil {
				return nil, err
			}
			queue = append(queue, reached)
		}
	}
	slices.Sort(missing)
	return missing, nil
}

// installAccess is the access a row asks an install for, as the admin area
// asks it: the first team named for the row's role, if any (an install names
// one team per role), and the roles the installer takes. A further team named
// is granted the role afterwards, as an administrator grants a role to another
// team in Set up (alsoGrant).
func installAccess(row pendingInstallation) business.InstallAccess {
	access := business.InstallAccess{InstallerRoles: row.declared.InstallerRoles}
	if len(row.allTeamIDs) > 0 {
		access.RoleTeams = map[string]string{row.declared.Role: row.allTeamIDs[0]}
	}
	return access
}

// alsoGrant grants the row's role to every further team it names, on the
// installation, as an administrator grants a role to a team in Set up.
func (i *Installations) alsoGrant(ctx context.Context, row pendingInstallation, caller authority.Person, installationID string) error {
	if len(row.allTeamIDs) < 2 {
		return nil
	}
	var grants []authority.RoleRequest
	for _, teamID := range row.allTeamIDs[1:] {
		grants = append(grants, authority.RoleRequest{Organization: row.orgID, InstallationID: installationID, Role: row.declared.Role, TeamID: teamID})
	}
	if err := i.service.GrantInstallationRoles(ctx, caller, grants); err != nil {
		return fmt.Errorf("the authority core: %w", err)
	}
	return nil
}

// accessRefused is the access an install could not assign, as an error: the
// fixture's people would otherwise hold less than it declares, silently.
func accessRefused(problems []business.AccessProblem) error {
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("access not assigned: %s", strings.Join(business.AccessProblemsToWire(problems), "; "))
}

// joinMembers adds each of the row's members to the team their role went to
// at install (the first team the row names for its role, else the role's
// `<target>-<role>` team), as an administrator adds a person to a team; one
// who is a member already is left as they are.
func (i *Installations) joinMembers(ctx context.Context, row pendingInstallation, caller authority.Person, installationID string) error {
	for _, role := range slices.Sorted(maps.Keys(row.memberIDs)) {
		team, err := i.roleTeam(ctx, row, caller, installationID, role)
		if err != nil {
			return err
		}
		for _, userID := range row.memberIDs[role] {
			joined, err := i.service.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: row.orgID, MemberId: userID})
			if err != nil {
				return err
			}
			if slices.ContainsFunc(joined.GetTeams(), func(t *gen.Team) bool { return t.GetId() == team }) {
				continue
			}
			if err := i.service.AddTeamMember(ctx, row.ownerID, &gen.AddTeamMemberRequest{TeamId: team, UserId: userID}); err != nil {
				return fmt.Errorf("%s: add a member to the %s team: %w", row.declared.describe(), role, err)
			}
		}
	}
	return nil
}

// roleTeam is the team role went to at install: the first team the row names
// for its role, else the role's `<target>-<role>` team holding it.
func (i *Installations) roleTeam(ctx context.Context, row pendingInstallation, caller authority.Person, installationID, role string) (string, error) {
	if role == row.declared.Role && len(row.allTeamIDs) > 0 {
		return row.allTeamIDs[0], nil
	}
	holding, err := i.service.InstallationRoleTeams(ctx, caller, row.orgID, installationID, role)
	if err != nil {
		return "", fmt.Errorf("the authority core: %w", err)
	}
	listed, err := i.service.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: row.orgID})
	if err != nil {
		return "", err
	}
	name := business.ConventionalTeamName(row.declared.Target, role)
	for _, team := range listed.GetTeams() {
		if slices.Contains(holding, team.GetId()) && strings.EqualFold(team.GetName(), name) {
			return team.GetId(), nil
		}
	}
	return "", fmt.Errorf("%s: no %s team holds %s on the installation, so its members have no team to join", row.declared.describe(), name, role)
}

// modulePageInstall installs a module page as every module is installed, by
// composition, holding no standing authority (the row's role names who may use
// it, never what it holds), then sets it up as an administrator does
// (SetUpModulePage): each declared role to its team, the installer joining the
// teams of the roles they take; then the row's members join their teams. One
// still pending its install step waits for the next pass; one installed
// already is not installed or consented to again.
func (i *Installations) modulePageInstall(ctx context.Context, row pendingInstallation, caller authority.Person, m domain.Module, access business.InstallAccess) (bool, error) {
	// Installed once: a pass over a converged row consents to nothing again.
	var installation *gen.Installation
	if id, err := i.service.WorkContextAudienceInstallation(ctx, row.orgID, row.declared.Target); err == nil && id != "" {
		if installation, _, err = i.service.GetInstallation(ctx, row.orgID, id); err != nil {
			return false, err
		}
	} else if installation, _, err = i.service.InstallFixtureTarget(ctx, row.ownerID, row.orgID, row.declared.Target, "", row.ownerID); err != nil {
		return false, err
	}
	if installation.GetStatus() != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
		return false, fmt.Errorf("%w: %s is pending its install step", errCoreTargetNotComposed, row.declared.Target)
	}
	target, err := i.service.ResolveInstallationPrincipal(ctx, row.orgID, m.Principal)
	if err != nil {
		return false, err
	}
	setUp, err := i.service.SetUpModulePage(ctx, caller, row.orgID, target.TargetID, access)
	if err != nil {
		return false, fmt.Errorf("set up %s: %w", row.declared.Target, err)
	}
	if err := accessRefused(setUp.AccessProblems); err != nil {
		return false, err
	}
	if err := i.alsoGrant(ctx, row, caller, setUp.ID); err != nil {
		return false, err
	}
	return true, i.joinMembers(ctx, row, caller, setUp.ID)
}

// reviewFor is the review the owner installs m with: the one a pending
// attempt of theirs was opened under while it is still current, which resumes
// it (what that attempt installed is installed again under the same
// operation, idempotently), or a fresh one. A review a delivery has moved past
// is refused on every replay, so the owner's fresh review supersedes that
// attempt instead, as an administrator's does.
func (i *Installations) reviewFor(ctx context.Context, caller authority.Person, m domain.Module, role string, inst domain.Installation, held bool) (*gen.ReviewedSolutionInstall, error) {
	if store, ok := i.service.Store().(business.CanonicalInstallationSetupStore); ok && held && inst.Status == domain.Pending {
		plan, err := store.SolutionInstallationPlan(ctx, caller.Organization, inst.ID)
		if err == nil && plan.Review != nil && plan.Review.ActorId == caller.ID && plan.RequestedRole == role && plan.RequestedOwner == "" {
			if i.service.ReviewCurrent(ctx, plan.Review) {
				return plan.Review, nil
			}
			wool.Get(ctx).In("fixtures.Installations").Info("a pending fixture installation's review is no longer current; it is reviewed again",
				wool.Field("target", m.Name), wool.Field("installation_id", inst.ID), wool.Field("reviewed_generation", plan.Review.GetApproval().GetGeneration()))
		}
	}
	target, err := i.service.ResolveInstallationPrincipal(ctx, caller.Organization, m.Principal)
	if err != nil {
		return nil, err
	}
	return i.service.ReviewSolutionInstallAs(ctx, caller, caller.Organization, target.TargetID, role)
}

// ConvergeAuthority performs every declared installation in the authority core
// alone, for a host that serves the host interfaces without the product's own
// installation records (the conformance harness). A target the composition
// has not delivered yet is waited on.
func (i *Installations) ConvergeAuthority(ctx context.Context) error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	var failures []error
	if err := i.expandEvery(ctx); err != nil {
		failures = append(failures, err)
	}
	for _, row := range i.pending {
		_, err := i.coreInstall(ctx, row)
		if code, ok := domain.CodeOf(err); errors.Is(err, errCoreTargetNotComposed) || ok && code == domain.NotFound {
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("fixture installation %s: %w", row.declared.describe(), err))
		}
	}
	return errors.Join(failures...)
}

// heldInstallation is the core's installation id among all, whatever its
// status, and whether the core holds it at all.
func heldInstallation(all []domain.Installation, id string) (domain.Installation, bool) {
	for _, inst := range all {
		if inst.ID == id {
			return inst, true
		}
	}
	return domain.Installation{}, false
}
