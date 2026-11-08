package fixtures

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/wool"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// fixtureInstallation is one installation a fixture declares: the organisation
// installing, the target by the name the composition gives it, the role it
// holds, and the administrator who installs it and is its owner of record. A
// role-gated target (a solution, or a module page declaring roles) is reviewed
// and installed through that review by that person, as an administrator
// installs it, and its role is granted to each team named; a roleless module
// is the host interface's Install by name. The role
// is left out for a target that holds none: a module declaring
// `installation_role: none`, or one declaring neither an installation role
// nor any role, is installed holding no standing authority, and a row naming
// no role names no teams, since a team grant carries the role.
type fixtureInstallation struct {
	Org    string   `yaml:"org"`
	Target string   `yaml:"target"`
	Role   string   `yaml:"role"`
	Owner  string   `yaml:"owner"`
	Teams  []string `yaml:"teams,omitempty"`
}

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
		if strings.TrimSpace(installation.Role) == "" && len(installation.Teams) > 0 {
			return fmt.Errorf("installation[%d] (%s): teams are granted the installation's role, so a row naming teams names a role", i, installation.describe())
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
}

type pendingInstallation struct {
	declared fixtureInstallation
	orgID    string
	ownerID  string
	// teamIDs are the declared teams not exposed yet, allTeamIDs every one.
	teamIDs, allTeamIDs []string
	// waiting is set once the first pass found the target undelivered, so the
	// wait is said once rather than at every pass.
	waiting bool
}

func newInstallations(service *business.Service, declared []fixtureInstallation, orgIDs, userIDs, teamIDs map[string]string) (*Installations, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	installations := &Installations{service: service}
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
		installations.pending = append(installations.pending, row)
	}
	return installations, nil
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
	w := wool.Get(ctx).In("fixtures.Installations")
	var failures []error
	remaining := i.pending[:0]
	for _, row := range i.pending {
		if active, err := i.coreInstall(ctx, row); active || err != nil {
			if err != nil {
				remaining = append(remaining, row)
				if !errors.Is(err, errCoreTargetNotComposed) {
					failures = append(failures, fmt.Errorf("fixture installation %s: %w", row.declared.describe(), err))
				}
			}
			continue
		}
		installation, installed, err := i.service.InstallFixtureTarget(ctx, row.ownerID, row.orgID, row.declared.Target, row.declared.Role, row.ownerID)
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

// errCoreTargetNotComposed is a declared installation whose target the
// authority core has not composed yet; the next pass installs it there.
var errCoreTargetNotComposed = errors.New("the authority core has not composed this target yet")

// coreInstall installs a role-gated target (a solution, or a module page
// declaring roles) as an administrator does: the row's owner reviews it
// (ReviewSolutionInstallAs) and installs that review (InstallReviewedSolutionAs),
// which converges the target's closure of roleless supporting modules with it,
// then the owner is granted the installer role and each declared team the
// row's role. A module holding no roles answers false: Converge installs it by
// name. An installation the core holds active is left as it is and its grants
// are granted again, idempotently; one it holds pending is resumed from the
// review it was opened under when that review is the owner's, and superseded
// by a fresh one otherwise; a revoked or absent one takes a fresh review.
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
	if !decide.RoleGated(m) {
		return false, nil
	}
	caller := authority.Person{ID: row.ownerID, Organization: row.orgID}
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
	if !held || inst.Status != domain.Active {
		review, err := i.reviewFor(ctx, caller, m, role, inst, held)
		if err != nil {
			return false, err
		}
		inst, err = i.service.InstallReviewedSolutionAs(ctx, caller, row.orgID, review, role)
		if err != nil {
			return false, fmt.Errorf("the authority core: %w", err)
		}
	}
	grants := []authority.RoleRequest{{Organization: row.orgID, InstallationID: inst.ID, Role: domain.InstallerRole, PersonID: row.ownerID}}
	for _, teamID := range row.allTeamIDs {
		grants = append(grants, authority.RoleRequest{Organization: row.orgID, InstallationID: inst.ID, Role: row.declared.Role, TeamID: teamID})
	}
	for _, grant := range grants {
		if _, err := core.GrantRoleAsHost(ctx, grant); err != nil {
			return false, fmt.Errorf("the authority core: grant %s: %w", grant.Role, err)
		}
	}
	return inst.Status == domain.Active, nil
}

// reviewFor is the review the owner installs m with: the one a pending
// attempt of theirs was opened under, which resumes it (what that attempt
// installed is installed again under the same operation, idempotently), or a
// fresh one.
func (i *Installations) reviewFor(ctx context.Context, caller authority.Person, m domain.Module, role string, inst domain.Installation, held bool) (*gen.ReviewedSolutionInstall, error) {
	if store, ok := i.service.Store().(business.CanonicalInstallationSetupStore); ok && held && inst.Status == domain.Pending {
		plan, err := store.SolutionInstallationPlan(ctx, caller.Organization, inst.ID)
		if err == nil && plan.Review != nil && plan.Review.ActorId == caller.ID && plan.RequestedRole == role && plan.RequestedOwner == "" {
			return plan.Review, nil
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
