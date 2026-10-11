package business

import "context"

// currentModuleAuthority is the live read the EXISTING capability tests get.
//
// They exist to exercise delegation, oracles and exchange behaviour, and
// enforcement at use made every one of them fail closed — which is the
// enforcement working, not a test defect: a Service with a declared registry and
// no live read now refuses every capability rather than deciding on the declared
// ceiling alone.
//
// So they are given a read that reports a CURRENT installation. That keeps them
// testing what they are about while still going through
// AuthorizeModuleCapability, rather than bypassing it — the alternative,
// allowing an unwired Service to serve, would have made the whole check
// optional in exactly the configuration tests run in.
//
// It reports revision and epoch 0 deliberately: a credential sealed at any
// non-zero value is refused by it, so a test that wants to prove a stale seal is
// refused does not have to opt out of this.
type currentModuleAuthority struct{}

func (currentModuleAuthority) LiveModuleAuthority(
	_ context.Context, _, _, _ string,
) (*LiveModuleAuthority, error) {
	return &LiveModuleAuthority{InstallationID: "installation-current"}, nil
}

// withCurrentAuthority wires it. Named so a reader of a failing test can see at
// once that the live read is a fixture rather than the thing under test.
func withCurrentAuthority(service *Service) *Service {
	service.SetModuleAuthorityReads(currentModuleAuthority{}, nil)
	return service
}
