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

// InstallationAccess is the access an install resolved and recorded on its
// plan, so a pending attempt that resumes applies the same one: the team of
// the organization each role is granted to once the installation is active,
// by role name, and the person who joins the teams of InstallerRoles then.
type InstallationAccess struct {
	Teams          map[string]string `json:",omitempty"`
	Installer      string            `json:",omitempty"`
	InstallerRoles []string          `json:",omitempty"`
}

// ConventionalTeamName is the team a declared role of target goes to when the
// administrator names none: `<target>-<role>`, lower case, with runs of
// spaces as one dash.
func ConventionalTeamName(target, role string) string {
	return strings.ToLower(strings.Join(strings.Fields(target+"-"+role), "-"))
}

// resolveInstallationAccess is the access caller's install of target assigns:
// for every role target declares, the team requested names for it, which must
// be a team of org, or else the organization's team named ConventionalTeamName
// (case-insensitively), created through CreateTeam when absent. Everything
// requested is checked before any team is created, and no team's members
// change here.
func (s *Service) resolveInstallationAccess(ctx context.Context, caller authority.Person, org string, target domain.Module, requested InstallAccess) (*InstallationAccess, error) {
	roles := slices.Sorted(maps.Keys(target.Roles))
	access := &InstallationAccess{Teams: map[string]string{}, Installer: caller.ID, InstallerRoles: slices.Sorted(slices.Values(requested.InstallerRoles))}
	for _, role := range access.InstallerRoles {
		if !slices.Contains(roles, role) {
			return nil, domain.Malformed("%s declares no role %q", target.Name, role)
		}
	}
	if len(roles) == 0 && len(requested.RoleTeams) == 0 {
		return access, nil
	}
	listed, err := s.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: org})
	if err != nil {
		return nil, err
	}
	teams := listed.GetTeams()
	for role, id := range requested.RoleTeams {
		if !slices.Contains(roles, role) {
			return nil, domain.Malformed("%s declares no role %q", target.Name, role)
		}
		if !slices.ContainsFunc(teams, func(t *gen.Team) bool { return t.GetId() == id }) {
			return nil, domain.Refuse("%q is not a team of this organization", id)
		}
		access.Teams[role] = id
	}
	for _, role := range roles {
		if _, named := requested.RoleTeams[role]; named {
			continue
		}
		name := ConventionalTeamName(target.Name, role)
		i := slices.IndexFunc(teams, func(t *gen.Team) bool { return strings.EqualFold(strings.TrimSpace(t.GetName()), name) })
		if i >= 0 {
			access.Teams[role] = teams[i].GetId()
			continue
		}
		created, err := s.CreateTeam(ctx, caller.ID, &gen.CreateTeamRequest{OrgId: org, Name: name})
		if err != nil {
			return nil, fmt.Errorf("create the team %s for the role %s: %w", name, role, err)
		}
		teams = append(teams, created.GetTeam())
		access.Teams[role] = created.GetTeam().GetId()
	}
	return access, nil
}

// applyInstallationAccess applies access once inst is active: the installer
// joins the team of each role they took, and each role is granted to its
// team. Before activation there is nothing to apply; the resumed attempt that
// activates inst applies it. Both halves are idempotent, so applying it again
// changes nothing.
func (s *Service) applyInstallationAccess(ctx context.Context, caller authority.Person, inst domain.Installation, access *InstallationAccess) error {
	if access == nil || inst.Status != domain.Active {
		return nil
	}
	if len(access.InstallerRoles) > 0 {
		joined, err := s.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: inst.Organization, MemberId: access.Installer})
		if err != nil {
			return err
		}
		for _, role := range access.InstallerRoles {
			team := access.Teams[role]
			if slices.ContainsFunc(joined.GetTeams(), func(t *gen.Team) bool { return t.GetId() == team }) {
				continue // A member already, in whatever team role they hold.
			}
			if err := s.AddTeamMember(ctx, caller.ID, &gen.AddTeamMemberRequest{TeamId: team, UserId: access.Installer}); err != nil {
				return fmt.Errorf("add the installer to the team of %s: %w", role, err)
			}
		}
	}
	grants := make([]authority.RoleRequest, 0, len(access.Teams))
	for _, role := range slices.Sorted(maps.Keys(access.Teams)) {
		grants = append(grants, authority.RoleRequest{Organization: inst.Organization, InstallationID: inst.ID, Role: role, TeamID: access.Teams[role]})
	}
	return s.GrantInstallationRoles(ctx, caller, grants)
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
