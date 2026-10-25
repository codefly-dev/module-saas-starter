package business

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
)

// A solution's roles decide what a person holds through it (the owner's rule:
// access to a solution is its own role). The roles are the solution's own
// declaration, signed and delivered with its manifest; a person holds one at
// the organisation's installation of the solution: the installer role when
// they own the installation, and the installation's standing role through
// every team it is exposed to that they belong to. What they hold at the
// solution's audience, and through its bindings, is those roles' grants,
// bounded by what the solution may grant: its delivered scope ceilings on its
// own kinds and its bindings' invoke and lookup scopes on the kinds it
// consumes. Organisation roles do not enter it: they administer the
// organisation.

// InstallerRole is the role every solution declares; an installation's owner holds it.
const InstallerRole = "installer"

// SolutionAuthority is one delivered solution's role declaration.
type SolutionAuthority struct {
	Principal string
	Roles     map[string][]*basev0.WorkScopeV1
	// Kinds are the solution's own resource kinds; Ceilings the delivered
	// ceiling on each, where one is declared.
	Kinds    []string
	Ceilings map[string][]string
	// Consumed is what the solution's bindings may carry for a person.
	Consumed []*basev0.WorkScopeV1
}

// DeliveredSolutionAuthority is principal's role declaration from its applied
// signed delivery, or nil when principal is not a delivered solution that
// declares roles.
func (s *Service) DeliveredSolutionAuthority(ctx context.Context, principal string) (*SolutionAuthority, error) {
	doc, manifest, err := s.appliedAuthorityDocument(ctx, principal)
	if err != nil {
		return nil, err
	}
	return s.solutionAuthorityOf(doc, manifest), nil
}

// deliveredTargetAuthority is DeliveredSolutionAuthority for an installable
// target; a nil declaration comes with why the target declares none.
func (s *Service) deliveredTargetAuthority(ctx context.Context, target *AvailableSolutionTarget) (*SolutionAuthority, string, error) {
	doc, manifest, err := s.appliedAuthorityDocumentOf(ctx, target.RouteAlias, func(binding string, _ *solutionhost.ModuleManifest) bool {
		return binding == target.BindingID
	})
	if errors.Is(err, errNoAuthorityDocument) {
		return nil, err.Error(), nil
	}
	if err != nil {
		return nil, "", err
	}
	if authority := s.solutionAuthorityOf(doc, manifest); authority != nil {
		return authority, "", nil
	}
	return nil, fmt.Sprintf("%s's applied delivery declares no solution roles", target.RouteAlias), nil
}

func (s *Service) solutionAuthorityOf(doc *compositionv1.AuthorityManifestV1, manifest *solutionhost.ModuleManifest) *SolutionAuthority {
	if doc == nil || manifest.Kind != solutionhost.KindSolution || len(doc.GetRoles()) == 0 {
		return nil
	}
	principal := manifest.Authority.Principal
	out := &SolutionAuthority{Principal: principal, Roles: map[string][]*basev0.WorkScopeV1{}, Kinds: slices.Clone(manifest.Authority.ResourceKinds), Ceilings: map[string][]string{}}
	for _, role := range doc.GetRoles() {
		out.Roles[role.GetName()] = hostScopes(role.GetScopes())
	}
	for _, ceiling := range doc.GetScopeCeilings() {
		out.Ceilings[ceiling.GetResourceKind()] = append(out.Ceilings[ceiling.GetResourceKind()], ceiling.GetActions()...)
	}
	for _, binding := range s.ModulePrincipals()[ModulePrincipalID(principal)].OperationAudiences {
		for _, scope := range append(slices.Clone(binding.InvokeScopes), binding.LookupScopes...) {
			out.Consumed = append(out.Consumed, &basev0.WorkScopeV1{ResourceKind: scope.ResourceKind, Actions: slices.Clone(scope.Actions), ResourceIds: slices.Clone(scope.ResourceIDs)})
		}
	}
	return out
}

// RoleNames are the roles the solution declares, sorted.
func (a *SolutionAuthority) RoleNames() []string {
	names := make([]string, 0, len(a.Roles))
	for name := range a.Roles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Grants is what role grants, bounded by what the solution may grant.
func (a *SolutionAuthority) Grants(role string) []*basev0.WorkScopeV1 {
	var out []*basev0.WorkScopeV1
	for _, scope := range a.Roles[role] {
		if !slices.Contains(a.Kinds, scope.GetResourceKind()) {
			out = append(out, IntersectWorkScopes([]*basev0.WorkScopeV1{scope}, a.Consumed)...)
			continue
		}
		ceiling, bounded := a.Ceilings[scope.GetResourceKind()]
		if !bounded {
			out = append(out, cloneWorkScope(scope))
			continue
		}
		out = append(out, IntersectWorkScopes([]*basev0.WorkScopeV1{scope}, []*basev0.WorkScopeV1{{ResourceKind: scope.GetResourceKind(), Actions: ceiling}})...)
	}
	return out
}

// Own is scopes cut to the solution's own kinds: what is held at its audience.
func (a *SolutionAuthority) Own(scopes []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	var out []*basev0.WorkScopeV1
	for _, scope := range scopes {
		if slices.Contains(a.Kinds, scope.GetResourceKind()) {
			out = append(out, cloneWorkScope(scope))
		}
	}
	return out
}

// SolutionRoleHolders is who holds a role at one installation.
type SolutionRoleHolders struct {
	Owner string
	// Role is the installation's standing role, held through Teams.
	Role  string
	Teams []string
}

// HeldBy is what person, a member of teams, holds through the solution's
// roles at one installation.
func (a *SolutionAuthority) HeldBy(person string, teams []string, at SolutionRoleHolders) []*basev0.WorkScopeV1 {
	var held []*basev0.WorkScopeV1
	if at.Owner == person {
		held = append(held, a.Grants(InstallerRole)...)
	}
	for _, team := range at.Teams {
		if slices.Contains(teams, team) {
			held = append(held, a.Grants(at.Role)...)
			break
		}
	}
	return held
}

// PersonSolutionRoles is what personID holds through principal's roles in
// orgID, with the solution's declaration; a nil declaration says principal is
// not a solution whose roles decide it, so the organisation's roles answer. A
// solution not installed in the organisation, or not active there, grants
// nothing.
func (s *Service) PersonSolutionRoles(ctx context.Context, orgID, personID, principal string) (*SolutionAuthority, []*basev0.WorkScopeV1, error) {
	authority, err := s.DeliveredSolutionAuthority(ctx, principal)
	if err != nil || authority == nil {
		return nil, nil, err
	}
	installationID, err := s.WorkContextAudienceInstallation(ctx, orgID, principal)
	if errors.Is(err, ErrWorkContextNoAudienceInstallation) {
		return authority, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	details, err := s.ReadHostInstallation(ctx, orgID, installationID)
	if err != nil {
		return nil, nil, err
	}
	if details.Installation.GetStatus() != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
		return authority, nil, nil
	}
	teams, err := s.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: orgID, MemberId: personID})
	if err != nil {
		return nil, nil, err
	}
	var mine []string
	for _, team := range teams.GetTeams() {
		mine = append(mine, team.GetId())
	}
	return authority, authority.HeldBy(personID, mine, SolutionRoleHolders{
		Owner: details.Installation.GetOwnerPrincipalId(), Role: details.Role, Teams: details.TeamIDs,
	}), nil
}

// An installation of a solution holds one of the solution's roles as its
// standing role: a role this host derives and owns per organisation, solution
// and role name, whose permissions are the role's whole-kind grants (a role
// row has no resource column, so a grant limited to named resources is not
// carried). Like every derived role it is outside the catalogue and is never
// assigned organisation-wide or shared; it is granted only at a solution's
// authority-root node in its own organisation (GrantScope). Its name is its provenance, so it never needs rewriting, and the
// solution's role name is read back from it.
const solutionRoleNamePrefix = "solution role · "

func solutionRoleProvenance(orgID, targetID, role string) string {
	return fmt.Sprintf("org=%s;solution=%s;role=%s", orgID, targetID, role)
}

// SolutionRoleOrganization is the organisation a solution role's provenance
// names, and whether the provenance is a solution role's at all.
func SolutionRoleOrganization(provenance string) (string, bool) {
	rest, ok := strings.CutPrefix(provenance, "org=")
	if !ok || !strings.Contains(rest, ";solution=") || !strings.Contains(rest, ";role=") {
		return "", false
	}
	org, _, _ := strings.Cut(rest, ";")
	return org, true
}

// SolutionRoleOf is the solution's role name a standing role carries, or the
// role's own name when it is not a solution role.
func SolutionRoleOf(name string) string {
	provenance, ok := strings.CutPrefix(name, solutionRoleNamePrefix)
	if !ok {
		return name
	}
	_, role, _ := strings.Cut(provenance, ";role=")
	return role
}

// solutionExposureRole is the role a team is granted when an installation of
// target is exposed to it without a role named: the solution's own role of the
// installation's standing role's name, which permits using the solution. An
// installation made before solution roles were read, whose standing role is a
// catalogue role of that name, is exposed this way too, its standing grant
// untouched. An empty id says the standing role itself is granted, and why.
func (s *Service) solutionExposureRole(ctx context.Context, orgID, installationID string, target *AvailableSolutionTarget) (string, string, error) {
	if target == nil {
		return "", "the installation's target is no longer offered to this organization", nil
	}
	authority, why, err := s.deliveredTargetAuthority(ctx, target)
	if err != nil || authority == nil {
		return "", why, err
	}
	reader, ok := s.store.(hostInstallationReader)
	if !ok {
		return "", "", fmt.Errorf("installation role reader is not wired")
	}
	var standing string
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var e error
		_, standing, _, e = reader.InstallationRoleRevision(ctx, orgID, installationID)
		return e
	}); err != nil {
		return "", "", err
	}
	role := SolutionRoleOf(standing)
	if _, declared := authority.Roles[role]; !declared {
		return "", fmt.Sprintf("%s declares no role named after the standing role %q; its roles are %s", target.RouteAlias, role, strings.Join(authority.RoleNames(), ", ")), nil
	}
	id, err := s.solutionRoleID(ctx, orgID, target.TargetID, target.RouteAlias, authority, role)
	return id, "", err
}

// solutionRoleID is the id of the role an installation of targetID holds, and
// its exposed teams hold, for the solution's role named role: written if
// absent, its permissions reconciled to the declaration if present.
func (s *Service) solutionRoleID(ctx context.Context, orgID, targetID, alias string, authority *SolutionAuthority, role string) (string, error) {
	if _, declared := authority.Roles[role]; !declared {
		return "", NewStoreError(fmt.Errorf("%s declares no role %q; its roles are %s", alias, role, strings.Join(authority.RoleNames(), ", ")), ErrTypeNotFound)
	}
	// Holding any of a solution's roles is using the solution: the permission
	// exposure and the solution's entitlement listing read.
	permissions := []*gen.Permission{{Resource: ResourceTypeSolution, Action: ActionUseSolution}}
	for _, scope := range authority.Grants(role) {
		if len(scope.GetResourceIds()) > 0 {
			continue
		}
		for _, action := range scope.GetActions() {
			permissions = append(permissions, &gen.Permission{Resource: scope.GetResourceKind(), Action: action})
		}
	}
	provenance := solutionRoleProvenance(orgID, targetID, role)
	var id string
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var e error
		id, e = s.installationStore().EnsureDerivedRole(ctx, DerivedRole{
			OrgID: orgID, DerivedFrom: provenance, Name: solutionRoleNamePrefix + provenance,
			Description: fmt.Sprintf("the %s role of %s", role, alias), Permissions: permissions,
		})
		return e
	})
	return id, err
}

// IntersectWorkScopes is what both scope sets allow, kind by kind: the actions
// in both and the resources in both, where naming none means any.
func IntersectWorkScopes(a, b []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	var out []*basev0.WorkScopeV1
	for _, x := range a {
		for _, y := range b {
			if x.GetResourceKind() != y.GetResourceKind() {
				continue
			}
			var actions []string
			for _, action := range x.GetActions() {
				if slices.Contains(y.GetActions(), action) && !slices.Contains(actions, action) {
					actions = append(actions, action)
				}
			}
			ids, ok := intersectIDs(x.GetResourceIds(), y.GetResourceIds())
			if len(actions) > 0 && ok {
				out = append(out, &basev0.WorkScopeV1{ResourceKind: x.GetResourceKind(), Actions: actions, ResourceIds: ids})
			}
		}
	}
	return out
}

func intersectIDs(a, b []string) ([]string, bool) {
	switch {
	case len(a) == 0:
		return slices.Clone(b), true
	case len(b) == 0:
		return slices.Clone(a), true
	}
	var both []string
	for _, id := range a {
		if slices.Contains(b, id) {
			both = append(both, id)
		}
	}
	return both, len(both) > 0
}

func hostScopes(in []*authorityv1.WorkScopeV1) []*basev0.WorkScopeV1 {
	out := make([]*basev0.WorkScopeV1, 0, len(in))
	for _, scope := range in {
		out = append(out, &basev0.WorkScopeV1{ResourceKind: scope.GetResourceKind(), Actions: slices.Clone(scope.GetActions()), ResourceIds: slices.Clone(scope.GetResourceIds())})
	}
	return out
}

func cloneWorkScope(scope *basev0.WorkScopeV1) *basev0.WorkScopeV1 {
	return &basev0.WorkScopeV1{ResourceKind: scope.GetResourceKind(), Actions: slices.Clone(scope.GetActions()), ResourceIds: slices.Clone(scope.GetResourceIds())}
}
