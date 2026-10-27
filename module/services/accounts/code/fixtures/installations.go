package fixtures

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/wool"

	"accounts/pkg/business"
)

// fixtureInstallation is one installation a fixture declares: the organisation
// installing, the target by the name the composition gives it, the role it
// holds, and the administrator who installs it and is its owner of record. It
// is the host interface's Install by name, performed by a fixture person, then
// granted to each team named: the teams whose members hold its role.
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
// declared in: every field named, and an owner the fixture makes an
// administrator of the organisation, as an install requires. Refused here, a
// typo is read at boot instead of as an install refused at every pass.
func validateInstallations(f *fixtureFile) error {
	for i, installation := range f.Installations {
		if strings.TrimSpace(installation.Org) == "" || strings.TrimSpace(installation.Target) == "" ||
			strings.TrimSpace(installation.Role) == "" || strings.TrimSpace(installation.Owner) == "" {
			return fmt.Errorf("installation[%d]: org, target, role and owner are required", i)
		}
		if !fixtureAdministers(f, installation.Org, installation.Owner) {
			return fmt.Errorf("installation[%d] (%s): %s is not an owner or admin of %q in this fixture, and only an administrator installs",
				i, installation.describe(), installation.Owner, installation.Org)
		}
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
// through the host's own install path (business.Service.InstallTargetByName)
// as each installation's owner. A target is installable only once the
// composition has delivered it to this host, which happens after the seed, so
// they are converged after every delivery pass rather than at seed time.
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
			// The authority core holds the same installation, which the host
			// interfaces decide with (coreInstall).
			if err := i.coreInstall(ctx, row); err != nil {
				failures = append(failures, fmt.Errorf("fixture installation %s: %w", row.declared.describe(), err))
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

// coreInstall performs the declared installation in the authority core as the
// host's own setup (InstallAsHost), then grants the installer role to its
// owner and its role to each declared team (GrantRoleAsHost), as the local
// authority's fixture does. Each step is idempotent, so a reseed repeats it
// harmlessly. A host whose core is not configured installs nothing there.
func (i *Installations) coreInstall(ctx context.Context, row pendingInstallation) error {
	core, err := i.service.AuthorityCore()
	if errors.Is(err, business.ErrAuthorityCoreUnconfigured) {
		return nil
	}
	if err != nil {
		return err
	}
	inst, err := core.InstallAsHost(ctx, row.orgID, row.declared.Target, row.declared.Role, row.ownerID, i.service.AuthorityInstallStep())
	switch {
	case errors.Is(err, domain.ErrAlreadyInstalled):
		// Installed already: its grants are granted again, idempotently.
		inst.ID = authority.StableInstallationID(row.orgID, row.declared.Target)
	case err != nil:
		return fmt.Errorf("the authority core: %w", err)
	}
	grants := []authority.RoleRequest{{Organization: row.orgID, InstallationID: inst.ID, Role: domain.InstallerRole, PersonID: row.ownerID}}
	for _, teamID := range row.allTeamIDs {
		grants = append(grants, authority.RoleRequest{Organization: row.orgID, InstallationID: inst.ID, Role: row.declared.Role, TeamID: teamID})
	}
	for _, grant := range grants {
		if _, err := core.GrantRoleAsHost(ctx, grant); err != nil {
			return fmt.Errorf("the authority core: grant %s: %w", grant.Role, err)
		}
	}
	return nil
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
		err := i.coreInstall(ctx, row)
		if code, ok := domain.CodeOf(err); ok && code == domain.NotFound {
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("fixture installation %s: %w", row.declared.describe(), err))
		}
	}
	return errors.Join(failures...)
}
