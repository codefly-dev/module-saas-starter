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

## Status

- ✅ Foundation: `infra.WithOrgTx(ctx, orgID, fn)` helper. Begins a
  transaction, sets `app.current_org_id` via `set_config(..., true)`,
  threads the tx onto the context using the same key
  `RunInTransaction` uses so existing Store methods inherit it.
- ✅ Empty-orgID guard test (`TestWithOrgTx_RejectsEmptyOrgID`).
- ❌ No table has RLS enabled yet. Adopting `WithOrgTx` upstream of
  the policies costs nothing — queries still hit normally — and means
  the day a policy goes live, the wrapped paths Just Work.

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

### Phase 3 — fan out to remaining per-tenant tables

In rough order of stake:

| Table | Per-tenant? | Cross-tenant readers? | Effort |
|---|---|---|---|
| audit_events | yes (`org_id`) | audit-exporter goroutine | medium |
| api_keys | yes | none | small |
| webhook_subscriptions | yes | none | small |
| webhook_deliveries | yes (via subscription) | dispatcher worker | medium |
| entitlement_overrides | yes | platform admin | small (admin uses bypass) |
| usage_records | yes | reporting/billing aggregator | medium |
| invitations | yes | none | small |
| teams + team_members | yes | none | small |
| roles + role_assignments | yes | none | small |
| subscriptions | yes | webhook handler (Stripe events) | medium |
| audit_export_configs | yes | exporter goroutine | covered in Phase 2 |
| organizations | tricky — owner can be platform-admin + tenant rows | needs special policy | hard |

Skip-list (intentionally NOT RLS-enabled):

- `users` — global lookup needed for cross-org auth flows.
- `plans`, `plan_entitlements` — global catalog.
- `feature_flags` — global config.
- `oauth_state`, `refresh_tokens`, `mfa_devices` — user-scoped, not
  tenant. Could get a `user_id` policy instead if we want symmetric
  defense for user-scoped tables; lower priority.

### Phase 4 — bypass-role audit

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
