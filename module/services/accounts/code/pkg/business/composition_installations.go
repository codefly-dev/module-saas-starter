package business

import (
	"context"
	"errors"
	"fmt"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Every composed module is installed in every organization by composition,
// never chosen by an administrator. The authority core runs with
// HostInstallsModules, so it leaves that installation to this host, which
// does what the core's composition does: on every delivery pass and at start
// (KeepAuthorityCoreCurrent) for every organization, and for a new
// organization when it is created. Each installation is the host's own module
// record, made through the module install path (InstallFixtureTarget, the
// host-owned consent), holding no standing authority: a module whose
// declaration selects none, or selects nothing at all, a module page included
// (its roles decide who may use it, through grants a set up makes). Its
// consent scope is the envelope's, as every module install's is. A module
// whose declaration selects a standing role is not installed this way: that
// role is an administrator's to consent to, and the pass says so. What is
// installed already, pending or active, is never installed again; a
// withdrawn module's installations are revoked by the withdrawal.

// compositionPageLimit bounds the organizations one pass reads per page.
const compositionPageLimit = 200

// InstallComposedModules installs, by composition, every composed module not
// yet installed at every organization. It is idempotent; each organization's
// failures are collected and the others are installed all the same.
func (s *Service) InstallComposedModules(ctx context.Context) error {
	if _, err := s.AuthorityCore(); err != nil {
		return nil // A host whose core is not built composes nothing yet.
	}
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
// install and holds none of yet, as the organization's owner of record.
func (s *Service) installComposedModulesAt(ctx context.Context, org, owner string) error {
	core, err := s.AuthorityCore()
	if err != nil {
		return nil
	}
	if owner == "" {
		return fmt.Errorf("organization %s has no owner to record its composed modules under", org)
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
			if supportingInstallationRole(m) != domain.NoInstallationRole {
				wool.Get(ctx).In("installComposedModulesAt").Warn("a module selecting a standing role is not installed by composition; an administrator consents to that role",
					wool.Field("module", m.Name), wool.Field("organization", org), wool.Field("role", m.InstallationRole))
				continue
			}
			if _, _, err := s.InstallFixtureTarget(ctx, owner, org, target.RouteAlias, "", owner); err != nil {
				failures = append(failures, fmt.Errorf("install %s at %s: %w", target.RouteAlias, org, err))
			}
		}
		if token = next; token == "" {
			break
		}
	}
	return errors.Join(failures...)
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
