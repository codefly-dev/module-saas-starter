# Postgres RLS rollout plan

> Defense-in-depth for tenant isolation at the database row level.
> Layered on top of:
>
> - cache: tenant-scoped key prefixes (`pkg/cache/cache.go:Scoped`,
>   `TenantPrefix`, `UserPrefix`) — already shipped.
> - handler authz: `requireOrgAdmin` / `requireOrgMember` /
>   `requireScope` — already shipped.

The model is standard: per-tenant tables enable RLS and a policy that
filters by `current_setting('app.current_org_id')`. The api wraps every
per-tenant request in a transaction that sets that setting before
running queries.

## Status — Phase 1, 2A, 2B, 2C, 2D, 2E, 2F all live

- ✅ Foundation: `infra.WithOrgTx(ctx, orgID, fn)` + `WithBypass`.
- ✅ Empty-orgID guard.
- ✅ Connection-level role downgrade (`BeforeAcquire` SET ROLE
  app_tenant) — un-wrapped Store calls return zero rows by default.
- ✅ Phase 1 — `audit_export_configs` (migration 23).
- ✅ Phase 2A — `webhook_subscriptions`, `webhook_deliveries` (27),
  `api_keys` (28).
- ✅ Phase 2B — `org_settings`, `invitations`, `organization_members`,
  `subscriptions`, `entitlement_overrides`, `usage_records` (29).
- ✅ Phase 2C — `teams` (direct), `team_members` (JOIN) (30).
- ✅ Phase 2D — `audit_events` (polymorphic; NULL org_id via bypass) (31).
- ✅ Phase 2E — `roles`, `role_assignments` (polymorphic; built-ins
  NULL globally readable) (32).
- ✅ Phase 2F — `organizations` (self-referential `id` policy) (33).

11 integration tests live (4 from Phase 1+2A, 2 each for teams /
team_members JOIN, 2 for audit_events tenant + async emitter, 2 for
roles polymorphic + assignment cross-tenant, 2 for organizations
self-referential + ListOrganizations bypass). All assert
cross-tenant blocking AND fail-closed-on-unwrapped.

See `AUTHZ.md` for the table-by-table coverage and skip-list rationale.

## Phased rollout

### Phase 0 — wiring only (this commit)

- Land `WithOrgTx` and the rejects-empty test.
- No schema changes.
- No behavioural change to existing code.

### Phase 1 — adopt `WithOrgTx` on a single per-tenant code path

Pick **one** path that is fully org-scoped on read and write, and has
no cross-tenant scans. Best candidates:

- `audit_export_configs` (small, recent, 6 Store methods, no
  cross-tenant reads except `ListDueAuditExportConfigs` which the
  exporter goroutine runs — that one keeps the bypass path).
- `webhooks_subscriptions` (org-scoped on every method).

Refactor those Store methods to call `WithOrgTx` with the orgID from
the request. Verify all existing tests still pass. No RLS yet — the
tx-with-setting is a no-op.

### Phase 2 — turn on RLS for that one table

Migration:

```sql
-- Bypass role for the audit-exporter goroutine + migrations. Without
-- this, the cross-tenant ListDue... scan returns zero rows.
CREATE ROLE api_bypass_rls BYPASSRLS;
GRANT api_bypass_rls TO api_app;  -- the role the api connects as

ALTER TABLE audit_export_configs ENABLE ROW LEVEL SECURITY;
CREATE POLICY audit_export_configs_tenant ON audit_export_configs
    USING (org_id::text = current_setting('app.current_org_id', true));
```

The exporter goroutine and migrations connect using the bypass role;
request-path queries connect as the regular role and go through RLS.

This is the moment of truth — every per-tenant Store method MUST be
inside a `WithOrgTx`. A missed path silently returns zero rows
(fail-closed). Run the existing test suite + the new
`TestRLS_CrossTenantBlocked` (to be added) before merging.

### Phase 4 — bypass-role audit (still pending)

Walk the api startup + all background workers; identify everything
that connects to Postgres. For each, decide:

- **Bypass role** — anything that legitimately spans tenants
  (audit-exporter, webhook-dispatcher, billing-reconciler,
  migration runner, platform-admin queries).
- **Tenant role** — request-path code that always has an orgID.

Ship as separate codefly secrets / connection strings so a
misconfigured deploy fails loudly (wrong role → policy blocks
everything) rather than silently bypassing.

## Test pattern

For each table after RLS is enabled, add an integration test:

```go
// TestRLS_AuditExportConfigs_CrossTenantBlocked — proves RLS is
// actually firing. Without this assertion, a missing WithOrgTx
// somewhere would silently return zero rows in production.
func TestRLS_AuditExportConfigs_CrossTenantBlocked(t *testing.T) {
    // Seed a config for org-A using the bypass role.
    seedConfig(t, "org-A")
    // Request as org-B — must see nothing.
    err := store.WithOrgTx(ctx, "org-B", func(ctx context.Context) error {
        cfg, err := store.GetAuditExportConfig(ctx, anyConfigID)
        require.NoError(t, err)
        require.Nil(t, cfg, "RLS must hide org-A's row from org-B")
        return nil
    })
    require.NoError(t, err)
}
```

## Operational notes

- The `current_setting('app.current_org_id', true)` call is cheap
  (parsed once per query), but if hot paths show up in pg_stat_statements
  we can read it once per tx into a local variable.
- A WithOrgTx call that errors before fn returns leaves the connection
  in a clean state — `tx.Rollback` is deferred and idempotent.
- For long-running connections in pgxpool, the SET LOCAL is cleared on
  commit/rollback automatically. There is no setting leakage across
  pool reuse.
- Postgres logs `set_config` calls at NOTICE level by default. If that's
  too noisy in production, set `client_min_messages = warning` for the
  api role.
