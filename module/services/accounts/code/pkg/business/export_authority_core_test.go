package business

import "github.com/codefly-dev/authority/libraries/authority-core/go/authority"

// UnconfigureAuthorityCoreForTest returns the service to a host whose
// authority core is not built, so a test that configured the shared service's
// core for itself leaves it as the package's TestMain built it. Compiled into
// tests only.
func (s *Service) UnconfigureAuthorityCoreForTest() {
	state := s.authorityCoreState()
	state.mu.Lock()
	state.core, state.err = nil, ErrAuthorityCoreUnconfigured
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
