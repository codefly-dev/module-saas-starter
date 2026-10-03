package business_test

import (
	"context"

	"accounts/pkg/business"
)

// currentModuleAuthority is a live-authority read reporting a CURRENT
// installation, wired through the real seam like this package's other fakes.
//
// Enforcement at use made every capability test in this package fail closed:
// a Service that declares module principals but cannot re-read live authority
// now refuses every capability rather than deciding on the declared ceiling
// alone. That is the enforcement working, so the tests wire the read rather
// than the Service growing a permissive default — a default would make the
// check optional in exactly the configuration tests run in, which is the one
// configuration where it must not be.
//
// Revision and epoch are 0 on purpose: a credential sealed at any non-zero
// value is still refused through this, so a test proving a stale seal is
// rejected needs no opt-out.
type currentModuleAuthority struct{}

func (currentModuleAuthority) LiveModuleAuthority(
	_ context.Context, _, _ string,
) (*business.LiveModuleAuthority, error) {
	return &business.LiveModuleAuthority{InstallationID: "installation-current"}, nil
}
