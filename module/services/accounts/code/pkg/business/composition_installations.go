package business

import (
	"context"
	"errors"
	"fmt"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Every composed module is installed in every organization by composition,
// never chosen by an administrator. The authority core runs with
// HostInstallsModules, so it leaves that installation to this host, which
// does what the core's composition does: on every delivery pass and at start
// (KeepAuthorityCoreCurrent) for every organization, and for a new
// organization when it is created. Each installation is the host's own module
// record, made through the module install path by installByComposition,
// recorded as the system's, holding no standing authority: a module whose
// declaration selects none, or selects nothing at all, a module page included
// (its roles decide who may use it, through grants a set up makes). Its
// consent scope is the envelope's, as every module install's is. What is
// installed already, pending or active, is never installed again; a person
// never uninstalls one (UninstallSolution refuses), and a withdrawn module's
// installations are revoked by the withdrawal.

// compositionPageLimit bounds the organizations one pass reads per page.
const compositionPageLimit = 200

// InstallComposedModules installs, by composition, every composed module not
// yet installed at every organization. It is idempotent; each organization's
// failures are collected and the others are installed all the same.
func (s *Service) InstallComposedModules(ctx context.Context) error {
	if _, err := s.AuthorityCore(); err != nil {
		return nil // A host whose core is not built composes nothing yet.
	}
	// One pass at a time in this process: a delivery's own pass and the
	// ticking one never install the same module twice at once.
	s.composing.Lock()
	defer s.composing.Unlock()
	var failures []error
	for offset := 0; ; offset += compositionPageLimit {
		var page []*organizationOwner
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			orgs, err := s.store.ListAllOrganizations(ctx, "", false, compositionPageLimit, offset)
			for _, o := range orgs {
				page = append(page, &organizationOwner{id: o.GetOrganization().GetId(), owner: o.GetOrganization().GetOwnerId()})
			}
			return err
		}); err != nil {
			return fmt.Errorf("install the composed modules: list the organizations: %w", err)
		}
		for _, o := range page {
			if err := s.installComposedModulesAt(ctx, o.id, o.owner); err != nil {
				failures = append(failures, err)
			}
		}
		if len(page) < compositionPageLimit {
			break
		}
	}
	return errors.Join(failures...)
}

type organizationOwner struct{ id, owner string }

// installComposedModulesAt installs, by composition, every module org may
// install and holds none of yet (installByComposition). The caller holds
// s.composing.
func (s *Service) installComposedModulesAt(ctx context.Context, org, owner string) error {
	core, err := s.AuthorityCore()
	if err != nil {
		return nil
	}
	var failures []error
	for token := ""; ; {
		available, next, err := s.ListAvailableSolutions(ctx, org, token, listInstallationsMaxPageSize)
		if err != nil {
			return fmt.Errorf("install the composed modules at %s: %w", org, err)
		}
		for _, target := range available {
			if target.Kind != "module" || target.Installed {
				continue
			}
			m, err := core.Composed(ctx, target.RouteAlias)
			if code, ok := domain.CodeOf(err); ok && code == domain.NotFound {
				continue // Not composed yet: the pass that composes it installs it.
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("install %s at %s: %w", target.RouteAlias, org, err))
				continue
			}
			if err := s.installByComposition(ctx, org, owner, target, m); err != nil {
				failures = append(failures, fmt.Errorf("install %s at %s: %w", target.RouteAlias, org, err))
			}
		}
		if token = next; token == "" {
			break
		}
	}
	return errors.Join(failures...)
}

// installByComposition installs module m at org as the host's composition
// does, through the module install path, recorded as the system's ("by":
// "composition"): no person approves it. It holds no standing authority (a
// declaration selecting none or nothing; a module page selects none). Its
// owner of record, which every installation names, is the organization's
// owner while they are a current administrator, or else its first current
// administrator; an organization with none gets nothing installed, and says
// so. A declaration selecting anything else is one the module install path
// refuses (modulePageHoldsNothing, undeclaredInstallationRole), so it is
// reported, never installed.
func (s *Service) installByComposition(ctx context.Context, org, owner string, target *AvailableSolutionTarget, m domain.Module) error {
	if role := supportingInstallationRole(m); role != domain.NoInstallationRole {
		return fmt.Errorf("its declaration selects the standing role %q, which a module never holds by composition: a module page declares installation_role none, and a module without a page declares no role", role)
	}
	ownerOfRecord, err := s.compositionOwnerOfRecord(ctx, org, owner)
	if err != nil {
		return err
	}
	approval, err := s.freshApproval(ctx, org, target)
	if err != nil {
		return err
	}
	_, _, err = s.installTargetByName(ctx, "", org, target.RouteAlias, "", ownerOfRecord, approval, true)
	return err
}

// compositionOwnerOfRecord is the organization's owner while they are a
// current administrator, or else its first current administrator by id.
func (s *Service) compositionOwnerOfRecord(ctx context.Context, org, owner string) (string, error) {
	var members []*gen.OrgMembership
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		members, err = s.store.ListOrgMembers(ctx, org)
		return err
	}); err != nil {
		return "", err
	}
	admin := func(m *gen.OrgMembership) bool {
		return m.GetRole() == gen.OrgRole_ORG_ROLE_OWNER || m.GetRole() == gen.OrgRole_ORG_ROLE_ADMIN
	}
	var first string
	for _, m := range members {
		if !admin(m) {
			continue
		}
		if m.GetUserId() == owner {
			return owner, nil
		}
		if first == "" || m.GetUserId() < first {
			first = m.GetUserId()
		}
	}
	if first == "" {
		return "", fmt.Errorf("organization %s has no current administrator to name as its modules' owner of record", org)
	}
	return first, nil
}

// ActiveOrPendingModuleInstallation is org's installation of the composed
// module named module that is not revoked, or nil when it holds none.
func (s *Service) ActiveOrPendingModuleInstallation(ctx context.Context, org, module string) (*gen.Installation, error) {
	available, err := s.availableTargetNamed(ctx, org, module)
	if err != nil {
		return nil, err
	}
	if available.Kind != "module" || !available.Installed {
		return nil, nil
	}
	return s.heldTargetInstallation(ctx, org, available.TargetID)
}
