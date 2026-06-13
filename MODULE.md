# saas-starter — Module Reference

Multi-tenant SaaS backend with three-layer authorization (handler gates + RBAC + Postgres RLS), a Next.js admin frontend, and per-service introspection endpoints.

A codefly **module** is a collection of **services**; each service owns its own proto. The api service of saas-starter exposes a self-describing catalog of its OWN RPCs / RBAC vocabulary / RLS-protected tables / scopes. A "module-level" view = aggregation across every service's catalog and is a separate concern (CLI / gateway / Mind aggregator) — not encoded in any single proto.

## Quick links

- api service introspection (after `codefly run`): `GET /v1/.well-known/service-info`
- Connect-RPC: `customers.IntrospectionService/GetServiceInfo`
- Code: `pkg/business/introspection.go`
- Test: `pkg/business/introspection_test.go`
- Source-of-truth proto: `module/services/api/proto/api.proto` (api service owns its proto; other services own theirs)

## Architecture

```
Browser
  ↓ Connect-ES (TS, generated from buf)
Gateway (Envoy/KrakenD) — merges per-service REST endpoints
  ↓ Bearer JWT or X-API-Key, plus X-Scopes
auth-sidecar — validates token, stamps x-user-id / x-org-id metadata
  ↓ gRPC
api service
  ├─ adapters/ — gRPC + Connect + REST gateway servers
  ├─ business/ — Service methods (RBAC + RLS wrappers go here)
  └─ infra/    — Store impl (Postgres + Redis + Vault)
       ↓
     Postgres pool with BeforeAcquire SET ROLE app_tenant
     ↓
     RLS-enforced reads / writes
```

## Three layers of authorization

| Layer | Where | What it asserts |
|---|---|---|
| **L1** Handler gates | `adapters/auth.go`, `adapters/connect_auth_interceptor.go`, `adapters/rpcs.go` | "Should this caller be ALLOWED to invoke this RPC?" — `requireAuth` / `requireOrgMember` / `requireOrgAdmin` / `requirePlatformAdmin` / `requireMFA` / `requireScope`, plus rate-limiting. |
| **L2** RBAC | `business/service.go:CheckPermission` (+ Postgres `roles` / `role_permissions` / `role_assignments`) | "Does this caller have THIS capability for THIS resource?" — wildcard support (`*:*`, `users:*`, `*:read`), team inheritance. |
| **L3** RLS | Postgres `ROW LEVEL SECURITY` + `WithOrgTx` / `WithBypass` | "Even if L1+L2 said yes, does the row physically belong to the caller's tenant?" — fail-closed via `BeforeAcquire SET ROLE app_tenant`. |

A bug in any one layer is caught by the others. See `AUTHZ.md` for the deep dive.

## RLS — protected tables

16 tables, every per-tenant table covered. Source of truth: `migrations/` 23 (Phase 1) → 33 (Phase 2F).

| Table | Policy | Notes |
|---|---|---|
| `audit_export_configs` | direct `org_id` | Phase 1 |
| `webhook_subscriptions` | direct `org_id` | Phase 2A |
| `webhook_deliveries` | JOIN via `subscription_id` | Phase 2A |
| `api_keys` | direct `organization_id` | Phase 2A |
| `org_settings`, `invitations`, `organization_members`, `subscriptions`, `entitlement_overrides`, `usage_records` | direct `org_id` | Phase 2B |
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

The full machine-readable list is auto-derived from gRPC service descriptors at runtime, joined with hand-maintained metadata in `pkg/business/introspection.go:rpcMetadata`, and served at `GET /v1/.well-known/service-info`. Highlights:

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

### TeamService

`CreateTeam`, `ListTeams`, `AddMember`, `RemoveMember`, `ListMembers` — team_id-scoped operations resolve team→org via `WithBypass` first, then enter `WithOrgTx` for the actual write.

### Other domain services

`UserService`, `APIKeyService`, `AuditService`, `AuditExportService`, `InvitationService`, `WebhookService`, `BillingService`, `PlatformAdminService`, `SSOAdminService`, `MFAService`, `ConsentService`, `NotificationService`, `OnboardingService`, `GDPRService`, `UserSettingsService`, `AuthService`, `IdentityService`.

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
     USING (org_id::text = current_setting('app.current_org_id', true)
            OR current_setting('app.bypass', true) = '1')
     WITH CHECK (...same...);
   ```
3. Add a `Store` method in `pkg/business/store.go`.
4. Implement in `pkg/infra/postgres_X.go`. Use `s.getQueryExecutor(ctx)` so context-scoped tx (from `WithOrgTx`) is reused.
5. Wrap every Service-layer call site in `s.store.WithOrgTx(ctx, orgID, ...)`. Cross-tenant scans (background workers) use `WithBypass`.
6. Add a cross-tenant blocking test in `pkg/business/rls_X_test.go` mirroring `rls_audit_export_test.go`.
7. Add the table to `pkg/business/introspection.go:serviceRLSTables` so it shows up in `GetServiceInfo`.

### Adding a new RPC

1. Add message + RPC to `proto/api.proto`. Annotate the HTTP route via `option (google.api.http)`.
2. Run `codefly generate proto --proto ./proto --output ./code/pkg/gen` (NEVER run `buf generate` directly — see `CLAUDE.md`).
3. Run `codefly generate gRPC --service saas-starter/api --language ts --destination <frontend>/src/gen` to refresh the TS client.
4. Implement: `pkg/business/<feature>.go` (the Service method, with `WithOrgTx`/`WithBypass` wrap), `pkg/infra/postgres_<feature>.go` (raw SQL), `pkg/adapters/rpcs.go` or a new `<feature>_rpcs.go` (handler authz + Validate + Service call), `pkg/adapters/connect_handlers.go` (Connect adapter), and registration in `grpc_gen.go` / `connect_gen.go` / `rest_gen.go`.
5. Add metadata to `pkg/business/introspection.go:rpcMetadata` (the RPC list itself is auto-derived from descriptors; this map carries handler authz tier, scopes, audit-emit, and HTTP route).
6. Frontend: a `useX` hook in `src/features/<feature>/service/{queries,mutations}.ts`, called from a UI in `src/features/<feature>/ui/`.

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

- api service version: see `pkg/business/introspection.go:ServiceVersion` (`0.1.0` at time of writing). Module-level versioning is owned by `module.codefly.yaml`, not by any single service.

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
