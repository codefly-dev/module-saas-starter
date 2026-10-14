//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A presence the inbox holds at a generation its binding has not applied is
// counted until the binding reconciler applies it, and not after.
func TestUnappliedPresenceIsCountedUntilItsBindingApplies(t *testing.T) {
	s := newRegistryScenario(t, "unap")
	// On the control plane, as its one production caller reads it: the
	// request role sees neither table.
	unapplied := func() (uint64, error) {
		var count uint64
		err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			var err error
			count, err = testStore.CountUnappliedPresence(ctx)
			return err
		})
		return count, err
	}
	before, err := unapplied()
	require.NoError(t, err)

	current := s.currentRevision(t)
	_, err = s.delivery.service.PutModuleEnvelope(testCtx, s.admin, current, s.envelope(t, current+1, envelopeOptions{resources: true}))
	require.NoError(t, err)
	s.deliverPresence(t, s.primary, 1, current+1)
	pending, err := unapplied()
	require.NoError(t, err)
	require.Equal(t, before+1, pending, "a delivered presence nothing has applied is pending")

	if err := s.passReasons(t); err != nil {
		t.Logf("reconcile refusals: %v", err)
	}
	record := productionPathBindingRecord(t, s.delivery.service, s.primary.binding)
	require.NotNil(t, record.Applied, "this binding must actually apply before checking the pending count")
	require.Equal(t, uint64(1), record.Applied.Generation)
	after, err := unapplied()
	require.NoError(t, err)
	require.Equal(t, before, after, "an applied presence is no longer pending")
}
