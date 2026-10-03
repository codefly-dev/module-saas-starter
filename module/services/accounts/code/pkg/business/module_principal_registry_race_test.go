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
	withCurrentAuthority(service)
	withCurrentAuthority(service)
	service.SetModulePrincipals(narrow)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				if i%2 == 0 {
					withCurrentAuthority(service)
					withCurrentAuthority(service)
					service.SetModulePrincipals(wide)
					continue
				}
				withCurrentAuthority(service)
				withCurrentAuthority(service)
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

// The atomic pointer guards the map HEADER. These two tests guard the map.
//
// Both reviews of #953 made the same point: a pointer swap synchronizes nothing
// if the backing map stays reachable from outside, because a writer mutating it
// in place swaps no pointer and so crosses no barrier. The registry was being
// stored as the CALLER'S map and handed back out by the exported accessor, so
// there were two such routes.
//
// Deliberately NOT race-dependent. The earlier test in this file can only fail
// under `-race`, which CI runs on one package — so it proves little where it
// matters. These fail on a plain `go test` by observing the authorization answer
// change, which is the thing that actually goes wrong.
func TestModulePrincipalRegistryIsNotMutableThroughTheMapItWasGiven(t *testing.T) {
	service := &Service{}
	caller := ModulePrincipalRegistry{
		ModulePrincipalID("example"): {Prefix: "example", Queues: []string{"a"}},
	}
	withCurrentAuthority(service)
	withCurrentAuthority(service)
	service.SetModulePrincipals(caller)

	// The caller keeps its map and widens the grant it already handed over, and
	// adds a principal that was never declared.
	caller[ModulePrincipalID("example")] = ModulePrincipalGrant{
		Prefix: "example", Queues: []string{"a", "b"}, CrossTenant: true,
	}
	caller[ModulePrincipalID("smuggled")] = ModulePrincipalGrant{Prefix: "smuggled"}

	granted := service.declaredModules()[ModulePrincipalID("example")]
	require.Equal(t, []string{"a"}, granted.Queues,
		"a widened queue list must not reach the registry the service authorizes against")
	require.False(t, granted.CrossTenant,
		"cross-tenant must not be grantable by mutating the map the caller passed in")
	_, smuggled := service.declaredModules()[ModulePrincipalID("smuggled")]
	require.False(t, smuggled,
		"a principal added after the store must not become declared")
}

func TestModulePrincipalRegistryIsNotMutableThroughTheAccessor(t *testing.T) {
	service := &Service{}
	withCurrentAuthority(service)
	withCurrentAuthority(service)
	service.SetModulePrincipals(ModulePrincipalRegistry{
		ModulePrincipalID("example"): {Prefix: "example", Queues: []string{"a"}},
	})

	// A caller reads the declaration and writes into what it was handed.
	read := service.ModulePrincipals()
	read[ModulePrincipalID("example")] = ModulePrincipalGrant{
		Prefix: "example", Queues: []string{"a", "b"}, CrossTenant: true,
	}
	read[ModulePrincipalID("smuggled")] = ModulePrincipalGrant{Prefix: "smuggled"}

	granted := service.declaredModules()[ModulePrincipalID("example")]
	require.Equal(t, []string{"a"}, granted.Queues,
		"the accessor must not hand out the registry the service authorizes against")
	require.False(t, granted.CrossTenant)
	_, smuggled := service.declaredModules()[ModulePrincipalID("smuggled")]
	require.False(t, smuggled)
}

// Clearing the registry denies every module caller, rather than leaving the
// previous one serving. maps.Clone of a nil map is nil, and both an unset
// pointer and a nil registry read as empty — so the fail-closed answer survives
// the clone that was added to make the registry unreachable from outside.
func TestModulePrincipalRegistryClearedDeniesEveryCaller(t *testing.T) {
	service := &Service{}
	withCurrentAuthority(service)
	withCurrentAuthority(service)
	service.SetModulePrincipals(ModulePrincipalRegistry{
		ModulePrincipalID("example"): {Prefix: "example", Queues: []string{"a"}},
	})
	withCurrentAuthority(service)
	withCurrentAuthority(service)
	service.SetModulePrincipals(nil)
	require.Empty(t, service.declaredModules(),
		"a cleared registry must authorize nobody, never keep the previous declaration")
}
