package business_test

import (
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// TestModuleEmitAuditEventDedupsRetriedTenantEmit exercises the issue #511
// idempotency guard end to end on the tenant path — the surface a module
// actually calls — against the real database. The lower-level guard-table test
// (infra/postgres_audit_idempotency_test.go) pins ReserveAuditIdempotency in
// isolation; this one proves the whole path a retried module emit takes
// (ModuleEmitAuditEvent -> WithOrgTx -> emitEntryTx -> EmitTx -> reserve +
// insert, all in one tenant transaction) collapses a duplicate to exactly one
// audit row while still admitting a genuinely distinct event.
//
// It is deliberately org-scoped: ClearAll does not truncate audit_events, and
// the package-locked database is reused across runs without truncation, so the
// assertion counts only rows for the org minted in THIS run (a fresh UUID),
// which isolates it from every prior run's residue. The idempotency keys are
// likewise made unique per run so a re-run cannot see a key an earlier run
// already reserved.
func TestModuleEmitAuditEventDedupsRetriedTenantEmit(t *testing.T) {
	clearData(t)
	_, org := mustUserAndOrg(t, testCtx, "idem@audit-test.com", "idem-audit", "Idem Co")

	// Wire a service against the real store with a real durable emitter (so the
	// reservation and the insert share the tenant transaction) and a registry
	// that grants the module principal its bound org — no cross-tenant grant is
	// needed because the call names its own tenant.
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	svc.SetAuditEmitter(emitter)
	backend := &fakeJobBackend{}
	svc.SetModuleCapabilities(backend, backend, business.ModulePrincipalRegistry{
		modulePrincSvc: {Queues: []string{"datasource", "documents"}},
	})
	caller := business.ModuleCaller{PrincipalID: modulePrincSvc, BoundOrg: org}

	key := "idem-" + business.NewIDString()
	emit := func(idempotencyKey string) error {
		return svc.ModuleEmitAuditEvent(testCtx, caller, org, "saas.document.ingested",
			"actor-1", "sol", "entry-1", idempotencyKey, nil)
	}

	// First emit writes the event; a retry of the identical key is a duplicate the
	// emitter silently absorbs (it reports success — the intended one-row effect is
	// already in place — so dedup is observable only in the row count, not here).
	require.NoError(t, emit(key), "first emit of a key must succeed")
	require.NoError(t, emit(key), "a retried emit of the same key must still report success")

	// A distinct key under the same (org, event_type) is a genuinely different
	// event and must be written, proving the guard dedups a retry without
	// suppressing real traffic.
	require.NoError(t, emit("idem-"+business.NewIDString()), "a distinct key must be written")

	// Exactly two rows: the deduped pair collapsed to one, plus the distinct
	// event. Three would mean the retry was not deduped; one would mean the
	// distinct event was wrongly suppressed.
	buckets, err := svc.AggregateAuditLog(testCtx,
		business.AuditQuery{OrgID: org, EventType: "saas.document.ingested"},
		business.AuditAggregationSpec{GroupBy: []string{"event_type"}})
	require.NoError(t, err)
	require.Len(t, buckets, 1, "one event_type group is expected")
	require.EqualValues(t, 2, buckets[0].Count,
		"the retried emit must collapse to one row, leaving two document.ingested rows")
}
