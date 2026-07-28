# saas-starter — Module Reference

Multi-tenant SaaS backend with three-layer authorization (handler gates + RBAC
+ Postgres RLS), a Next.js authenticated product, a separately deployable
public marketing site, and per-service introspection endpoints.

A codefly **module** is a collection of **services**; each service owns its own proto. The accounts service of saas-starter exposes a self-describing catalog of its OWN RPCs / RBAC vocabulary / RLS-protected tables / scopes. A "module-level" view = aggregation across every service's catalog and is a separate concern (CLI / gateway / Mind aggregator) — not encoded in any single proto.

## Quick links

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
marketing service                         auth-sidecar
  ├─ repository content                     ↓
  ├─ public plan projection                product frontend
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
6. Add only the editorial summary to `pkg/business/introspection.go:rpcDescriptions`, then run `go generate ./pkg/business`, `go generate ./pkg/adapters`, and `go generate ./pkg/cataloggen`. This refreshes the normalized catalog, authorization catalog/matrix, auth-sidecar policy lookup, Connect registration, REST registration/allowlists, filtered OpenAPI, and gateway/Envoy/Istio route artifacts. HTTP, authz, scopes, resource bindings, MFA, audit, rate, and sensitivity must come from descriptors; do not introduce another policy map or service list.
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

Run: `go test -count=1 -run TestRLS ./pkg/business`. Requires Docker + `codefly run service api --headless`.

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
