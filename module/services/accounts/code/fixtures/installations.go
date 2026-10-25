package fixtures

import (
	"accounts/pkg/business"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/codefly-dev/core/wool"
)

// fixtureInstallation is one installation a fixture declares: the organisation
// installing, the target by the name the composition gives it, the role it
// holds, and the administrator who installs it and is its owner of record. It
// is the host interface's Install by name, performed by a fixture person.
type fixtureInstallation struct {
	Org    string `yaml:"org"`
	Target string `yaml:"target"`
	Role   string `yaml:"role"`
	Owner  string `yaml:"owner"`
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
	// waiting is set once the first pass found the target undelivered, so the
	// wait is said once rather than at every pass.
	waiting bool
}

func newInstallations(service *business.Service, declared []fixtureInstallation, orgIDs, userIDs map[string]string) (*Installations, error) {
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
		installations.pending = append(installations.pending, pendingInstallation{declared: installation, orgID: orgID, ownerID: ownerID})
	}
	return installations, nil
}

// Converge installs every declared installation whose target this host now
// offers and that its organisation does not hold yet. A target not delivered
// yet stays pending for the next pass; any other refusal is returned, naming
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
		installation, installed, err := i.service.InstallTargetByName(ctx, row.ownerID, row.orgID, row.declared.Target, row.declared.Role, row.ownerID)
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
		case installed:
			w.Info("fixture installation installed", wool.Field("installation", row.declared.describe()), wool.Field("installation_id", installation.GetId()))
		default:
			w.Info("fixture installation already held, reusing", wool.Field("installation", row.declared.describe()), wool.Field("installation_id", installation.GetId()))
		}
	}
	i.pending = remaining
	return errors.Join(failures...)
}
