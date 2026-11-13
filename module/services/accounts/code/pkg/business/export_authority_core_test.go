package business

import (
	"context"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/core/solutionhost"
)

// UnconfigureAuthorityCoreForTest returns the service to a host whose
// authority core is not built, so a test that configured the shared service's
// core for itself leaves it as the package's TestMain built it. Compiled into
// tests only.
func (s *Service) UnconfigureAuthorityCoreForTest() {
	state := s.authorityCoreState()
	state.mu.Lock()
	state.core, state.err, state.bootPending = nil, ErrAuthorityCoreUnconfigured, false
	state.mu.Unlock()
}

// SetAuthorityCoreForTest builds this service's authority core over a store of
// the test's own, for a service whose store keeps no core records. Compiled
// into tests only.
func (s *Service) SetAuthorityCoreForTest(core *authority.Service) {
	state := s.authorityCoreState()
	state.mu.Lock()
	state.core, state.err = core, nil
	state.mu.Unlock()
}

// ApplyBeforeTheCoreForTest applies one delivered carrier as a pass did
// before this host decided through the authority core, and as a pass never
// does now (composeDelivered): verified with the pass's pinned keys, recorded
// as desired, applied in accounts alone, and the module registry derived from
// it. It builds the state such a host left behind, which its core then meets
// at boot and at every later delivery. Compiled into tests only.
func (r *SolutionHostBindingReconciler) ApplyBeforeTheCoreForTest(ctx context.Context, carrier []byte) error {
	signed, err := solutionhost.ParseSigned(carrier)
	if err != nil {
		return err
	}
	delivered, err := solutionhost.VerifyDelivered(ctx, signed, r.verifier)
	if err != nil {
		return err
	}
	manifest, err := delivered.Manifest()
	if err != nil {
		return err
	}
	if err := r.service.recordSolutionHostBindingDesired(ctx, manifest, "", "", false, r.now()); err != nil {
		return err
	}
	if err := r.service.applySolutionHostBinding(ctx, delivered, r.coordinate, r.now()); err != nil {
		return err
	}
	return r.modulePrincipals.RunOnce(ctx)
}
