//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// reserveAuditTenant runs ReserveAuditIdempotency once for a tenant org, in its
// own committed transaction — the shape a retried emit takes (each retry is a
// separate transaction) — so a guard row written by one call is visible to the
// next. It returns the reserved flag: true means "newly reserved, write the
// event", false means "duplicate, skip".
func reserveAuditTenant(t *testing.T, org, eventType, key string) bool {
	t.Helper()
	var reserved bool
	require.NoError(t, testStore.As(business.Identity{OrgID: org}).Within(testCtx, func(ctx context.Context) error {
		var err error
		reserved, err = testStore.ReserveAuditIdempotency(ctx, org, eventType, key)
		return err
	}))
	return reserved
}

// reserveAuditSystem runs ReserveAuditIdempotency for a system-scoped emit (empty
// org) under the control plane, exercising the all-zero sentinel-org path.
func reserveAuditSystem(t *testing.T, eventType, key string) bool {
	t.Helper()
	var reserved bool
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		reserved, err = testStore.ReserveAuditIdempotency(ctx, "", eventType, key)
		return err
	}))
	return reserved
}

// TestPostgresReserveAuditIdempotencyDedupsPerTenantKey pins the issue #511
// audit-emit idempotency contract at the real guard table: the first emit of a
// (org, event_type, key) reserves and writes; a retry of the exact same key is a
// duplicate the emitter must skip; and dedup is scoped strictly per (org,
// event_type, key) so a different key, a different event type, or a different
// tenant never suppresses a genuine event.
func TestPostgresReserveAuditIdempotencyDedupsPerTenantKey(t *testing.T) {
	orgA := seedOrg(t, seedUser(t))
	orgB := seedOrg(t, seedUser(t))

	// First emit reserves the key and instructs the caller to write the event.
	require.True(t, reserveAuditTenant(t, orgA, "datasource.blob_fetched", "k1"),
		"the first emit of a key must reserve and write")

	// A retry of the identical key is a duplicate: no second reservation.
	require.False(t, reserveAuditTenant(t, orgA, "datasource.blob_fetched", "k1"),
		"a retried emit of the same key must be reported as a duplicate")

	// A different key under the same org and event type is a distinct event.
	require.True(t, reserveAuditTenant(t, orgA, "datasource.blob_fetched", "k2"))

	// The same key under a different event type is a distinct event: the key is
	// namespaced by event type, not global.
	require.True(t, reserveAuditTenant(t, orgA, "datasource.source.recovered", "k1"))

	// The same key under a different tenant is a distinct event: one tenant's keys
	// can never suppress another tenant's audit trail.
	require.True(t, reserveAuditTenant(t, orgB, "datasource.blob_fetched", "k1"))
	require.False(t, reserveAuditTenant(t, orgB, "datasource.blob_fetched", "k1"))
}

// TestPostgresReserveAuditIdempotencySystemScopedEmitsDedup pins that
// system-scoped emits (empty tenant, written under the control plane) still
// dedup: they collapse onto the all-zero sentinel org rather than silently
// disabling the guard. All system-scoped emits share that one fixed sentinel
// org, so — unlike the tenant case, where a fresh org per run keeps keys
// distinct — the keys themselves must be unique per run: the package-locked
// database is reused across invocations without truncation, so a fixed key
// would already be reserved by an earlier run.
func TestPostgresReserveAuditIdempotencySystemScopedEmitsDedup(t *testing.T) {
	first := "sys-" + uuid.NewString()
	second := "sys-" + uuid.NewString()
	require.True(t, reserveAuditSystem(t, "job.replayed", first),
		"the first system-scoped emit of a key must reserve and write")
	require.False(t, reserveAuditSystem(t, "job.replayed", first),
		"a retried system-scoped emit of the same key must be a duplicate")
	require.True(t, reserveAuditSystem(t, "job.replayed", second))
}
