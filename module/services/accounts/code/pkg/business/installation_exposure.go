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

// OrganizationInstallation is one nonrevoked installation as its organization's
// administrator manages it.
type OrganizationInstallation struct {
	Summary  *gen.InstallationSummary
	Exposure *InstallationExposure
	// Target is the catalogue row of the installed target, nil when this host no
	// longer offers it.
	Target *AvailableSolutionTarget
	// Readiness is whether this installation will work, resolved live beside the
	// health that only answers whether its record exists
	// (installation_readiness.go).
	Readiness InstallationReadiness
	// ReachedModules is what this target declares it reaches on the asking
	// person's own session, resolved to this organisation's installation of each.
	ReachedModules []ReachedModule
}

// ErrRoleDoesNotExposeSolution refuses an exposure grant whose role does not
// permit using a solution: the grant would be written and reveal nothing, which
// is the one outcome an administrator granting "exposure" must never be handed.
var ErrRoleDoesNotExposeSolution = errors.New("role does not permit using a solution")

// ListOrganizationInstallations lists orgID's installations, one page, with
// live health, exposure and the catalogue row of each installed target.
//
// The installations and their exposure are read on ONE read-only REPEATABLE
// READ snapshot of the organization (WithOrgReadSnapshot), so a page never shows
// an installation beside exposure from a different moment — an uninstall that
// commits between the two reads is seen by both or by neither. The catalogue rows are read after it, under the control
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
		facts     *readinessFacts
	)
	if err := s.installationStore().WithOrgReadSnapshot(ctx, orgID, func(ctx context.Context) error {
		var e error
		summaries, e = s.installationStore().ListInstallations(ctx, orgID,
			gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED, pageToken, pageSize+1)
		if e != nil {
			return e
		}
		ids := make([]string, 0, len(summaries))
		for _, summary := range summaries {
			ids = append(ids, summary.GetInstallation().GetId())
		}
		if exposure, e = s.installationStore().ListInstallationExposure(ctx, orgID, ids); e != nil {
			return e
		}
		// The organisation half of readiness, on the same snapshot: a source is
		// never reported undelegated against a delegation list read at another
		// moment.
		facts, e = s.readReadinessFacts(ctx, orgID)
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
			if err != nil {
				return err
			}
			// What each applied manifest asks for is control-plane state too,
			// read here beside the catalogue rows rather than once per row.
			applied, err := s.store.ListSolutionHostBindings(ctx)
			if err != nil {
				return err
			}
			facts.withAppliedManifests(applied)
			return nil
		}); err != nil {
			return nil, "", w.Wrapf(err, "cannot read the installed targets")
		}
	}

	// Which module principal this organisation holds an active installation of,
	// so a declared audience resolves to the installation a team grant is
	// written at. Built from the rows of this very listing: a reached module is
	// offered only where the organisation actually installed it.
	installedByPrincipal := map[string]*gen.Installation{}
	for _, summary := range summaries {
		installation := summary.GetInstallation()
		if installation.GetStatus() != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
			continue
		}
		target := targets[installation.GetTargetId()]
		if target == nil {
			continue
		}
		if asked, ok := facts.askedByBinding[target.BindingID]; ok && asked.Authority != nil {
			installedByPrincipal[asked.Authority.Principal] = installation
		}
	}

	out := make([]*OrganizationInstallation, 0, len(summaries))
	for _, summary := range summaries {
		installation := summary.GetInstallation()
		if installation.Status == gen.InstallationStatus_INSTALLATION_STATUS_REVOKED {
			continue
		}
		row := &OrganizationInstallation{
			Summary:  summary,
			Exposure: exposure[installation.GetId()],
			Target:   targets[installation.GetTargetId()],
		}
		row.Readiness, row.ReachedModules = facts.readinessFor(
			installation, summary.GetHealth(), row.Target, installedByPrincipal)
		for i := range row.ReachedModules {
			if id := row.ReachedModules[i].InstallationID; id != "" {
				if reachedTarget := targets[installedTargetID(summaries, id)]; reachedTarget != nil {
					row.ReachedModules[i].RouteAlias = reachedTarget.RouteAlias
				}
			}
		}
		if row.Exposure == nil && installation.Status == gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
			// Both reads share one snapshot and an active installation always has
			// a root node, so a missing entry is a defect rather than a race. It
			// fails the listing instead of showing an active row with no root.
			return nil, "", w.Wrapf(fmt.Errorf("installation %s has no authority root in the same snapshot", installation.GetId()),
				"cannot list organization installations")
		}
		if row.Exposure == nil {
			row.Exposure = &InstallationExposure{}
		}
		out = append(out, row)
	}
	return out, next, nil
}

// GrantInstallationToTeam exposes one of orgID's active installations to one of
// its teams, by writing a grant of roleID to the team at the installation's
// authority-root node — and CARRIES, in the same transaction, the access the
// solution declares it consumes on every module it reaches.
//
// Resolving the node, checking the team and the role, and writing the grant run
// in ONE organization transaction through grantScopeTx — the body GrantScope
// runs — so the grant, its scope.granted event and its audit record are those of
// any other scope grant. The carried grants are in that same transaction and are
// each an ordinary grant too, so a grant that exposed a solution and carried
// nothing because a later statement failed is not a state this can reach.
//
// The resolution takes an UPDATE lock on the installation row and holds it to
// commit (InstallationRootScopePath). An uninstall revokes that row with an
// UPDATE, so the two serialize: if the uninstall commits first, the resolution
// finds no active installation and nothing is written; if the resolution locks
// first, the uninstall waits for this grant to commit. A grant therefore never
// lands on an installation that was already uninstalled — and the same holds for
// every module it reaches, because the carried grants resolve their node the
// same way.
//
// The role must permit using a solution — that is the minimum for the grant to
// expose anything — but it is not a ceiling. A broader role grants its full
// authority beneath the installation's authority root, exactly as GrantScope
// with the same role at the same node would. It IS a ceiling on what is carried:
// what a reached module's derived grant permits is the solution's declared
// consumption INTERSECTED with this role, never the role itself
// (derived_module_access.go).
//
// What the grant carries is read from the organisation's own installation
// listing — the same read the setup page renders — rather than derived a second
// time here. The two can therefore never disagree about which modules a solution
// reaches, and there is one mechanism for that question rather than two. It is
// read before the transaction because the declaration and the catalogue are
// control-plane state, which a tenant transaction holds no grant on; the SHARE
// lock on each installation is what makes the facts still true at the write.
func (s *Service) GrantInstallationToTeam(
	ctx context.Context, actorID, orgID, installationID, teamID, roleID string,
) (*InstallationGrant, error) {
	w := wool.Get(ctx).In("GrantInstallationToTeam",
		wool.Field("org_id", orgID), wool.Field("installation_id", installationID))
	row, err := s.organizationInstallation(ctx, orgID, installationID)
	if err != nil {
		return nil, w.Wrapf(err, "cannot expose installation to team")
	}
	plans, unmet := planCarriedModuleAccess(row.ReachedModules)
	alias := ""
	if row.Target != nil {
		alias = row.Target.RouteAlias
	}

	out := &InstallationGrant{Unmet: unmet}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		path, err := s.installationStore().InstallationRootScopePath(ctx, orgID, installationID)
		if err != nil {
			return err
		}
		// The host interface names the installation's own role. Resolve it
		// under the same installation lock as the exposure write.
		if roleID == "" {
			reader, ok := s.store.(hostInstallationReader)
			if !ok {
				return fmt.Errorf("installation role reader is not wired")
			}
			roleID, _, _, err = reader.InstallationRoleRevision(ctx, orgID, installationID)
			if err != nil {
				return err
			}
		}
		permits, err := s.installationStore().RolePermits(ctx, roleID, ResourceTypeSolution, ActionUseSolution)
		if err != nil {
			return err
		}
		if !permits {
			return NewStoreError(fmt.Errorf("%w: role %s does not permit %s:%s, so a grant of it would expose nothing",
				ErrRoleDoesNotExposeSolution, roleID, ResourceTypeSolution, ActionUseSolution), ErrTypeValidation)
		}
		out.Grant, err = s.grantScopeTx(ctx, actorID, &gen.GrantScopeRequest{
			OrgId:       orgID,
			SubjectId:   teamID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
			ScopePath:   path,
			RoleId:      roleID,
		}, "")
		if err != nil {
			return err
		}
		carried, carryUnmet, err := s.carryModuleAccessTx(ctx, actorID, orgID, installationID, teamID, alias, roleID, out.Grant, plans)
		if err != nil {
			return err
		}
		out.Carried, out.Unmet = carried, append(unmet, carryUnmet...)
		return nil
	}); err != nil {
		return nil, w.Wrapf(err, "cannot expose installation to team")
	}
	return out, nil
}

// organizationInstallation is one installation's row of its organisation's own
// listing, the read the setup page renders. Reused rather than re-derived: what
// a grant carries has to be what the page said it would carry, and a second
// derivation of "which modules does this solution reach" would be a second
// answer to a question this host already answers in one place.
//
// It pages until it finds the row, because the listing is a page of up to 500
// and an organisation's installations are not addressable by id in it.
func (s *Service) organizationInstallation(ctx context.Context, orgID, installationID string) (*OrganizationInstallation, error) {
	for token := ""; ; {
		rows, next, err := s.ListOrganizationInstallations(ctx, orgID, token, listInstallationsMaxPageSize)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Summary.GetInstallation().GetId() == installationID {
				return row, nil
			}
		}
		if next == "" {
			return nil, NewStoreError(
				fmt.Errorf("active installation %s not found", installationID), ErrTypeNotFound)
		}
		token = next
	}
}

// RevokeInstallationFromTeam removes the grant of roleID to teamID at one of
// orgID's active installations' authority root — and every grant that grant
// CAUSED at the modules the solution reaches.
//
// The node is resolved in the organization's transaction, then the revocation is
// RevokeScope's own: the same policy-log entry and the same apply, so it is
// witnessed before it lands and refused — with the host's reason — when the log
// cannot witness it.
//
// The derived grants go FIRST and in the same narrowing, so there is no moment,
// and no failure, that leaves a carried grant alive after the grant that caused
// it. Each goes through revokeScopeApply, so each carries the scope.revoked
// event and the audit record of any other revocation rather than disappearing
// silently. A team's own direct grant on a reached module has no cause and is a
// different row: it is untouched.
func (s *Service) RevokeInstallationFromTeam(
	ctx context.Context, actorID, orgID, installationID, teamID, roleID string,
) error {
	w := wool.Get(ctx).In("RevokeInstallationFromTeam",
		wool.Field("org_id", orgID), wool.Field("installation_id", installationID))
	var path, causeGrantID string
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		path, err = s.installationStore().InstallationRootScopePath(ctx, orgID, installationID)
		if err != nil {
			return err
		}
		if err := s.requireSubjectInOrgTx(ctx, orgID, teamID, gen.SubjectKind_SUBJECT_KIND_TEAM); err != nil {
			return err
		}
		// The cause's own id, read from the exposure listing this installation's
		// row is already read through, so "which grant is being removed" is
		// answered by the same read that reports it to an administrator.
		exposure, err := s.installationStore().ListInstallationExposure(ctx, orgID, []string{installationID})
		if err != nil {
			return err
		}
		at := exposure[installationID]
		if at == nil {
			return nil
		}
		for _, granted := range at.GrantedTeams {
			if granted.GetGrant().GetSubjectId() == teamID && granted.GetGrant().GetRoleId() == roleID {
				causeGrantID = granted.GetGrant().GetId()
			}
		}
		return nil
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
		if causeGrantID != "" {
			if err := s.revokeDerivedGrantsTx(ctx, actorID, orgID, causeGrantID, ""); err != nil {
				return err
			}
		}
		return s.revokeScopeApply(ctx, actorID, req)
	}); err != nil {
		return w.Wrapf(err, "cannot revoke installation from team")
	}
	return nil
}

// installedTargetID is the target one of this page's installations records, so a
// reached module can be named by the alias its own catalogue row carries rather
// than by the audience string. Linear over a page of at most 500 rows, and
// reached only for a module this organisation installed.
func installedTargetID(summaries []*gen.InstallationSummary, installationID string) string {
	for _, summary := range summaries {
		if summary.GetInstallation().GetId() == installationID {
			return summary.GetInstallation().GetTargetId()
		}
	}
	return ""
}
