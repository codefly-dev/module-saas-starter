//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"testing"

	"accounts/pkg/business"
	"github.com/stretchr/testify/require"
)

func TestPostgresAuditEffectBinding(t *testing.T) {
	org := seedOrg(t, seedUser(t))
	entry := business.AuditEntry{ID: business.NewIDString(), OrgID: org,
		EventType: "example.changed", ActorID: business.NewIDString(), ActorType: business.ActorTypeUser,
		Resource: "example", ResourceID: "entry", IdempotencyKey: "effect", Payload: map[string]any{"value": 1}}
	reserve := func(e business.AuditEntry) (bool, error) {
		var inserted bool
		err := testStore.As(business.Identity{OrgID: e.OrgID}).Within(testCtx, func(ctx context.Context) error {
			var err error
			inserted, err = testStore.ReserveAuditEffect(ctx, e)
			return err
		})
		return inserted, err
	}
	inserted, err := reserve(entry)
	require.NoError(t, err)
	require.True(t, inserted)
	retry := entry
	retry.ID = business.NewIDString()
	inserted, err = reserve(retry)
	require.NoError(t, err)
	require.False(t, inserted)
	changed := retry
	changed.Payload = map[string]any{"value": 2}
	_, err = reserve(changed)
	require.ErrorIs(t, err, business.ErrAuditIdempotencyConflict)
	// Historical key-only reservations cannot be upgraded from a retry's claims.
	legacy := entry
	legacy.IdempotencyKey = "legacy"
	require.True(t, reserveAuditTenant(t, org, string(entry.EventType), legacy.IdempotencyKey))
	_, err = reserve(legacy)
	require.ErrorIs(t, err, business.ErrAuditIdempotencyUnverifiable)
	// A failed enclosing effect frees the reservation with its transaction.
	rolledBack := entry
	rolledBack.IdempotencyKey = "rollback"
	aborted := errors.New("effect transaction aborted")
	err = testStore.As(business.Identity{OrgID: org}).Within(testCtx, func(ctx context.Context) error {
		inserted, err := testStore.ReserveAuditEffect(ctx, rolledBack)
		if err != nil {
			return err
		}
		require.True(t, inserted)
		return aborted
	})
	require.ErrorIs(t, err, aborted)
	rolledBack.Payload = map[string]any{"value": 3}
	inserted, err = reserve(rolledBack)
	require.NoError(t, err)
	require.True(t, inserted)
	// The same event/key in a different tenant is an independent effect.
	other := entry
	other.OrgID = seedOrg(t, seedUser(t))
	other.ID = business.NewIDString()
	inserted, err = reserve(other)
	require.NoError(t, err)
	require.True(t, inserted)
}
