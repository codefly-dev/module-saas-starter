package business

import (
	"context"
	"fmt"
)

// installationActivator activates accounts' record of an installation the
// authority core holds active.
type installationActivator interface {
	ActivateInstallationFollowingCore(ctx context.Context, orgID, installationID string) (bool, error)
}

// FollowCoreInstallation makes accounts' record of a solution installation
// follow the authority core's, which the host interfaces decide with: once
// the core's is active (consented and composed), accounts' record of the same
// installation, pending its install step or failed at it, is active too, with
// the root scope node it was installed with, so its teams can be exposed.
// One source of truth: accounts' record never activates on its own here; it
// is called with the core's installation active. An installation already
// active, or revoked, is left as it is. The activation is the core's, which
// its journal records (saas.authority.installation_activated); this records
// no second event for it.
func (s *Service) FollowCoreInstallation(ctx context.Context, orgID, installationID string) error {
	store, ok := s.store.(installationActivator)
	if !ok {
		return fmt.Errorf("the installation store cannot follow the authority core")
	}
	return s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		_, err := store.ActivateInstallationFollowingCore(ctx, orgID, installationID)
		return err
	})
}
