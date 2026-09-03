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

## Scoped roles downstream: two paths, and when to use which

A product is many backend services, each with per-module roles. A downstream
service authorizing on a **scoped** role assignment
(`role_assignments.scope IS NOT NULL`, e.g. `analyst` on `module-a`) has two
ways to learn the caller's grants. Both are first-class; pick by freshness need.

### Path A — the `X-Scoped-Roles` header (fast, token-fresh)

At mint / refresh / org-switch, accounts resolves the caller's **direct,
principal-subject** scoped assignments in the active org and stamps them onto
the access token as the compact `sr` claim:

```json
"sr": { "module-a": ["analyst"], "module-b": ["admin", "editor"] }
```

The auth-gateway forwards this to downstream services as the JSON
`X-Scoped-Roles` header (like `X-Org-Role` / `X-Platform-Role`). A service reads
it from the request context alone — no callback to accounts:

```go
// The two-value return prevents reading a miss as a denial: on a truncated
// header (conclusive == false) the caller falls back to the authoritative path.
granted, conclusive := adapters.HasScopedRole(ctx, "module-a", "analyst")
switch {
case granted:
    // allow
case !conclusive:
    // header incomplete — consult CheckPermission (Path B)
default:
    // deny
}
```

Properties and limits:

- **Freshness:** as fresh as the token. A scoped-assignment change revokes the
  caller's sessions (migration 91, mirroring how `organization_members` changes
  revoke sessions in migration 70), so a live token's `sr` claim is never
  staler than one refresh cycle — but a long-lived, un-refreshed token can lag.
- **Direct principal grants only.** Team-inherited grants and org-global
  (NULL-scope) roles are **not** in the claim — those stay on Path B, which
  honors team inheritance and wildcards.
- **Bounded, and truncation is signalled.** The claim carries at most
  `auth.MaxScopedRoleAssignments` (64) scoped pairs so token size stays
  predictable. A caller with more keeps a bounded slice and the token sets the
  `srt` claim (`X-Scoped-Roles-Truncated: true`); the mint does **not** fail, so
  the user is never locked out. When that flag is set, an absent grant in the
  header is *unknown*, not a denial — the service must consult Path B. Use
  `adapters.ScopedRolesTruncatedFromContext(ctx)` to detect it.

### Path B — `PermissionService.CheckPermission` (authoritative, current)

For callers that must have the current answer — long-lived jobs whose token
predates a role change, or high-sensitivity operations — call the oracle:

```go
client := authzclient.New(conn, os.Getenv("CODEFLY_INTERNAL_TOKEN"))
resp, err := client.CheckPermission(ctx, &accountsv1.CheckPermissionRequest{
    SubjectId: userID, SubjectKind: accountsv1.SubjectKind_SUBJECT_KIND_PRINCIPAL,
    Resource:  "deployments", Action: "write",
    OrgId:     orgID, Scope: "module-a", // empty scope = any scope
})
```

`CheckPermission` is `EXPOSURE_INTERNAL` (`generated/authz-methods.json`): it is
served only on the internal transport and requires the service credential
(`X-Codefly-Internal-Token`). A caller without it is refused before the handler
runs; a tenant transport refuses the method outright. It honors the full RBAC
model — team inheritance, wildcard `resource`/`action`, and `scope` (a NULL
assignment scope matches any requested scope). Polyglot (Python/TS) services
call the same RPC over gRPC/Connect with the credential in metadata.

### Which to use

| | Path A — header | Path B — CheckPermission |
|---|---|---|
| Cost | zero round-trips | one internal RPC |
| Freshness | token-fresh (≤ 1 refresh) | live, authoritative |
| Covers | direct scoped grants | + team inheritance, wildcards, NULL-scope |
| Reach for it when | per-request checks on the hot path | long-lived jobs, high-sensitivity ops, non-scoped RBAC |

Default to Path A on the request hot path; escalate to Path B when a stale
answer is unacceptable.

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
| `org_identity_providers` | direct org_id (pre-auth discovery via control-plane) | 92 |

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

## Built-in role catalog import

Migration 4 seeds `admin` / `editor` / `viewer` as hand-written SQL. Products
that already maintain a permission catalog outside this repo — a machine-readable
list of roles and `resource:action` grants reviewed in their own CI — can sync it
into the L2 tables without forking migrations, using the catalog importer.

> This is **not** the generated authorization catalog in
> `module/AUTHORIZATION_CATALOG.md`. That projects per-RPC *method policy*; this
> one seeds *RBAC roles*.

### Catalog format (versioned JSON)

```json
{
  "version": 1,
  "roles": [
    {
      "name": "module-a:analyst",
      "description": "Read access to module A",
      "scope": "module-a",
      "permissions": [
        {"resource": "reports", "action": "read"},
        {"resource": "queries", "action": "execute"}
      ]
    }
  ]
}
```

- `version` must be `1`. `resource`/`action` accept `*` for wildcard grants.
- `scope` is stored on the role (`roles.scope`) as the default
  `role_assignments.scope` for later assignments. Deriving it at assignment time
  lands with strict scope semantics; until then the column is recorded but not
  yet consulted by the assignment path.

### Semantics

- **Upsert built-in roles keyed by name** (`built_in = true`, `org_id IS NULL`).
  A role the catalog names but that a hand-seeded built-in already occupies
  (e.g. `admin`) is *adopted* into catalog management.
- **Diff-apply permissions** — only the `(resource, action)` rows that differ
  are inserted or deleted, so changing one permission is exactly one row change.
- **Provenance bounds deletion.** Only `roles.catalog_managed = true` rows are
  removal candidates; a catalog that doesn't mention `admin`/`editor`/`viewer`
  leaves them alone. Org-defined custom roles (`org_id` set) are never touched.
- **One `system`-actor audit event per applied change** (`role.created` /
  `role.updated` / `role.deleted`, `org_id` NULL), stamped with the catalog's
  SHA-256 (`catalog_sha256`) and source label (`catalog_source`) so a change is
  traceable to the exact catalog version that produced it.

### Safety

- `-dry-run` prints the byte-stable plan and writes nothing.
- A removal that would cascade away existing `role_assignments` is **refused**
  unless `-force` (which then deletes those assignments along with the role).
- A catalog that declares **no roles at all** would remove every catalog-managed
  role — almost always a truncated or empty file rather than an intentional
  "delete everything", and one the assignment guard above can't catch for roles
  without assignments. It is refused unless `-force`.
- Same catalog in → same DB state out; a second run is a no-op.

### Workflow

Built-in roles can only be written with RLS bypassed (migrations 32, 65), so the
importer runs under the audited `app_control_plane` role. The connection
principal must be a member of that role (the same authority migrations run
under).

```sh
# from module/services/accounts/code
go run ./cmd/role-catalog-import -catalog roles.json -database-url "$DATABASE_URL" -dry-run
go run ./cmd/role-catalog-import -catalog roles.json -database-url "$DATABASE_URL"
```

Domain core is `pkg/rolecatalog` (parse + diff + deterministic plan, no DB);
`pkg/infra` snapshots current state and applies the plan in one transaction.

### Composed contribution catalog (deploy step)

A consuming solution does not hand-write the catalog. It ships a
`PermissionsContribution` (`codefly/saas/permissions-contribution/v1`), and
`module-compose` regenerates the catalog the importer applies:

1. The contribution declares `resource:action` permissions under a namespace
   reserved to that solution. `module-compose` merges every contribution into
   the permission vocabulary (`deployment/generated/contributed-permissions.json`,
   `pkg/permissioncatalog/catalog_gen.go`) and, from the same input, emits the
   role catalog `deployment/generated/contributed-roles.json` in the versioned
   format above (`module/tools/composition/composition.go`).
2. A permission exists in the RBAC schema only as a grant inside a role
   (`role_permissions` has no standalone permission registry), so the bridge
   materializes one built-in role per contributing namespace —
   `name: "<namespace>:catalog"`, `scope: "<namespace>"` — granting that
   namespace's full permission set. Each grant is stored verbatim as the
   contributed `{resource, action}` (the `resource` is namespace-qualified,
   e.g. `reference.console`). This is the authority the Work Context layer
   reads as a `WorkContextScope` (`resource_kind` + `actions`); mapping the
   stored grant onto that scope shape is the enforcement layer's concern
   (#415/#416) — this bridge lands the grants, it does not itself transform
   them. Finer-grained roles remain an org's own custom roles, which the
   importer never touches.
3. Both generated files are **base** — base-manifest-tracked
   (`module/tools/base-manifest.json`). A consumer cannot hand-edit them: a
   permission or role reaches a deployment only through contribution →
   regeneration, and editing the generated file without regenerating fails the
   base-integrity gate.

The composed `contributed-roles.json` is applied by `role-catalog-import` as a
bring-up step that runs **after** the store migration Job, under the same
`app_control_plane` connection — seeding the contributed roles automatically
instead of by the manual `go run` above. Its interaction with the importer's
empty-catalog guard is deliberate, not incidental:

- A module with **no** contributions emits `{"version": 1, "roles": []}`.
  Applied to a deployment that holds no catalog-managed roles yet, that is a
  clean no-op — nothing to create, nothing to remove — and exits `0`.
- Once contributions have seeded `<namespace>:catalog` roles, **regenerating
  back down to an empty catalog** (the last contributing solution was removed)
  makes the importer *refuse* rather than silently wipe them: an empty document
  is indistinguishable from a truncated file, so the guard demands `-force`.
  That transition is a genuine role removal, so the deploy step passes `-force`
  for it. The guard is not weakened for a generated catalog — emptying
  contributed authority is exactly the destructive step `-force` exists to
  confirm.

The module declares this step so the driver need not know the module's internals.
`deployment/topology.bindings.codefly.yaml` carries a top-level `deploy_jobs`
entry for `role-catalog-import`: it runs the `accounts` image (which ships the
importer binary and connects under the `app_control_plane` migration authority)
but *writes* to the `store` dependency and runs `after` the store — the boundary
a self-serving `bootstrap_job_endpoints` Job cannot express, since that models a
service reaching only its own endpoints. The generated bundle
([`deployment/README.md`](module/deployment/README.md)) surfaces it per
environment under `deployJobs`, resolving the catalog artifact, the store
endpoint/port, the `force` flag, and the ordering. The promotion driver mounts
the catalog, connects the store, runs the importer, and fails the promotion if
it exits non-zero — the same way it schedules the store migration Job it already
runs. It carries no repository, revision, or Argo resource.

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
