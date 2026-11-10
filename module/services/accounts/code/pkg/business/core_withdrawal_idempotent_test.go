//go:build pure

package business

import (
	"context"
	"crypto/ed25519"
	"sync"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
)

// Blue 2026-11-08: a withdrawal applied by the delivery endpoint's pass and the
// reconciler's at once logged `module "<m>": not found` on the second, which
// had read the module as composed just before the first withdrew it. A
// withdrawal is idempotent: however many passes apply it, none fails.
func TestAWithdrawalAppliedByConcurrentPassesNeverFails(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	m := domain.Module{Name: "example-module", Principal: "example-module", Kind: domain.KindModule, Generation: 1, InstallationRole: domain.NoInstallationRole}
	require.NoError(t, store.Write(ctx, func(tx authority.Tx) error { return tx.PutModule(m) }))
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	core, err := authority.New(store, authority.Config{Issuer: "test.example", KeyID: "key", Signer: private, Keys: staticKeys{"key": public}, Replay: workcontext.NewMemoryReplayStore()})
	require.NoError(t, err)
	s := &Service{authorityCore: &authorityCoreState{core: core}}
	removed := &solutionhost.ModuleManifest{Alias: m.Name, Removed: true}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.applyCoreModule(ctx, removed, nil, nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	_, err = core.Composed(ctx, m.Name)
	code, _ := domain.CodeOf(err)
	require.Equal(t, domain.NotFound, code, "the module is withdrawn")

	// Applied again later (a reconcile pass over the applied removal), still nothing fails.
	_, err = s.applyCoreModule(ctx, removed, nil, nil)
	require.NoError(t, err)
}
