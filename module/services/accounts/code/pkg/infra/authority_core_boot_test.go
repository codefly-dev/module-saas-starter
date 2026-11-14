//go:build !pure

package infra_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// A host starts without a module registry; its reconcile pass derives one from
// the committed envelope. The authority core's boot reconciliation reads every
// module installation's consent through that registry, so run at start it is
// refused for every consent: "the registry was derived from envelope revision
// 0 and revision N is committed" (seen on a hosted cell, 2026-11-12). On a
// host whose pass derives the registry it waits for that pass, and the pass
// completes it.
func TestAuthorityCoreBootReconciliationWaitsForTheRegistryPass(t *testing.T) {
	s := newRegistryScenario(t, "bootorder")
	key := func() ed25519.PrivateKey {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		return key
	}
	// The running host: its core composes what its passes apply and installs
	// every composed module at every organization, so the module and its
	// installation are the core's when the host restarts.
	require.NoError(t, s.delivery.service.ConfigureAuthorityCore(testCtx, "accounts.test", "boot-test", key()))
	revision := s.putEnvelope(t, envelopeOptions{resources: true})
	s.deliverManifest(t, s.primary, 1)
	s.pass(t)
	core, err := s.delivery.service.AuthorityCore()
	require.NoError(t, err)
	_, err = core.Composed(testCtx, s.prefix)
	require.NoError(t, err, "the established module must be composed in the core")

	// A restart with no pass to wait for reconciles at once, and the registry
	// it reads is the empty one a host starts with.
	unwired := newRegistryDelivery(t).service
	err = unwired.ConfigureAuthorityCore(testCtx, "accounts.test", "boot-test", key())
	require.ErrorIs(t, err, business.ErrModuleRegistryStale)
	require.ErrorContains(t, err, "derived from envelope revision 0")

	// A restart whose pass derives the registry: the core serves at once and
	// its reconciliation waits for that pass, which it asks for. The host is
	// wired as work() wires it: the module capability surface is set before
	// the core is built, with no registry, so the registry it holds then is
	// the empty one, read as derived from envelope revision 0 (seen again on
	// a hosted cell, 2026-11-13, every start since the wait was added).
	restarted := newRegistryDelivery(t).service
	restarted.SetModuleCapabilities(testStore, nil, nil)
	reconciler := newRegistryReconciler(t, restarted, productionVerifier(t), restarted)
	asked := 0
	restarted.SetModuleRegistryPassTrigger(func() { asked++ })
	require.NoError(t, restarted.ConfigureAuthorityCore(testCtx, "accounts.test", "boot-test", key()))
	_, err = restarted.AuthorityCore()
	require.NoError(t, err, "the core serves while its reconciliation waits")
	require.Equal(t, 1, asked, "the reconciliation asks for the pass it waits for")
	require.True(t, restarted.AuthorityCoreBootPending(), "the reconciliation waits for the pass")

	// The pass derives the registry from the committed envelope, then
	// completes the reconciliation against it. The inbox is host-wide, so the
	// pass may report other tests' bindings; never a stale registry.
	if err := reconciler.RunOnce(testCtx); err != nil {
		require.NotErrorIs(t, err, business.ErrModuleRegistryStale)
	}
	_, present := entryOn(restarted, s.prefix)
	require.True(t, present, "the pass derived the registry at revision %d", revision)
	require.False(t, restarted.AuthorityCoreBootPending(), "the pass ran the reconciliation it waited for, and it completed")
}
