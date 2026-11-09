package business

import (
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestCompositionAdoptsOnlyAnActiveInstallationWithAConsentOfItsOwnUnderAnEnvelope(t *testing.T) {
	active := gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE
	pinned := InstallationScope{Capabilities: true, Resources: []string{"documents"}}
	following := InstallationScope{Capabilities: true, Follows: true}
	for _, c := range []struct {
		name     string
		envelope uint64
		status   gen.InstallationStatus
		scope    InstallationScope
		adopts   bool
	}{
		{"a pinned consent under an envelope is adopted", 12, active, pinned, true},
		{"a following installation is left alone", 12, active, following, false},
		{"with no envelope a consent is the organisation's own", 0, active, pinned, false},
		{"an installation that is not active is not installed again", 12, gen.InstallationStatus_INSTALLATION_STATUS_PENDING, pinned, false},
	} {
		if got := adoptsByComposition(c.envelope, c.status, c.scope); got != c.adopts {
			t.Errorf("%s: adoptsByComposition = %v", c.name, got)
		}
	}
}
