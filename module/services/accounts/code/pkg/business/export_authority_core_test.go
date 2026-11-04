package business

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
