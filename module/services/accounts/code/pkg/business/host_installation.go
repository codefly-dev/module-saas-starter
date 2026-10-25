package business

import (
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"errors"
	"fmt"
)

// HostInstallationDetails projects existing consent and exposure, without a
// second installation store or a second grant mechanism.
type HostInstallationDetails struct {
	Installation         *gen.Installation
	Target, RoleID, Role string
	Revision             uint64
	TeamIDs              []string
}

type hostInstallationReader interface {
	InstallationRoleRevision(context.Context, string, string) (roleID, role string, revision uint64, err error)
}

func (s *Service) ReadHostInstallation(ctx context.Context, orgID, id string) (*HostInstallationDetails, error) {
	reader, ok := s.store.(hostInstallationReader)
	if !ok {
		return nil, fmt.Errorf("installation role and revision reader is not wired")
	}
	out := &HostInstallationDetails{}
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		out.Installation, _, err = s.installationStore().GetInstallation(ctx, orgID, id)
		if err != nil {
			return err
		}
		out.RoleID, out.Role, out.Revision, err = reader.InstallationRoleRevision(ctx, orgID, id)
		if err != nil {
			return err
		}
		out.Role = SolutionRoleOf(out.Role)
		exposure, err := s.installationStore().ListInstallationExposure(ctx, orgID, []string{id})
		if err != nil {
			return err
		}
		if one := exposure[id]; one != nil {
			for _, grant := range one.GrantedTeams {
				out.TeamIDs = append(out.TeamIDs, grant.GetGrant().GetSubjectId())
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		rows, err := s.store.ListAvailableSolutionTargets(ctx, AvailableSolutionQuery{TargetID: out.Installation.GetTargetId(), Limit: 1})
		if err == nil && len(rows) == 1 {
			out.Target = rows[0].RouteAlias
		}
		return err
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// ExposeHostInstallation grants a team the installation's role. For a
// solution that declares roles, that is the solution's own role of the
// standing role's name (solutionRoleID), which permits using the solution; an
// installation made holding a catalogue role of that name, before solution
// roles were read, is exposed this way too, with no change to its standing
// grant. Otherwise it is the standing role itself. The existing grant owner
// authenticates team tenancy, requires an active installation, serializes
// with uninstall and emits the scope-grant audit transactionally.
func (s *Service) ExposeHostInstallation(ctx context.Context, actor, org, id, team string) (*HostInstallationDetails, error) {
	details, err := s.ReadHostInstallation(ctx, org, id)
	if err != nil {
		return nil, err
	}
	authority, err := s.installedSolutionAuthority(ctx, org, details.Target)
	if err != nil {
		return nil, err
	}
	return s.exposeHostInstallation(ctx, actor, org, details, team, authority)
}

func (s *Service) exposeHostInstallation(ctx context.Context, actor, org string, details *HostInstallationDetails, team string, authority *SolutionAuthority) (*HostInstallationDetails, error) {
	id := details.Installation.GetId()
	roleID := ""
	if authority != nil {
		var err error
		if roleID, err = s.solutionRoleID(ctx, org, details.Installation.GetTargetId(), details.Target, authority, details.Role); err != nil {
			return nil, err
		}
	}
	if _, err := s.GrantInstallationToTeam(ctx, actor, org, id, team, roleID); err != nil {
		return nil, err
	}
	return s.ReadHostInstallation(ctx, org, id)
}

// installedSolutionAuthority is the role declaration of the target an
// organisation's catalogue offers under alias, or nil when it declares none
// or is no longer offered.
func (s *Service) installedSolutionAuthority(ctx context.Context, org, alias string) (*SolutionAuthority, error) {
	if alias == "" {
		return nil, nil
	}
	available, err := s.availableTargetNamed(ctx, org, alias)
	if errors.Is(err, ErrInstallTargetNotPresent) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.deliveredTargetAuthority(ctx, available)
}
