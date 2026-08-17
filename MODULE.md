# saas-starter — Module Reference

Multi-tenant SaaS backend with three-layer authorization (handler gates + RBAC
+ Postgres RLS), a Next.js authenticated product, a separately deployable
public marketing site, and per-service introspection endpoints.

A codefly **module** is a collection of **services**; each service owns its own proto. The accounts service of saas-starter exposes a self-describing catalog of its OWN RPCs / RBAC vocabulary / RLS-protected tables / scopes. A "module-level" view = aggregation across every service's catalog and is a separate concern (CLI / gateway / Mind aggregator) — not encoded in any single proto.

## Quick links

- Composing this module into a downstream workspace: [Composing this module into a workspace](#composing-this-module-into-a-workspace)
- Runnable local product with real identity: [LOCAL_DOGFOODING.md](./LOCAL_DOGFOODING.md)
- External-provider bootstrap scripts: [scripts/setup/README.md](./scripts/setup/README.md)
- Runtime capability owners and provider boundaries: [Runtime capability ownership](#runtime-capability-ownership)
- SigNoz dashboard/alert provisioning qualification: [module/SIGNOZ_PROVISIONING.md](./module/SIGNOZ_PROVISIONING.md)
- accounts service introspection (after `codefly run`): `GET /v1/.well-known/service-info`
- Connect-RPC: `saas.accounts.v1.IntrospectionService/GetServiceInfo`
- Code: `pkg/business/introspection.go`
- Test: `pkg/business/introspection_test.go`
- Source-of-truth protos: `module/services/accounts/proto/saas/accounts/v1/*.proto` (each bounded context owns one file)
- Normalized generator input: `module/services/accounts/generated/service-catalog.json`
- Catalog contract and workflow: `module/SERVICE_CATALOG.md`
- Generated frontend clients and vocabulary: `module/FRONTEND_CATALOG.md`
- Generated frontend plugin routes/navigation: `module/FRONTEND_PLUGINS.md`
- Generic event meters, quota semantics, and product integration: `module/USAGE_METERING.md`
- Evidence-bound trust claims and adopter responsibilities: `module/TRUST_CAPABILITIES.md`
- Postgres roles, grant rules, and RLS authority: `module/DATABASE_AUTHORITY.md`
- Generated Codefly topology and NetworkPolicies: `module/DEPLOYMENT_TOPOLOGY.md`
- Marketing runtime, deployment, and extraction contract: `module/services/marketing/README.md`
- Typed public brand and site configuration: `module/public/site.config.json`
- Generated PDP input: `module/services/accounts/generated/authz-methods.json`
- Authorization catalog and enforcement boundary: `module/AUTHORIZATION_CATALOG.md`
- Generated gateway inventory: `module/services/accounts/generated/gateway-routes.json`
- Gateway route contract and rollout boundary: `module/GATEWAY_ROUTES.md`
- Generated REST inventory: `module/services/accounts/generated/rest-surface.json`
- REST/OpenAPI contract and extension boundary: `module/REST_SURFACE.md`

## Architecture

```text
example.com / www.example.com             app.example.com
  ↓                                         ↓
marketing service                         product frontend
  ├─ repository content                     ↓ same-origin API proxy
  ├─ public plan projection                auth-sidecar
  └─ fixed auth handoff                      ↓
                                           accounts service
                                             ├─ adapters/ — gRPC + Connect + REST gateway servers
                                             ├─ business/ — RBAC + RLS wrappers
                                             └─ infra/ — Postgres + Redis + Vault

Product API traffic
  ↓ Connect-ES (TS, generated from buf)
Gateway (Envoy/KrakenD) — merges per-service REST endpoints
  ↓ Bearer JWT or X-API-Key, plus X-Scopes
auth-sidecar — validates token, stamps x-user-id / x-org-id metadata
  ↓ gRPC
accounts service
  ├─ adapters/ — gRPC + Connect + REST gateway servers
  ├─ business/ — Service methods (RBAC + RLS wrappers go here)
  └─ infra/    — Store impl (Postgres + Redis + Vault)
       ↓
     Postgres pool with BeforeAcquire SET ROLE app_tenant
     ↓
     RLS-enforced reads / writes
```

The marketing process has no authentication session, database, Vault, object
store, admin client, or product feature dependency. Product and marketing have
independent build, health, deployment, rollback, cache, and hostname policies.

## Runtime capability ownership

This is the authoritative ownership decision for
[epic #98](https://github.com/codefly-dev/module-saas-starter/issues/98) and the
[PostHog](https://github.com/codefly-dev/module-saas-starter/issues/127),
[Unleash](https://github.com/codefly-dev/module-saas-starter/issues/128), and
[SigNoz](https://github.com/codefly-dev/module-saas-starter/issues/131) provider
implementations. Ownership names the only implementation allowed to serve a
capability; it does not claim that every planned provider is released yet.

| Capability | Single owner | Allowed SDK and runtime surface | Explicitly excluded |
|---|---|---|---|
| Runtime feature flags | Unleash | The provider-neutral `feature-flags@1` SDK may evaluate flags with consumer-scoped Edge/browser or server tokens. The Unleash admin API is module-only; public clients reach only Unleash Edge. | Product analytics, replay, exception capture, and OTLP/APM |
| Product analytics | PostHog | The `product-analytics@1` browser/server APIs may capture registered events and perform consent-gated identify, alias, organization grouping, and privacy suppression. An event may carry a variant already evaluated by Unleash for experiment analysis. | Flag definition or evaluation, exception capture, and traces, metrics, or logs |
| Session replay | PostHog | A browser-only recorder may start after separate replay consent and redaction policy resolve to allow. It has no server SDK or implicit analytics-consent fallback. | Flags, errors, and APM signals |
| Error tracking | Sentry | Browser/server exception events, release/environment tags, source-map upload, and the error-issue workflow. Provisioning credentials remain build/provider-only. | Performance transactions, profiles, replay, logs, metrics, and feature flags |
| APM traces, metrics, and logs | SigNoz | Applications use OpenTelemetry SDKs and standard OTLP configuration through the in-graph collector. SigNoz receives those signals as the OTLP backend. | Product analytics, replay, feature flags, and the Sentry error-issue contract |

Provider manifests are allowlists. `provider-posthog` may project only
`product-analytics@1`; its browser initialization must disable flag remote
configuration and exception autocapture, and replay must remain stopped until
replay consent is granted. `provider-unleash` may project only
`feature-flags@1`. Sentry may project only `error-tracking`; the starter fixes
Sentry trace sampling at zero and installs no Sentry tracing integration.
`provider-signoz`, if API qualification succeeds, may manage exactly owned
dashboards and alerts but projects no application runtime configuration. In
particular, dashboard provisioning is not an application telemetry contract:
application OTLP endpoints remain instrumentation configuration resolved
through the telemetry service.

The starter keeps the capability configurations independent. Selecting
PostHog never selects flags, errors, or observability; selecting Sentry never
selects tracing; selecting an OTLP exporter never selects error tracking.
PostHog's current direct HTTP adapters expose capture, identity, grouping, and
suppression only, so no bundled vendor SDK can silently enable an overlapping
feature.

### Database-backed feature-flag retirement

The `feature_flags` table, read-only `ListFeatureFlags` API, and
`/admin/platform/feature-flags` page are a legacy migration inventory, not a
runtime flag owner. No application runtime evaluates or mutates that table: the
former database evaluator, combined flag/entitlement checker, and mutation path
have been removed. The published v1 `UpsertFeatureFlag` RPC remains deprecated
for wire compatibility, but its handler always fails closed and the runtime
database roles have no write grants. The remaining surface is retired in this
order:

1. Land the
   [`feature-flags@1` contract](https://github.com/codefly-dev/core/issues/281),
   [Unleash service](https://github.com/codefly-dev/module-saas-starter/issues/130),
   and [Unleash provider](https://github.com/codefly-dev/module-saas-starter/issues/128).
2. Export each legacy row through the read-only inventory API and map `enabled`,
   `rollout_percent`, and
   `target_org_ids` to one explicitly owned Unleash project/environment and its
   strategies. Reject ambiguous mappings, import once, then verify equivalent
   evaluations against fixed organization fixtures. Do not add dual reads or
   dual writes.
3. Bind consumers to `feature-flags@1` and remove the admin route/navigation,
   list RPC and messages, business/infra read methods, generated read surfaces,
   permissions, and tests in the same cutover. Keep the deprecated v1 mutation
   compatibility shim until the stable-major support policy permits removal.
4. After the imported project is verified and no legacy consumers remain, add
   a forward migration that drops `feature_flags` and its grants/indexes. The
   historical migration that originally created the table remains immutable.

Entitlements and plan gates stay in Accounts and Postgres. They answer whether
an organization bought or is allowed a product capability; Unleash answers
which runtime behavior is rolled out. A flag may disable entitled behavior but
must never grant an entitlement, raise a quota, or replace authorization. Each
product path checks its entitlement independently from its flag evaluation.

## Three layers of authorization

| Layer | Where | What it asserts |
|---|---|---|
| **L1** Handler gates | `adapters/auth.go`, `adapters/connect_auth_interceptor.go`, `adapters/rpcs.go` | "Should this caller be ALLOWED to invoke this RPC?" — `requireAuth` / `requireOrgMember` / `requireOrgAdmin` / `requirePlatformAdmin` / `requireMFA` / `requireScope`, plus rate-limiting. |
| **L2** RBAC | `business/service.go:CheckPermission` (+ Postgres `roles` / `role_permissions` / `role_assignments`) | "Does this caller have THIS capability for THIS resource?" — wildcard support (`*:*`, `users:*`, `*:read`), team inheritance. |
| **L3** RLS | Postgres `ROW LEVEL SECURITY` + `WithOrgTx` / `WithBypass` | "Even if L1+L2 said yes, does the row physically belong to the caller's tenant?" — fail-closed via `BeforeAcquire SET ROLE app_tenant`. |

A bug in any one layer is caught by the others. See `AUTHZ.md` for the deep dive.

## RLS — protected tables

The runtime introspection catalog is the current protected-table inventory.
Important direct-tenant tables include:

| Table | Policy | Notes |
|---|---|---|
| `audit_export_configs` | direct `org_id` | Phase 1 |
| `webhook_subscriptions` | direct `org_id` | Phase 2A |
| `webhook_deliveries` | JOIN via `subscription_id` | Phase 2A |
| `api_keys` | direct `organization_id` | Phase 2A |
| `org_settings`, `invitations`, `organization_members`, `subscriptions`, `entitlement_overrides` | direct `org_id` | Phase 2B |
| `usage_events`, `usage_totals` | direct `org_id` | Immutable attempts + monthly read model; no application bypass branch |
| `teams` | direct `org_id` | Phase 2C |
| `team_members` | JOIN via `team_id` → teams.org_id | Phase 2C |
| `audit_events` | polymorphic; NULL `org_id` rows visible only via bypass | Phase 2D |
| `roles`, `role_assignments` | polymorphic; built-ins (NULL) globally readable | Phase 2E |
| `organizations` | self-referential (`id` matches setting) | Phase 2F |

**Skip-list** (intentionally NOT RLS-protected):

- `users`, `plans`, `plan_entitlements`, `feature_flags` — global lookup tables.
- `oauth_state`, `refresh_tokens`, `mfa_devices`, `notifications` — user-scoped (the `WHERE user_id = $1` SQL filter is the safety property; symmetric `WithUserTx` is a future improvement).
- `role_permissions` — child of `roles` via FK; resource:action pairs are not tenant-secret.

## RBAC vocabulary

| Resource | Actions | Built-in roles holding write |
|---|---|---|
| `*` | `*` | admin |
| `users` | read, write | admin, editor (write); +viewer (read) |
| `teams` | read, write | admin, editor (write); +viewer (read) |
| `knowledge` | read, write | admin, editor (write); +viewer (read) |
| `billing` | read, write | admin (write); +editor (read) |
| `audit` | read | admin |
| `webhooks` | read, write | admin |
| `api_keys` | read, write | admin |

Built-ins: **admin** (wildcard), **editor** (read+write on domain), **viewer** (read-only). Custom roles can be created per org via `CreateRole(orgId)`.

## API-key scopes

| Scope | Description |
|---|---|
| `*:*` | Root |
| `users:read|write` | List/manage users |
| `orgs:read|write` | Read/manage org metadata + members |
| `teams:read|write` | List/manage teams |
| `roles:read|write` | List/manage roles + assignments |
| `api_keys:read|write` | Manage API keys |
| `audit:read` | Read audit events |
| `invitations:read|write` | List/manage invites |
| `webhooks:read|write` | Manage webhook subs (incl. RotateSecret needs `:write` + MFA) |
| `billing:read|write` | View / open billing portal |
| `entitlements:read` | View overrides + usage |

Wildcard semantics: `users:*` matches all `users:X`; `*:read` matches read across resources.

## RPC catalog (selected)

The full machine-readable list is derived from protobuf descriptors and `saas.policy.v1.method_policy`; only editorial summaries remain in `pkg/business/introspection.go:rpcDescriptions`. The complete deterministic generator input is checked in at `module/services/accounts/generated/service-catalog.json`, and the redacted runtime view is served at `GET /v1/.well-known/service-info`. Highlights:

### IntrospectionService

| RPC | Path | Authz |
|---|---|---|
| `GetServiceInfo` | `GET /v1/.well-known/service-info` | public (privileged tiers redacted for anonymous callers) |

### PermissionService — RBAC

| RPC | Path | Authz | Audit |
|---|---|---|---|
| `CreateRole` | `POST /v1/roles` | org_admin | ✓ |
| `ListRoles` | `GET /v1/roles?org_id=…` | auth | |
| `DeleteRole` | `DELETE /v1/roles/{id}` | platform_admin | ✓ |
| `AssignRole` | `POST /v1/role-assignments` | org_admin | ✓ |
| `RevokeRole` | `DELETE /v1/role-assignments` | org_admin | ✓ |
| `ListRoleAssignments` | `GET /v1/role-assignments?org_id=…&subject_id=…` | org_member | |
| `CheckPermission` | `POST /v1/permissions:check` | public (internal-only) | |

### OrganizationService

`CreateOrganization`, `GetOrganization`, `ListMembers`, `AddMember`, `RemoveMember` — all org-scoped, RLS-wrapped.

### AuthService organization exchange

`SwitchOrganization` is an authenticated, target-id-only token exchange. The
server locks the current device session using verified `sub` + `sid`, resolves
the target membership and roles from PostgreSQL, and returns a new access token
without rotating the refresh credential or changing device/lifetime state. The
frontend decodes the signed `org` claim into one global organization context;
tenant-scoped pages do not maintain independent organization filters.

### TeamService

`CreateTeam`, `ListTeams`, `AddMember`, `RemoveMember`, `ListMembers` — team_id-scoped operations resolve team→org via `WithBypass` first, then enter `WithOrgTx` for the actual write.

### BillingService public catalog

`ListPublicPlans` (`GET /v1/public/plans`) is a public, credential-free
projection of the authoritative server catalog. It exposes only presentation,
checkout eligibility, trial/tax terms, and entitlement limits, plus a
deterministic revision. The marketing service never copies pricing into
content and disables a plan CTA when the catalog says checkout is unavailable.

### Other domain services

`UserService`, `APIKeyService`, `AuditService`, `AuditExportService`, `InvitationService`, `WebhookService`, `BillingService`, `PlatformAdminService`, `UsageService`, `SSOAdminService`, `MFAService`, `ConsentService`, `NotificationService`, `OnboardingService`, `GDPRService`, `UserSettingsService`, `AuthService`, `IdentityService`.

## Usage metering

`UsageService.ConsumeUsage` is an internal-only protobuf API for trusted product
services. Each logical operation supplies an organization, canonical meter,
positive quantity, idempotency key, and optional event time/dimensions. The
service resolves the plan/override inside the same tenant transaction, locks the
monthly aggregate, and persists an immutable accepted or rejected receipt.
Retries return that receipt without incrementing again; reusing the key with a
different payload fails. `UsageService.GetUsage` is the authenticated tenant
read path. Periods are UTC calendar months and `period_end` is exclusive.

Seats and API-key counts remain computed cardinality gauges. They must not be
written as usage events because their authoritative rows can be reconciled
directly. Their admission checks are serialized with the authoritative write in
one tenant transaction; pending invitations reserve seats, while expired
invitations and expired or revoked API keys release capacity. New event meters
become active by adding their canonical key to the product plan/override
catalog; unknown keys resolve to a disabled limit.

The protobuf and storage contract is product-neutral. The current internal
gRPC listener is multiplexed onto the private REST h2c listener and is not a
module export. Cross-module product callers must use the generated named
internal endpoint tracked by `P1-NET-007`; exporting the mixed listener or
making ingestion public is not an acceptable integration shortcut. See
`module/USAGE_METERING.md` for the complete producer contract.

## Frontend admin pages

Every page lives under `frontend/code/src/app/admin/`:

| Route | Backend RPCs |
|---|---|
| `/admin/roles` | `ListRoles`, `CreateRole`, `DeleteRole` |
| `/admin/teams` | `ListTeams`, `CreateTeam`, `AddTeamMember`, `RemoveTeamMember`, `ListTeamMembers` |
| `/admin/organizations` | `GetOrganization`, `ListMembers`, `AddMember`, `RemoveMember`, **`AssignRole`/`RevokeRole`/`ListRoleAssignments` via the per-member "Manage roles" dialog** |
| `/admin/organizations/settings` | `GetOrgSettings`, `UpdateOrgSettings` |
| `/admin/invitations` | `CreateInvitation`, `ListInvitations`, `RevokeInvitation` |
| `/admin/api-keys` | `CreateAPIKey`, `ListAPIKeys`, `RevokeAPIKey` |
| `/admin/webhooks` | full webhook CRUD + `RotateSecret`, `TestWebhook` |
| `/admin/audit-log` | `QueryAuditLog` |
| `/admin/sso` | `GetOrgSSO`, `StartSSOSetup`, `DisableSSO` |
| `/admin/billing` | `OpenBillingPortal`, `ListInvoices` |
| `/admin/platform/admins` | platform role grant/revoke |

Client-side gating uses `<RoleGate>` (`src/components/auth/role-gate.tsx`) — display-only; backend remains authoritative.

## Composing this module into a workspace

A downstream workspace does not fork or copy saas-starter. It **composes** the
module: Codefly writes a copy into the consumer under `modules/<name>/`, records
where that copy came from, and enforces that the consumer only ever ADDS files
beside the base — never edits the base in place. All paths below are relative to
that composed module root (the `modules/<name>/` directory), the same root the
base-integrity tooling runs against.

### Two-step compose

```bash
# 1. Create the empty shell (no base files yet).
codefly add module --agent saas-starter <name>

# 2. Pull the base in from an immutable upstream tag. Dry-run is the default;
#    review the plan, then re-run with --apply.
codefly sync module <name> \
  --source https://github.com/codefly-dev/module-saas-starter.git \
  --to <tag> \
  --subdir module
codefly sync module <name> \
  --source https://github.com/codefly-dev/module-saas-starter.git \
  --to <tag> \
  --subdir module \
  --apply
```

`--subdir module` selects saas-starter's composed subtree (this repo ships the
module under `module/`, not at the repository root). Never compose with `rsync`,
a directory copy, or a hand-edited manifest — Codefly owns the transaction so
the result has reproducible provenance.

### The pin and the lock

`--to` takes an **immutable semantic-version tag**, never a branch or a moving
ref. On `--apply`, Codefly writes the consumer-owned lock `tools/base-source.json`
recording the repository, tag, peeled commit, and subdir. That file is the
consumer's provenance and belongs in its git history; sync never overwrites it
with anything but a newer applied pin.

Once the source is pinned, later updates need only the new tag — source and
subdir are read back from the lock:

```bash
codefly sync module <name> --to <newtag>            # dry-run
codefly sync module <name> --to <newtag> --apply
```

### Overlay discipline

Base files are **upstream-owned**. Every base file's `sha256` is recorded in
`tools/base-manifest.json`, shipped into the consumer at sync time. A consumer
composes by ADDING files on the side — a product plugin package, extra services,
integration tests — never by editing a base file in place. `codefly verify`
(and `node tools/base-integrity.mjs check`) re-hash every manifest file in the
consumer and fail on any drift; files absent from the manifest are legal
side-additions. Anything indispensable to the product — plugin entry points,
composition roots, contract tests — should be listed under `requiredAdditions`
in `tools/base-integrity-allow.json` so verify fails if an update ever leaves
one missing.

### When a base file genuinely must change

In-place edits fail closed. Resolve the conflict deliberately, preferring the
option earliest in this list:

1. **Promote it upstream (preferred).** Land the change in canonical
   saas-starter, cut a new tag, and `sync --to` it down. The base gets stronger
   and every consumer benefits.
2. **Accept the upstream version.** When sync reports a base file you diverged
   on and you want canonical's copy, pin it with `--accept-upstream <path>` to
   discard the local edit for that path. There is no flag that forces a
   consumer edit back over upstream — the base only moves via a new tag.
3. **Whitelist the divergence (last resort).** Add the path to
   `tools/base-integrity-allow.json` with a human-readable reason. This is
   logged loudly on every check and is tech debt — prefer a config seam or a
   side-module.

### Composing a subset of services

A consumer may compose only some of the module's services. The composed set is
the `services:` list in the consumer's `module.codefly.yaml`. `check` skips
manifest files that belong to a non-composed service and reports them as an
expected omission, not a missing base file — module-level files are always
enforced. So absent base files for a service you never composed are normal;
missing base files for a service you DID compose are a real failure.

For the frontend product-plugin overlay specifically — package layout,
generated projections, and gates — see
[docs/frontend-plugin-installation.md](./module/docs/frontend-plugin-installation.md).

## How to extend the module

### Adding a new per-tenant table

1. Write the migration (`store/migrations/N_create_X.up.sql`) with `org_id NOT NULL REFERENCES organizations(id) ON DELETE CASCADE`.
2. In a follow-up migration, enable RLS:
   ```sql
   ALTER TABLE X ENABLE ROW LEVEL SECURITY;
   ALTER TABLE X FORCE  ROW LEVEL SECURITY;
   CREATE POLICY X_tenant ON X
     USING (org_id::text = current_setting('app.current_org_id', true))
     WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
   ```
3. Add a `Store` method in `pkg/business/store.go`.
4. Implement in `pkg/infra/postgres_X.go`. Use `s.getQueryExecutor(ctx)` so context-scoped tx (from `WithOrgTx`) is reused.
5. Wrap every Service-layer call site in `s.store.WithOrgTx(ctx, orgID, ...)`.
   Cross-tenant workers require a dedicated least-privilege worker role and
   pool; do not add an application-settable bypass branch to a new policy.
6. Add a cross-tenant blocking test in `pkg/business/rls_X_test.go` mirroring `rls_audit_export_test.go`.
7. Add the table to `pkg/business/introspection.go:serviceRLSTables` so it shows up in `GetServiceInfo`.

### Adding a new RPC

1. Add the message + RPC to its bounded-context file under `proto/saas/accounts/v1`. Add a complete `option (saas.policy.v1.method_policy)`; missing or `UNSPECIFIED` policy is denied and fails tests. Annotate `google.api.http` only when REST exposure is intended.
2. From the accounts service directory, run `codefly generate proto --proto ./proto --output . --local --template buf.gen.local.yaml` (NEVER run `buf generate` directly). The local template invokes exact-version Go plugins and the lockfile-pinned TypeScript plugin, regenerating Go, gRPC, Connect, gateway, OpenAPI, and modular TypeScript outputs together without depending on BSR plugin availability.
3. Import browser types from their bounded module, for example `@/gen/saas/accounts/v1/teams_pb`; do not restore the former monolithic TypeScript barrel.
4. Implement: `pkg/business/<feature>.go` (the Service method, with `WithOrgTx`/`WithBypass` wrap), `pkg/infra/postgres_<feature>.go` (raw SQL), `pkg/adapters/rpcs.go` or a new `<feature>_rpcs.go` (handler authz + Validate + Service call), and a Connect adapter. Keep any still-manual gRPC/REST implementation wiring current.
5. For a new service only, add its finite implementation source to `pkg/adapters/connect_bindings.yaml`; never hand-register a Connect service or procedure. If it opts into REST, also classify the service as `generated` or `plugin` in `pkg/adapters/rest_bindings.yaml`. Existing services need no binding change when an RPC is added.
6. Add only the editorial summary to `pkg/business/introspection.go:rpcDescriptions`, then run `go generate ./pkg/business`, `go generate ./pkg/adapters`, and `go generate ./pkg/cataloggen`. This refreshes the normalized catalog, authorization catalog/matrix, auth-sidecar policy lookup, Connect registration, REST registration/allowlists, filtered OpenAPI, and target-neutral gateway route artifacts. HTTP, authz, scopes, resource bindings, MFA, audit, rate, and sensitivity must come from descriptors; do not introduce another policy map or service list.
7. Frontend: a `useX` hook in `src/features/<feature>/service/{queries,mutations}.ts`, called from a UI in `src/features/<feature>/ui/`.

### Adding a new RBAC permission

1. Pick a `resource:action` pair. Add it to `pkg/business/introspection.go:servicePermissions` with description + which built-ins hold it.
2. Update `migrations/4_create_roles_permissions.up.sql` (or a follow-up migration) to grant it to the relevant built-in role.
3. If the FE displays a permission matrix, add the pair to `frontend/code/src/lib/permissions.ts`.

### Common pitfalls

- **Forgetting `WithOrgTx`** on a per-tenant table → fail-closed (zero rows). Loud test failures, not silent leaks. The `BeforeAcquire SET ROLE app_tenant` hook makes this fail-closed by default.
- **Using `pgx.BeginTxFunc` inside a Store method** → opens a fresh pool tx that ignores the WithOrgTx context. Always use `s.getQueryExecutor(ctx)` instead. See `postgres_org.go:CreateOrganization` and `postgres_permissions.go:CreateRole` for the context-tx-reuse pattern.
- **Test fakes that embed `business.Store` but don't override `WithOrgTx`/`WithBypass`** → nil panic. Add pass-through implementations (see `sso_admin_test.go`).
- **Custom-role assignment without UI** — Use the `<ManageMemberRolesDialog>` shipped at `src/features/roles/ui/manage-member-roles-dialog.tsx`. Don't bypass via direct SQL.
- **Assigning a built-in role to a user in a NEW org** — `RegisterUser` only auto-assigns to the resolver-bootstrapped personal org. Explicit orgs need an explicit `AssignRole` call.

## Tests

```
pkg/business/rls_*_test.go              — 22 RLS cross-tenant blocking tests
pkg/business/module_test.go              — capabilities introspection smoke
pkg/infra/tenant_tx_test.go              — empty-orgID guard
```

Run from `module/services/accounts`:
`codefly test service --target ./pkg/business --filter TestRLS`.
Codefly owns the dependency graph and Docker lifecycle.

## Compatibility

- api service version: see `pkg/business/introspection.go:ServiceVersion` (`0.2.0` at time of writing). Module-level versioning is owned by `module.codefly.yaml`, not by any single service.

## Known issues

### Codefly daemon flake when tests run in parallel

`go test ./...` (the default `-p N` parallel mode) occasionally fails with:

```
WithDependencies failed: sdk.SetEnvironment: failed to get dependencies network mappings:
  rpc error: code = Unavailable desc = error reading from server: EOF
```

Cause: each test package (`pkg/business`, `pkg/infra`, `pkg/auth/pg`, `pkg/billing/pg`) calls `sdk.WithDependencies` which spawns the codefly CLI; under parallel mode several of those races on the codefly daemon's gRPC socket. The naming-scope mechanism gives each test its own DB/cache/vault, but daemon connection races still happen.

**Workaround:** run sequentially:

```
go test -p 1 -count=1 ./pkg/business ./pkg/infra ./pkg/adapters
```

Each package passes individually; the flake only appears when several processes start at once. Tracked upstream in `codefly-dev/core` — when the daemon serializes connection setup this issue goes away.

### LSP shows phantom `go mod tidy` errors on generated `pb.gw.go` files

Editor diagnostics may flag `github.com/oklog/run`, `go.opentelemetry.io/proto/otlp`, etc. as "not in your go.mod file" on generated grpc-gateway files. Those deps ARE present (`go build ./...` succeeds — verify with `cd module/services/api/code && go build ./...`). The diagnostics are stale LSP cache from a previous proto-gen iteration; they clear after restarting `gopls` or running `go mod tidy` in the api/code dir. No real build issue.
- Bump on proto-breaking changes (renamed RPCs, removed fields).
- Compatible-additive (new RPC, new field, new built-in role): patch-bump.
