package business

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// The module principal registry is REPLACED, never mutated: every writer swaps a
// whole registry in. As a bare field that swap was an unsynchronized write
// against the eighteen authorization sites that read it, which is undefined
// behaviour rather than a merely stale read.
//
// Nothing replaces it at runtime today — work.go wires it once at boot — so the
// race is latent. It goes live the moment a platform administrator can narrow a
// module's ceiling without a redeploy, which is exactly what the envelope record
// introduces. This test is what keeps the field behind an atomic pointer until
// then: under `-race` it fails against the bare field and passes against the
// pointer.
func TestModulePrincipalRegistryIsReplacedWithoutRacingItsReaders(t *testing.T) {
	service := &Service{}
	narrow := ModulePrincipalRegistry{
		ModulePrincipalID("example"): {Prefix: "example", Queues: []string{"a"}},
	}
	wide := ModulePrincipalRegistry{
		ModulePrincipalID("example"): {Prefix: "example", Queues: []string{"a", "b"}},
		ModulePrincipalID("other"):   {Prefix: "other"},
	}
	service.SetModulePrincipals(narrow)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				if i%2 == 0 {
					service.SetModulePrincipals(wide)
					continue
				}
				service.SetModulePrincipals(narrow)
			}
		}()
	}
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				// The two shapes of read every authorization site uses: a
				// lookup by principal, and a whole-registry derivation.
				_, _ = service.declaredModules()[ModulePrincipalID("example")]
				_ = service.declaredModules().ContentResources()
			}
		}()
	}
	wg.Wait()
}

// An unset registry denies every module caller rather than admitting all of
// them. It read that way as a nil map and must keep reading that way behind the
// pointer: this is the one place where losing the fail-closed default would be
// invisible, because a service with no declared modules is exactly what a
// misconfigured deployment has.
func TestUndeclaredModuleRegistryDeniesEveryCaller(t *testing.T) {
	service := &Service{}
	require.Empty(t, service.declaredModules(),
		"an unset registry must read as empty, which authorizes no module")
	_, registered := service.declaredModules()[ModulePrincipalID("example")]
	require.False(t, registered)
	require.Empty(t, service.declaredModules().ContentResources(),
		"no declared content resources means nothing may be placed")
	require.Empty(t, service.ModulePrincipals(),
		"the exported accessor answers the same empty registry")
}
