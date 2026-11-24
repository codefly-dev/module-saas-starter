package business

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	coredecide "github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	coredelivered "github.com/codefly-dev/authority/libraries/authority-core/go/delivered"
	solutionhost "github.com/codefly-dev/authority/libraries/solutionhost/go"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"

	compositionv1 "accounts/pkg/gen/host/composition/v1"
)

// A solution's roles decide what a person holds through it (the owner's rule:
// access to a solution is its own role). The roles are the solution's own
// declaration, signed and delivered with its manifest. What a person holds
// through them, at the solution's audience and through its bindings, is the
// authority core's to decide (a role's scopes at the audience it is addressed
// to, decide.RoleScopesAt, within the reach the installation's consent
// approved): nothing here computes it. This host reads the declaration for
// the names of the roles alone, to say which roles a target offers.
// Organisation roles do not enter it: they administer the organisation. A
// module that declares a page may declare roles too, and then everything here
// holds of it as of a solution.

// SolutionAuthority is one delivered solution's role declaration: the roles
// it declares, as the core composed them. It carries no grant a role holds
// addressed to another module, and decides nothing.
type SolutionAuthority struct {
	Principal string
	Roles     map[string][]*basev0.WorkScopeV1
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

// solutionAuthorityOf is the role declaration of a solution, or of a module
// that declares a page (the core's rule: a module page may declare roles, and
// then they decide what a person holds there, as a solution's do). The roles
// are the core's composition of the delivery (coredelivered.Module): a module
// page delivered with none has the two the core derives from what it serves,
// so this host offers the teams the core gates by.
func (s *Service) solutionAuthorityOf(doc *compositionv1.AuthorityManifestV1, manifest *solutionhost.ModuleManifest) *SolutionAuthority {
	if doc == nil || manifest.Authority == nil {
		return nil
	}
	composed, err := coredelivered.Module(manifest, documentOf(doc))
	if err != nil || len(composed.Roles) == 0 || !coredecide.RoleGated(composed) || coredecide.ValidateModule(composed) != nil {
		// The core's own verdict: roles it would refuse (a module declaring
		// roles and no page) decide nothing here either.
		return nil
	}
	out := &SolutionAuthority{Principal: manifest.Authority.Principal, Roles: map[string][]*basev0.WorkScopeV1{}}
	for name, scopes := range composed.Roles {
		out.Roles[name] = slices.Clone(scopes)
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

// solutionRoleNamePrefix begins the name of a role this host derived, before
// every host decided through its authority core, for one solution's role in
// one organisation; its name is its provenance (org, solution, role). Nothing
// writes one now; a stored one is still read back: its solution's role name
// (SolutionRoleOf) and its origin (SolutionRoleOrigin).
const solutionRoleNamePrefix = "solution role · "

// SolutionRoleOrigin is the organisation and the solution target a solution
// role's provenance names, and whether the provenance is a solution role's at
// all.
func SolutionRoleOrigin(provenance string) (org, target string, ok bool) {
	rest, ok := strings.CutPrefix(provenance, "org=")
	if !ok {
		return "", "", false
	}
	org, rest, _ = strings.Cut(rest, ";solution=")
	target, _, ok = strings.Cut(rest, ";role=")
	if !ok || org == "" || target == "" {
		return "", "", false
	}
	return org, target, true
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
// target is exposed to it without a role named. A target that declares no
// roles is exposed with the installation's standing role (an empty id says so,
// and why). One that declares roles is never exposed this way: its people
// hold its roles through the authority core, and an installation recorded by
// this host before its solution's roles were read holds a catalogue role,
// which is no role of the solution. Nothing guesses one from the other's name,
// so the catalogue's admin is never the page's admin; the installation is
// reinstalled through the review instead.
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
	return "", "", NewStoreError(fmt.Errorf("%s declares its own roles (%s) and this installation's standing role %q is not one of them: the installation predates its solution's roles, so reinstall it through the review to give them",
		target.RouteAlias, strings.Join(authority.RoleNames(), ", "), standing), ErrTypeValidation)
}
