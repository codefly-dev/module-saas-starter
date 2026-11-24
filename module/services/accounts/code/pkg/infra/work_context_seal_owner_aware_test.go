//go:build !pure

package infra_test

import (
	"strings"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/stretchr/testify/require"
)

// The authority core seals a person's capability to the installation's
// revision, their grant epoch on it, and the owner-aware revision of the
// installed module's audience (authority.sources.sealIn): a module that
// checked owners and is delivered no longer checking them advances the last,
// which ends every capability signed while its scopes' owners were still
// checked. This host's seal read summed the first two alone, so a capability
// it minted outlived that withdrawal until its expiry where the core's own
// was refused at its next use. The seal now moves with the core's revision of
// the module the installation names, and with no other module's.
func TestTheSealMovesWhenItsModuleStopsCheckingOwners(t *testing.T) {
	i := newDynamicInstallKind(t, "", "", "", "module")
	module := strings.TrimPrefix(i.binding, "acme.test.")
	other := newDynamicInstallKind(t, "", "", "", "module")
	core := testStore.AuthorityCore()
	compose := func(name string) {
		t.Helper()
		require.NoError(t, core.Write(testCtx, func(tx authority.Tx) error {
			return tx.PutModule(domain.Module{Name: name, Principal: name, Kind: domain.KindModule, Generation: 1, OwnerAware: true})
		}))
	}
	compose(module)
	compose(strings.TrimPrefix(other.binding, "acme.test."))
	held := func(d dynamicInstall) uint64 {
		t.Helper()
		revision, err := testStore.WorkContextHeldInstallation(testCtx, d.org, d.owner, d.installation.Id)
		require.NoError(t, err)
		return revision
	}
	before, beside := held(i), held(other)

	// What the core's delivery does when a module that checked owners is
	// delivered checking none (lifecycle: BumpOwnerAwareRevision).
	withdraw := func() {
		t.Helper()
		require.NoError(t, core.Write(testCtx, func(tx authority.Tx) error { return tx.BumpOwnerAwareRevision(module) }))
	}
	withdraw()
	require.Equal(t, before+1, held(i), "the seal moves by the core's owner-aware revision of the installed module")
	withdraw()
	require.Equal(t, before+2, held(i), "and by every later one")
	require.Equal(t, beside, held(other), "another module's installation is sealed as it was")

	var aware uint64
	require.NoError(t, core.Read(testCtx, func(tx authority.Tx) (err error) {
		aware, err = tx.OwnerAwareRevision(module)
		return err
	}))
	require.Equal(t, uint64(2), aware, "the term added is the one the core's own seal reads")
}
