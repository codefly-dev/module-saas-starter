package business

import (
	"context"
	"errors"
	"fmt"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
)

// Exposure (design: solution lifecycle §5) is the third of an installation's
// states — available, installed, exposed to a team — and none is inferred from
// another. An installation is exposed to a team by a scope grant to that team at
// the installation's authority-root node, of a role that permits using a solution
// (ResourceTypeSolution, ActionUseSolution): exactly the grant
// ListSolutionEntitlements resolves through, so a grant written here is what
// makes the solution appear for that team's members, and nothing else does.
//
// There is no exposure store. A grant is a scope_grants row written by
// grantScopeTx and removed by RevokeScope — the same code every other scope grant
// goes through — and this file only resolves the node from the installation, so
// a caller names an installation and a team and never a scope path.

// InstallationExposure is where one installation is exposed: the scope path of
// its authority root and the team grants that reach it, granted there or
// inherited from above.
type InstallationExposure struct {
	RootScopePath  string
	GrantedTeams   []*gen.CollectionReadGrant
	InheritedTeams []*gen.CollectionReadGrant
}

// OrganizationInstallation is one active installation as its organization's
// administrator manages it.
type OrganizationInstallation struct {
	Summary  *gen.InstallationSummary
	Exposure *InstallationExposure
	// Target is the catalogue row of the installed target, nil when this host no
	// longer offers it.
	Target *AvailableSolutionTarget
}

// ErrRoleDoesNotExposeSolution refuses an exposure grant whose role does not
// permit using a solution: the grant would be written and reveal nothing, which
// is the one outcome an administrator granting "exposure" must never be handed.
var ErrRoleDoesNotExposeSolution = errors.New("role does not permit using a solution")

// ErrTeamOutsideOrganization refuses exposing an installation to a team that
// does not belong to the installation's organization.
var ErrTeamOutsideOrganization = errors.New("team is not in this organization")

// ListOrganizationInstallations lists orgID's ACTIVE installations, one page, with
// live health, exposure and the catalogue row of each installed target.
//
// The installations and their exposure are read in ONE organization
// transaction, so a page never shows a grant beside an installation state from a
// different moment. The catalogue rows are read after it, under the control
// plane, through the same accepted-applied-state query ListAvailableSolutions
// serves — presence is control-plane state the tenant role holds no grant on,
// which is why ListAvailableSolutions reads it there too. They are display facts
// (alias, release, generation) joined on the immutable target, and nothing here
// is authorised by them.
func (s *Service) ListOrganizationInstallations(
	ctx context.Context, orgID, pageToken string, pageSize int,
) ([]*OrganizationInstallation, string, error) {
	w := wool.Get(ctx).In("ListOrganizationInstallations", wool.Field("org_id", orgID))
	if pageSize <= 0 {
		pageSize = listInstallationsDefaultPageSize
	}
	if pageSize > listInstallationsMaxPageSize {
		pageSize = listInstallationsMaxPageSize
	}

	var (
		summaries []*gen.InstallationSummary
		exposure  map[string]*InstallationExposure
	)
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var e error
		summaries, e = s.installationStore().ListInstallations(ctx, orgID,
			gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, pageToken, pageSize+1)
		if e != nil {
			return e
		}
		ids := make([]string, 0, len(summaries))
		for _, summary := range summaries {
			ids = append(ids, summary.GetInstallation().GetId())
		}
		exposure, e = s.installationStore().ListInstallationExposure(ctx, orgID, ids)
		return e
	}); err != nil {
		return nil, "", w.Wrapf(err, "cannot list organization installations")
	}

	next := ""
	if len(summaries) > pageSize {
		summaries = summaries[:pageSize]
		next = summaries[pageSize-1].GetInstallation().GetId()
	}

	targets := map[string]*AvailableSolutionTarget{}
	if len(summaries) > 0 {
		targetIDs := make([]string, 0, len(summaries))
		for _, summary := range summaries {
			if id := summary.GetInstallation().GetTargetId(); id != "" {
				targetIDs = append(targetIDs, id)
			}
		}
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			available, err := s.store.ListAvailableSolutionTargets(ctx, AvailableSolutionQuery{
				OrgID: orgID, TargetIDs: targetIDs, Limit: len(targetIDs) + 1,
			})
			for _, target := range available {
				targets[target.TargetID] = target
			}
			return err
		}); err != nil {
			return nil, "", w.Wrapf(err, "cannot read the installed targets")
		}
	}

	out := make([]*OrganizationInstallation, 0, len(summaries))
	for _, summary := range summaries {
		installation := summary.GetInstallation()
		row := &OrganizationInstallation{
			Summary:  summary,
			Exposure: exposure[installation.GetId()],
			Target:   targets[installation.GetTargetId()],
		}
		if row.Exposure == nil {
			// An active installation always has a root node, so a missing entry is
			// a defect; an empty exposure is still the truthful answer to show.
			row.Exposure = &InstallationExposure{}
		}
		out = append(out, row)
	}
	return out, next, nil
}

// GrantInstallationToTeam exposes one of orgID's active installations to one of
// its teams, by writing a grant of roleID to the team at the installation's
// authority-root node.
//
// Resolving the node, checking the team and the role, and writing the grant run
// in ONE organization transaction through grantScopeTx — the body GrantScope
// runs — so the grant, its scope.granted event and its audit record are those of
// any other scope grant, and the installation cannot be uninstalled between the
// resolution and the write.
func (s *Service) GrantInstallationToTeam(
	ctx context.Context, actorID, orgID, installationID, teamID, roleID string,
) (*gen.ScopeGrant, error) {
	w := wool.Get(ctx).In("GrantInstallationToTeam",
		wool.Field("org_id", orgID), wool.Field("installation_id", installationID))
	var grant *gen.ScopeGrant
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		path, err := s.installationStore().InstallationRootScopePath(ctx, orgID, installationID)
		if err != nil {
			return err
		}
		if err := s.requireTeamInOrgTx(ctx, orgID, teamID); err != nil {
			return err
		}
		permits, err := s.installationStore().RolePermits(ctx, roleID, ResourceTypeSolution, ActionUseSolution)
		if err != nil {
			return err
		}
		if !permits {
			return NewStoreError(fmt.Errorf("%w: role %s does not permit %s:%s, so a grant of it would expose nothing",
				ErrRoleDoesNotExposeSolution, roleID, ResourceTypeSolution, ActionUseSolution), ErrTypeValidation)
		}
		grant, err = s.grantScopeTx(ctx, actorID, &gen.GrantScopeRequest{
			OrgId:       orgID,
			SubjectId:   teamID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
			ScopePath:   path,
			RoleId:      roleID,
		})
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot expose installation to team")
	}
	return grant, nil
}

// RevokeInstallationFromTeam removes the grant of roleID to teamID at one of
// orgID's active installations' authority root.
//
// The node is resolved in the organization's transaction, then the revocation is
// RevokeScope's own: the same policy-log entry and the same apply, so it is
// witnessed before it lands and refused — with the host's reason — when the log
// cannot witness it.
func (s *Service) RevokeInstallationFromTeam(
	ctx context.Context, actorID, orgID, installationID, teamID, roleID string,
) error {
	w := wool.Get(ctx).In("RevokeInstallationFromTeam",
		wool.Field("org_id", orgID), wool.Field("installation_id", installationID))
	var path string
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		path, err = s.installationStore().InstallationRootScopePath(ctx, orgID, installationID)
		if err != nil {
			return err
		}
		return s.requireTeamInOrgTx(ctx, orgID, teamID)
	}); err != nil {
		return w.Wrapf(err, "cannot revoke installation from team")
	}
	req := &gen.RevokeScopeRequest{
		OrgId:       orgID,
		SubjectId:   teamID,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
		ScopePath:   path,
		RoleId:      roleID,
	}
	if err := s.WithPolicyLoggedNarrowing(ctx, revokeScopePolicyLogEntry(
		actorID, orgID, teamID, req.SubjectKind.String(), path, roleID,
	), func(ctx context.Context) error {
		return s.revokeScopeApply(ctx, actorID, req)
	}); err != nil {
		return w.Wrapf(err, "cannot revoke installation from team")
	}
	return nil
}

// requireTeamInOrgTx refuses a team that is not orgID's. It runs inside the
// organization's transaction, where RLS hides every other tenant's team, and
// compares the owning organization as well, so a team of another organization
// is refused whichever of the two gates would have caught it.
func (s *Service) requireTeamInOrgTx(ctx context.Context, orgID, teamID string) error {
	teamOrgID, err := s.store.GetTeamOrgID(ctx, teamID)
	if err != nil {
		return err
	}
	if teamOrgID != orgID {
		return NewStoreError(fmt.Errorf("%w: %s", ErrTeamOutsideOrganization, teamID), ErrTypePermission)
	}
	return nil
}
