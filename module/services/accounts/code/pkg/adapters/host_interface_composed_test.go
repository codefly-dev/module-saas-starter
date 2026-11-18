//go:build !pure

package adapters

import "testing"

// TestAccountsPassesTheHostInterfaceConformanceSuite boots accounts over a
// fresh test store, seeds the harness's fixture and runs the shared host
// interface conformance suite against it, then the cases beyond the suite.
// It runs wherever the package's tests run with their dependencies, as every
// other store-backed test does: a switch to turn it on was a switch nothing in
// CI turned, so the suite was skipped there. It includes everything the
// fresh-store test it replaces asserted (manifest authority derived from an
// empty store, a person issued).
func TestAccountsPassesTheHostInterfaceConformanceSuite(t *testing.T) {
	runHostInterfaceFixture(t)
}
