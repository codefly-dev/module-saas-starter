# Authorization model — three layers, defense-in-depth

> The starter ships three orthogonal authorization checks. Each is
> sufficient on its own to block the obvious attacks; **all three
> together** mean a single bug at any layer can't leak data across
> tenants. Adopting RLS as the third layer was the missing piece.

```
                     ┌─ User-issued request
                     ▼
        ┌────────────────────────┐
        │   L1  Policy gates     │   "Should this caller be ALLOWED to
        │   (handler-level)      │    invoke this RPC, given identity?"
        └────────────────────────┘
                     │
                     ▼
        ┌────────────────────────┐
        │   L2  Permissions      │   "Does this caller have the right
        │   (RBAC, business)     │    capability for this resource:action?"
        └────────────────────────┘
                     │
                     ▼
        ┌────────────────────────┐
        │   L3  RLS              │   "Even if everything above said yes,
        │   (DB row-level)       │    does the row physically belong to
        │                        │    the caller's tenant?"
        └────────────────────────┘
                     │
                     ▼
                 Postgres
```

## Layer 1 — Policy gates (handler-level)

**Where:** `pkg/adapters/auth.go`, `pkg/adapters/connect_auth_interceptor.go`,
called at the top of every Connect/gRPC handler.

**What it asserts:** the caller's identity satisfies preconditions for
the RPC to even run. None of these checks know about specific rows;
they're per-RPC gates on the caller's claims + role.

| Helper | Asserts |
|---|---|
| `requireAuth(ctx)` | A valid bearer JWT or API-key was presented. |
| `requireOrgMember(ctx, actor, orgID)` | Caller is a member of `orgID` (any role). |
| `requireOrgAdmin(ctx, actor, orgID)` | Caller is admin/owner of `orgID`. |
| `requirePlatformAdmin(ctx, actor)` | Caller has the platform `super_admin` role. |
| `requireMFA(ctx, actor)` | Caller's JWT carries `mfa: true`. Used for sensitive ops (rotate webhook secret, override entitlement, GDPR delete). |
| `requireScope(ctx, "res:action")` | API-key caller has the required scope (or wildcard). JWT callers pass through — RBAC handles them. |
| `rateLimitInterceptor` | Per-key request budget. |

**What this catches:** non-members hitting a tenant's RPCs, bare JWTs
hitting platform-only endpoints, API keys without the right scope, MFA
bypass on sensitive ops, IP/key floods.

**What this does NOT catch:** logic bugs *inside* a handler that look
up the wrong row (e.g., `getWebhook(id)` without filtering by orgID).
That's L2 + L3.

## Layer 2 — Permissions (RBAC, business layer)

**Where:** `pkg/business/service.go:CheckPermission` + the
`role_assignments` / `role_permissions` / `roles` tables in migration
4. Called when a handler needs to know "can THIS subject do THIS
action on THIS resource". Backed by `pkg/infra/postgres_permissions.go:CheckPermission`.

**What it asserts:** the caller has been granted a `(resource, action)`
permission in this org, either directly or via team inheritance, with
optional fine-grained `scope` (e.g. `projects/foo`).

Wildcard semantics: `resource="*"` matches all resources, `action="*"`
matches all actions, `*:*` is the root role. Team membership inherits
to the user. Scope NULL means "global" within the org.

**What this catches:** an org member trying to do something only
admins are supposed to do (e.g. an analyst hitting `users:write`),
even though L1 said "you're a member, come on in".

**What this does NOT catch:** a bug that reads `WHERE org_id = $1`
where `$1` came from the URL not the JWT (cross-tenant leak via
mass-assignment). Or a missing WHERE clause altogether. That's L3.

### Scope semantics

`role_assignments.scope` is a fine-grained authorization dimension *within*
an org (a module, product area, project — e.g. "analyst on module-a but not
module-b"). `CheckPermission` treats it strictly in both directions: a grant
scoped to `module-a` never widens to satisfy a check that asked for the
permission unscoped. A NULL-scope assignment is deliberately org-wide and
subsumes all scopes.

| Grant scope ↓ / Check scope → | `""` (unscoped) | `module-a` | `module-b` |
|---|---|---|---|
| `NULL` (org-wide) | ✅ | ✅ | ✅ |
| `module-a` | ❌ | ✅ | ❌ |

The two edges to note: a scoped grant does **not** satisfy an unscoped check
(a narrow grant stays narrow), and a NULL-scope grant satisfies every check
(org-wide subsumes scoped). If per-role subsumption is ever unwanted, that's
a follow-up design, not the default. Team-inherited assignments follow the
same matrix.

## Layer 3 — RLS (DB row-level)

**Where:** Postgres `ROW LEVEL SECURITY` + policies on every per-tenant
table. The api wraps every per-tenant request in a transaction that
sets `app.current_org_id`. Workers that legitimately span tenants set
`app.bypass = '1'` instead.

**What it asserts:** even if the SQL hitting Postgres has no WHERE
clause at all, it physically cannot return rows belonging to a
different tenant.

```sql
-- Example policy (applies to every per-tenant table):
CREATE POLICY tenant_isolation ON foo
USING (
    org_id::text = current_setting('app.current_org_id', true)
    OR current_setting('app.bypass', true) = '1'
);
```

The `, true` second arg to `current_setting` returns "" (not error)
when the setting is missing. So:

| Setting | Effect on a per-tenant query |
|---|---|
| `app.current_org_id = '<uuid>'` | Rows where `org_id = uuid` are visible. |
| `app.bypass = '1'` | All rows visible (workers + platform admin). |
| Neither set | **Zero rows** — fail-closed. |

**What this catches:**
- A SQL injection that bypassed `WHERE org_id = $1` parsing.
- A bug that uses the wrong orgID variable (`getWebhook(id, attackerOrg)` reading another tenant's row).
- A future Store method that legitimately compiles + tests but forgot the org filter.
- A connection that gets reused across requests without resetting state — the SET LOCAL is per-tx, gone on commit/rollback.

**What this does NOT catch:** it's defense-in-depth, not a primary
gate. L1 should still reject the request before L3 sees it; L3 is the
"belt" to L1's "suspenders."

## How the three layers compose

A request to `webhookConnectHandler.DeleteSubscription(orgID, subID)`:

| Layer | Check |
|---|---|
| Auth interceptor | JWT validated → caller `userID` on ctx |
| L1 Policy gates | `requireOrgAdmin(actor, orgID)` — member with admin role |
| L1 Policy gates | `requireScope("webhooks:write")` — for API-key callers |
| L1 Policy gates | (`requireMFA` for rotate-secret only) |
| L2 Permissions | `CheckPermission(actor, "webhooks", "write", orgID)` *if RBAC is more granular than the org-admin check* |
| L3 RLS | `WithOrgTx(ctx, orgID, …)` → DELETE WHERE id = subID. RLS lets it through only if the row's org_id matches. |
| Audit emit | `webhook.deleted` written to audit_events |

If any single layer is wrong, the others still hold:

| Bug | Caught by |
|---|---|
| Anonymous request | L1 (auth interceptor) |
| Wrong-org caller (hand-crafted request) | L1 + L3 |
| Authenticated org member without admin role | L1 (`requireOrgAdmin`) |
| API key without `webhooks:write` scope | L1 (`requireScope`) |
| Race between role-revoke and request | L1 cache invalidation; L3 always re-checks |
| SQL injection that drops `WHERE org_id` | L3 |
| New Store method developer forgets `WHERE org_id` | L3 |
| Cross-tenant lookup via swapped variable | L3 |

The worker paths (audit-exporter goroutine, webhook dispatcher,
billing reconciler, migration runner, platform-admin endpoints) bypass
L3 deliberately by setting `app.bypass = '1'`. They run unauthenticated
inside the api process — they're trusted by L1 implicitly. The L3
bypass is itself audit-able: every WithBypass call logs a wool event,
making it grep-able.

## Implementation status

| Layer | Status |
|---|---|
| L1 Policy gates | ✅ Live. All admin RPCs gated. Webhook + api-key handlers added scope checks 2026-04-26. |
| L2 Permissions | ✅ Live. `CheckPermission` + RBAC tables. Wildcard + team inheritance. |
| L3 RLS | ✅ Live across **all** per-tenant tables, **fail-closed** end-to-end. Connection-level role downgrade (`BeforeAcquire` SET ROLE app_tenant) makes un-wrapped Store calls return zero rows by default. `WithOrgTx` / `WithBypass` helpers, `app_tenant` role. 11 integration tests prove cross-tenant blocking + fail-closed-on-unwrapped across direct-org, JOIN, polymorphic, and self-referential policies. |

### RLS coverage (table → migration)

| Table | Policy shape | Migration |
|---|---|---|
| `audit_export_configs` | direct org_id | 23 |
| `webhook_subscriptions` | direct org_id | 27 |
| `webhook_deliveries` | JOIN via subscription | 27 |
| `api_keys` | direct organization_id | 28 |
| `org_settings`, `invitations`, `organization_members`, `subscriptions`, `entitlement_overrides`, `usage_records` | direct org_id | 29 |
| `teams` | direct org_id | 30 |
| `team_members` | JOIN via teams | 30 |
| `audit_events` | polymorphic (nullable org_id; NULL only via bypass) | 31 |
| `roles`, `role_assignments` | polymorphic (built-ins NULL globally readable) | 32 |
| `organizations` | self-referential (id matches setting) | 33 |

### Skip-list (intentionally NOT RLS-protected)

| Table | Why |
|---|---|
| `users` | Global lookup needed for cross-org auth flows; `WHERE user_id = $1` filters at the SQL layer. |
| `plans`, `plan_entitlements`, `feature_flags` | Global catalogs; no tenant data. |
| `oauth_state`, `refresh_tokens`, `mfa_devices`, `notifications` | User-scoped, not tenant-scoped. A symmetric `WithUserTx` + `app.current_user_id` GUC would close the symmetric defense, but the existing `WHERE user_id = $1` filters already provide the primary safety property. Lower priority. |
| `role_permissions` | Child of `roles` (FK role_id). A JOIN-via-role policy would be redundant — the rows are not tenant-secret data. |

## How the role-downgrade works

Codefly's Postgres plugin connects the api as a superuser. Postgres
superusers bypass RLS unconditionally, even with FORCE ROW LEVEL
SECURITY — so naive RLS is silently defeated. We solve this without
changing codefly's Postgres plugin:

```
                     pgxpool.Config.BeforeAcquire
                              ↓
   superuser conn ────► SET ROLE app_tenant ────► caller
                                                     │
                              ┌──────────────────────┤
                              ▼                      ▼
                       ┌─────────────┐         ┌─────────────┐
                       │ WithOrgTx   │         │ WithBypass  │
                       └─────────────┘         └─────────────┘
                              │                      │
                              ▼                      ▼
              SET LOCAL                  SET LOCAL ROLE NONE
              app.current_org_id = X     (revert to session_user
              (still app_tenant —         = the original superuser
               policy filters by org)     for this tx)

                              ↓                      ↓
                       RLS applies            RLS bypassed
                       fail-closed            (intentional)
```

`BeforeAcquire` runs on every connection checkout — every operation
the api performs (request-path or worker) starts as `app_tenant`.
`WithOrgTx` adds the org filter; `WithBypass` elevates back to
session_user via `SET LOCAL ROLE NONE` for the tx duration. Both
unwind on commit/rollback. `AfterRelease` does `RESET ROLE` as a
safety net before the connection returns to the pool.

The fail-closed property: a Store method called WITHOUT either
wrapper runs as `app_tenant` with no `app.current_org_id` set.
RLS policies see neither match, return zero rows. A bug that forgot
to wrap surfaces in tests as "expected 1 row, got 0" — loud, not
silent.

## Anti-patterns to avoid

- **Don't replace L1+L2 with L3.** RLS is defense-in-depth; it's not
  a substitute for handler authz. A permission check at the handler
  produces a clean 403; a row that RLS hides looks like "not found",
  which makes UX/debug worse.
- **Don't forget WithOrgTx on per-tenant Store calls** once policies
  are live. Missing one returns zero rows in production — fail-closed
  but invisible. Catch with cross-tenant tests per Store method.
- **Don't WithBypass casually.** Every bypass is a layer-skip;
  legitimate cases are workers + platform admin only. Greppable
  audit log: `wool.Get(ctx).In("WithBypass").Info(...)`.
- **Don't omit the empty-orgID guard.** WithOrgTx rejects "" — this
  is the load-bearing check that prevents a missing-context bug from
  silently matching the empty tenant.
