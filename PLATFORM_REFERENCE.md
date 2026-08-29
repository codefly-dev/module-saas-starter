# Platform functionality reference — lessons mapped to this starter

Provenance: this document distills an external audit of a production
multi-tenant SaaS platform (~4 years of admin/RBAC/auth/audit/tenant/S2S code
on GCP + Python/TypeScript) into a checklist of platform-side functionality,
a cloud-coupling scorecard, and testing lessons — and maps each item to what
**this** Go/Postgres/Codefly starter already ships, partially ships, or has
not built. It exists so the starter can adopt the proven patterns and skip the
recorded mistakes without re-deriving them. It is a **reference**, not a plan:
the executable backlog lives in [ROADMAP.md](./ROADMAP.md) and
[TODO.md](./TODO.md).

The audited platform is a *different* codebase (GCP, FastAPI, Firestore). Its
lessons transfer; its mechanisms do not. Where this starter reaches the same
goal by a different mechanism (Postgres RLS instead of a directory port,
append-only `audit_events` instead of stdout JSON), that is called out as a
**deliberate divergence**, not a gap.

## How to read the status markers

- **✅ Adopted** — implemented in shipped code, corroborated by the accounts
  service and the authoritative doc cited.
- **🟡 Partial** — implemented but narrower than the audited scope, or shipped
  for part of the surface with the rest still design-stage.
- **⬜ Gap** — not present, or present only as a forward-looking proposal.
- **↔ Divergent by design** — the goal is met, but by a mechanism the starter
  chose over the audited one; not a gap.

Trust level of the cited docs matters. [PRODUCTION_READY.md](./PRODUCTION_READY.md),
[AUTHZ.md](./AUTHZ.md), [module/DATABASE_AUTHORITY.md](./module/DATABASE_AUTHORITY.md),
[module/METHOD_POLICY.md](./module/METHOD_POLICY.md),
[module/AUTHORIZATION_CATALOG.md](./module/AUTHORIZATION_CATALOG.md),
[module/DEPLOYMENT_TOPOLOGY.md](./module/DEPLOYMENT_TOPOLOGY.md), and
`docs/authorization/9-reference/current-state.md` describe **shipped** behavior.
`module/docs/IDENTITY_ACCESS_PLAN.md` and [APPROVALS_DESIGN.md](./APPROVALS_DESIGN.md)
and [FRONTEND_ARCHITECTURE.md](./FRONTEND_ARCHITECTURE.md) are **forward-looking**
and are not evidence of current implementation.

---

## 1. Functionality catalog — mapped to the starter

### 1.1 Identity & sessions

| Capability | Status | Starter mechanism & reference |
|---|---|---|
| Hosted-IdP login (sign-in/up/callback, throttling, login audit) | ✅ | WorkOS / Auth0 / Google / generic OIDC validated once at `Authenticate`; provider token never enters the runtime path. `pkg/auth/{workos,oidc}`, [PRODUCTION_READY.md](./PRODUCTION_READY.md), [module/FEATURES.md](./module/FEATURES.md) |
| Self-minted session JWT + own JWKS | ✅ | Backend mints an Ed25519 session JWT (`sub/org/or/pr/sid`); the sidecar verifies it (signature, claims, revocation) but never mints. `GET /v1/auth/.well-known/jwks.json` (`pkg/adapters/jwks_http.go`), Vault-held current+previous keypair for rotation. This is the "own session JWT is the primary path" escape hatch the audit recommends. |
| JIT provisioning | ✅ | `pkg/auth/pg/resolver.go` upserts `(provider, sub)` → `user_identities`/`users` in one tx, plus `BOOTSTRAP_ADMIN_EMAIL` super-admin bootstrap. |
| Session revocation | ✅ ↔ | The audit's platform left this permanently stubbed (501). This starter **closed that gap**: refresh-rotation with reuse detection, plus migration-70 DB triggers that atomically revoke affected sessions on user-status / membership / role / MFA change, and migration-71 device-cap eviction (`sr` claim, [AUTHZ.md](./AUTHZ.md), [module/DATABASE_AUTHORITY.md](./module/DATABASE_AUTHORITY.md)). |
| Enterprise SSO / per-tenant `authMode` | 🟡 | Per-org IdP directory (`org_identity_providers`, RLS migration 92), pre-auth domain/host→provider discovery (`pkg/business/identity_discovery.go`), and WorkOS SSO setup/disable (`pkg/business/sso_admin.go`, `/admin/sso`). No shipped `authMode`/"require-SSO" enforcement flag; the login/invite/signup split that would make it explicit is proposal-only (`module/docs/IDENTITY_ACCESS_PLAN.md`). |

**Adopt/keep:** session revocation and self-minted sessions are already the
audit's recommended shape — hold that line.

### 1.2 Authorization

The audit's single most important design point is **role-layer separation**.
The starter separates *enforcement* layers and *role* dimensions differently
from the audited platform, so read the mapping carefully.

- **Enforcement layers (defense-in-depth), ✅:** the starter runs three
  orthogonal checks — L1 handler policy gates (`requireAuth` / `requireOrgMember`
  / `requireOrgAdmin` / `requirePlatformAdmin` / `requireMFA` / `requireScope`),
  L2 RBAC `resource:action` permissions with wildcards + team inheritance, and
  L3 Postgres RLS (`SET ROLE app_tenant` via `WithOrgTx`). This is the analog of
  the audit's `require_permission(...)` dependency, hardened with a database
  backstop. See [AUTHZ.md](./AUTHZ.md), [MODULE.md](./MODULE.md).
- **Role dimensions, 🟡:** the starter has a **tenant org role**
  (`owner`/`admin`/`member`, `X-Org-Role`) and a **platform/operator role**
  (`super_admin`/`billing`/`support`, `X-Platform-Role`), plus RBAC grants. It
  does **not** ship the audit's third dimension — a per-`(member, product)`
  **solution role** vocabulary (`administrator`/`approver`/`analyst`/…), nor an
  **org access type** (`standard`/`internal`/`delegated`). Impersonation here is
  gated by the platform `support` role, not by an org-level access type (§1.6).
  If the starter grows multiple products, the solution-role dimension is the one
  to add.
- **Canonical policy vocabulary, ✅ ↔:** rather than one JSON file symlinked
  across two runtimes, the starter's vocabulary is `saas.policy.v1.MethodPolicy`
  (proto extension 51000) projected to `generated/authz-methods.json` for every
  method, with a fail-closed compiler. Missing/`UNSPECIFIED` policy fails
  generation and denies at runtime. Same "one source of truth, no drift" goal;
  proto+codegen instead of JSON+symlink. [module/AUTHORIZATION_CATALOG.md](./module/AUTHORIZATION_CATALOG.md),
  [module/METHOD_POLICY.md](./module/METHOD_POLICY.md).
- **Enforce-by-default, ✅:** the audit's headline mistake was `RBAC_MODE`
  defaulting to shadow and *failing open*. The starter has **no shadow mode** —
  it is deny-by-default, always-enforce. That is the audit's recommendation
  already in force; do not add a fail-open shadow toggle without a boot-validated
  enum.
- **Agent/machine principals, ✅:** `principals` table with kinds
  `human`/`service`/`agent` (`pkg/business/principals.go`); agents are
  human-owned (`publisher/name:version`); `SubjectKind_PRINCIPAL` is canonical.
- **Denial auditing, 🟡:** audit emission is descriptor-declared
  (`AUDIT_EMISSION_SUCCESS_AND_FAILURE`), so per-method failures are audited;
  there is no single global "every denial" sink as the audit describes.

### 1.3 Service-to-service auth (two-token pattern) — ✅

- **Two tokens:** (a) a service-identity token (`CODEFLY_INTERNAL_TOKEN`,
  `x-codefly-internal-token`) admitting `EXPOSURE_INTERNAL` RPCs, distinct from
  the gateway-provenance token; (b) an on-behalf-of **Work Context** capability
  carrying an attenuating, audience-bound, multi-hop actor chain
  (`WorkContextService.StartRootSession/StartTask/StartChildSession/ExchangeAudience`).
  `pkg/authzclient/client.go`, `pkg/business/work_context_authority.go`,
  [module/services/accounts/TRUST_BOUNDARY.md](./module/services/accounts/TRUST_BOUNDARY.md).
- **OBO minting authority:** Work Context authority mints attenuating
  capabilities and journals each hop into `actor_chain_journal` (durable,
  hash-chained; revocation bumps `authorization_revision`). This is the analog
  of the audit's token-exchange service and its ceiling-snapshot auditor.
- **System-route declaration, ✅ ↔:** `EXPOSURE_INTERNAL` in the method policy
  is the in-code declaration, enforced by `requireInternalCredential` and by a
  generated Istio `deny-<service>-internal-authority` policy — the analog of
  `system_authorized(kind, reason)` plus its boot-time checker.

### 1.4 Tenant model & provisioning

| Capability | Status | Notes |
|---|---|---|
| Tenant directory + members | ✅ | `organizations`, `organization_members`, `teams`/`team_members`, all RLS-scoped; `OrganizationService`/`TeamService` CRUD; `/admin/organizations`, `/admin/teams`. Postgres-first — the audit explicitly regrets Firestore-first, so this is the recommended shape. |
| Per-product enablement flags (`provisioned`/`visible`) | 🟡 | Entitlement/plan gates (`entitlement_overrides`, `plans`, `plan_entitlements`) answer "is this org allowed this capability"; runtime rollout moved to Unleash (`feature-flags@1`), and the DB `feature_flags` table is retired/read-only ([MODULE.md](./MODULE.md)). No generic per-product provisioned/visible matrix beyond entitlements. |
| Tenant-lifecycle admin UI | 🟡 | Org settings + member management ship; ownership transfer / suspension / deletion are roadmap P4.1 ([ROADMAP.md](./ROADMAP.md)). The audit calls the *absence* of any lifecycle UI untenable past ~5 tenants — the starter is ahead here but not complete. |

### 1.5 The admin console

Gated by tenant-admin or platform role (`src/components/auth/role-gate.tsx`).

| Feature | Status | Notes |
|---|---|---|
| User management (invite/edit/remove, last-admin guard) | ✅ | `/admin/users`, `/admin/invitations`; last-owner/last-admin guards are handler-layer state-dependent checks ([module/METHOD_POLICY.md](./module/METHOD_POLICY.md)). |
| Roles page / matrix | ✅ | `/admin/roles` + `ManageMemberRolesDialog`; FE matrix `src/lib/permissions.ts`. Code-defined, PR-reviewable — matches the audit's "roles are code, not DB rows". |
| Connector management | 🟡 | SSO connector setup only (`/admin/sso`); no general third-party-connector/manifest framework. |
| Per-tenant config | ✅ | `/admin/organizations/settings` (`org_settings`), `/admin/entitlements`. |
| Background-agent ops / kill switch | 🟡 | `/admin/platform/jobs` gives payload-free queue/lifecycle + MFA-gated dead-letter replay; `RevokePrincipal` revokes an agent. No dedicated per-tenant agent kill switch — but note the audit's lesson: make any kill switch **per-tenant**, not deployment-global. |
| Partner API keys | 🟡 | Org API keys `cfly_sk_…` (hashed via Vault transit HMAC, scoped, `/admin/api-keys`, `pkg/business/api_keys.go` + `pkg/infra/vault.go`); no separate partner tier and no 404-not-403 concealment of a staff-only surface yet. |
| Audit viewer + compliance export | ✅ | `/admin/audit-log` (`QueryAuditLog`) + on-demand CSV/JSON download (`AuditService/ExportAuditLog`, `pkg/business/audit_export.go`). The former per-org S3 JSONL sink (`AuditExportService`) was removed with the object-storage service. |

### 1.6 Support access / impersonation — ✅ (with a role-model divergence)

- **Authority-stripped, ✅:** the minted impersonation token carries
  `ActingAsUserID` but an **empty PlatformRole** — an explicit fix against a
  support→super_admin escalation — and grants only the target org's context.
  This is server-enforced, matching the audit's core lesson that read-only
  support must be authority-stripped server-side, not cosmetic.
  `pkg/business/platform_admin.go` (`ImpersonateUser`),
  [PRODUCTION_READY.md](./PRODUCTION_READY.md) decisions 7–8.
- **Audited, ✅:** emits `platform.user_impersonated`; `RevokeSession` /
  `ListActiveSessions` are support+.
- **Per-request re-validation, ✅:** the impersonation token is a normal
  L1/L2/L3-checked runtime identity each request; it is a signed 15-minute
  snapshot rather than a re-read-every-request cookie.
- **Divergence ↔:** capability derives from the platform `support` role, not
  from an org **access type** (`staff`/`delegated`) as the audit recommends. The
  audit's access-type model is worth considering if delegated/contractor access
  ever needs an explicit per-org allowlist. A user-visible impersonation banner
  is roadmap P4.4.

### 1.7 The BFF / proxy perimeter — ✅

- The Next.js same-origin proxy strips caller-supplied trust headers, stamps the
  real origin with `CODEFLY_INTERNAL_TOKEN`, and forwards only API routes to the
  private `auth-sidecar`; accounts is never publicly reachable. The gateway/
  sidecar strips all client-supplied identity/org/role/scope/MFA headers before
  auth, then emits canonical `X-User-ID` / `X-Org-ID` / `X-Org-Role` /
  `X-Platform-Role` / `X-Session-ID` (+ signed `amr`/`auth_time`/`acr`) plus a
  gateway token that accounts validates constant-time.
  [module/services/accounts/TRUST_BOUNDARY.md](./module/services/accounts/TRUST_BOUNDARY.md),
  [PRODUCTION_READY.md](./PRODUCTION_READY.md). This is exactly the audit's
  "portal owns identity, services trust only attested headers" split.

### 1.8 Audit & compliance pipeline — ✅ ↔ (with Partial read-auditing)

- **Sink is Postgres, not stdout ↔:** the audit recommends audit→OTel→stdout
  JSON as the portable seam. The starter instead uses an append-only Postgres
  `audit_events` table (RLS, polymorphic org scope) with a **typed event
  registry** (`pkg/business/audit_registry.go`,
  `module/docs/adr/0003-typed-audit-event-registry.md`) and on-demand CSV/JSON
  export (`ExportAuditLog`). The
  typed registry buys compile-time discipline the stdout seam does not; the
  trade-off (a DB write on the hot path vs. a log line) is deliberate. Weigh the
  audit's synchronous-stdout argument if audit-write latency ever bites.
- **Default-deny coverage gates, ✅:** the audit's most-praised idea — structural
  CI gates over review discipline — is present. `module/tools/authz-coverage-gate.mjs`
  enforces RBAC + audit coverage and rejects permission-broadening without
  approval; `module/tools/rls-migration-gate.mjs` enforces tenant RLS coverage;
  the sidecar header-lockstep invariant runs in CI. See §5.
- **Delegation chain, ✅:** `actor_chain_journal` is append-only, hash-chained,
  with immutable UPDATE/DELETE triggers and revision-gated revocation — the
  analog of the audit's `delegation_jti` + `caused_by` chain + ceiling-snapshot
  hash.
- **Retention/redaction, 🟡:** `RunRetention` deletes per
  `data_retention_policies`; export payloads are PII-redacted. The audit's named
  two-tier (7-year content-free / 90-day full) split is not a distinct construct.
- **Read auditing, ⬜:** bulk reads are RLS-scoped but not themselves logged as
  audit events. The audit's "audit the access set (IDs, capped, dedupe window)"
  pattern — and its 11k-rows-from-a-poller cautionary tale — is unbuilt.

### 1.9 Control plane for agents/automation — 🟡

- Durable jobs (inbox/outbox, leased workers, DLQ, MFA-gated replay) ship
  ([module/JOBS.md](./module/JOBS.md)), and principal/actor-chain machinery
  exists (§1.3). A full capability registry with `/admin/*` `/*` `/external/*`
  trust doors and a pub/sub event orchestrator is not built. The
  [APPROVALS_DESIGN.md](./APPROVALS_DESIGN.md) primitive (§Approvals below) is the
  nearest in-flight control-plane work.

### 1.10 Observability / evals — ⬜ (out of scope for the starter)

OTLP-native telemetry ships (SigNoz), but per-tenant trace ingestion, trace
explorer, annotation queues, and online scoring are AI-product features, not
starter kernel. Keep the transferable lesson on file: **owner-scoped reads
behind a single widening permission, guarded by an auto-discovering leak test.**

---

## 2. Cloud coupling — the starter's portability model

The audited platform enforced portability with a **port registry**, a
**seam-integrity guard** that failed CI on any raw cloud-SDK touch outside
declared adapter zones, and a **`CLOUD_PROVIDER` read-at-call-time factory** that
fails loud on unknown values. This starter reaches portability differently, and
the difference is worth stating plainly so no one assumes a factory that isn't
there.

- **No `CLOUD_PROVIDER` factory / port registry (⬜ as named).** There is no
  provider-factory abstraction in code. Portability comes from **Codefly SDK
  endpoint resolution + generated deployment topology + a kustomize `overlays/aws`
  patch** that swaps stateful services for managed RDS / ElastiCache / external
  Vault ([module/DEPLOYMENT_TOPOLOGY.md](./module/DEPLOYMENT_TOPOLOGY.md)).
- **The seam-integrity analog exists (↔).** The **base-integrity manifest guard**
  (`module/tools/base-integrity.mjs`, CI `verify`) plus the SDK-boundary CI job
  ("reject direct Codefly runtime-carrier access") play the seam-guard role at
  the module boundary rather than at each cloud SDK call.
- **Postgres-first everywhere (✅).** Identity, sessions, RBAC, orgs/teams, API
  keys, invitations, `audit_events`, entitlements/subscriptions, usage meters,
  webhooks, jobs, delegation + actor chain, and approvals scaffolding are all
  Postgres under RLS — precisely the Postgres-first choice the audit says it
  regrets not making. Redis (cache/rate-limit) and Vault (keys/secrets) are the
  other backing stores; there is no object-storage dependency.

### Portability scorecard (starter-relative)

| Capability | Audited status | Starter status |
|---|---|---|
| Workload / service identity | Port + adapters | ✅ Codefly internal + gateway tokens; Istio reach policy generated from `authz-methods.json` |
| Token signing | Port + KMS/local | ✅ Ed25519 via Vault-held keypair, rotation with previous-key overlap |
| Member/tenant directory | Firestore/Dynamo port | ✅ ↔ Postgres + RLS (no port abstraction; single first-class store) |
| Object storage | GCS/S3 port | ❌ none — the audit-export S3 sink was removed; no object-storage surface remains |
| Audit **emission** | stdout JSON | ↔ Postgres `audit_events` + typed registry (see §1.8) |
| Audit/analytics **query** | Hand-written BigQuery SQL (❌ lock-in) | ✅ SQL over the same Postgres — no warehouse dialect to lock into |
| Document/DB store | Firestore-first (❌) | ✅ Postgres-first |
| Eventing | Pub/Sub only (❌) | 🟡 Durable Postgres jobs (inbox/outbox); no external broker adapter |
| Scheduling + leases + idempotency | Zero abstraction (❌) | ✅ Leased workers + idempotency in the jobs layer ([module/JOBS.md](./module/JOBS.md)) |
| Consumer IdP | Swappable in practice (⚠️) | ✅ Multi-provider adapters + own session JWT as the primary path |
| LLM access | Provider port + capability flags | ⬜ Not a starter concern |

**Honest caveat, unchanged from the audit:** adapters ≠ a deployed second cloud.
The AWS overlay is a kustomize patch, not a live, applied deployment — treat it
as designed, not proven, until exercised end to end.

---

## 3. Patterns already adopted (keep them)

1. **One source of truth, generated into every consumer** — proto `MethodPolicy`
   → `authz-methods.json` → sidecar + gateway + REST, drift-checked in CI. The
   starter's analog of "one policy file, two runtimes".
2. **Enforce-by-default, deny-by-default** — no fail-open shadow mode; missing
   policy fails generation. This is the audit's #1 mistake pre-avoided.
3. **Structural CI gates over review discipline** — authz/audit coverage, RLS
   coverage, permission no-broadening, header lockstep, base-integrity (§5).
4. **Two-token S2S** with attenuating, audience-bound, hash-chained actor
   capabilities that can only narrow across hops.
5. **Impersonation authority-stripped server-side** (empty platform role in the
   minted token), audited, per-request-checked.
6. **BFF-as-perimeter** — zero-trust header stripping + server-constructed trust
   headers; accounts never publicly reachable.
7. **Session revocation shipped** (DB-trigger driven), closing the audit's most
   glaring stub.
8. **Postgres-first under RLS** — the store choice the audit wishes it had made.

## 4. Mistakes the starter should keep avoiding

Drawn from the audit's "what's bad" list, filtered to what applies here:

1. **Don't add a fail-open shadow mode.** If a migration ever needs one,
   boot-validate the enum and refuse to start on unknown values.
2. **Make any background-automation kill switch per-tenant**, never
   deployment-global (§1.5).
3. **Keep read-only/support authority server-enforced**, never cosmetic — the
   starter does this today; regressions here are silent and dangerous.
4. **Keep one products/capability registry.** Avoid a second "valid products"
   allowlist diverging from the policy vocabulary.
5. **Generate policy docs from the policy file.** [module/AUTHORIZATION_CATALOG.md](./module/AUTHORIZATION_CATALOG.md)
   and `AUTHZ_MATRIX.md` are generated — keep them so; hand-written permission
   docs drift.
6. **Scope structural guards to the whole tree, allowlist out.** The audit's
   seam guard rotted because it covered only 4 of ~24 service trees.

## 5. Testing — mapped to the starter's gates

The audit's overall finding — *structural gates beat coverage percentages, and a
small security core deserves 2–3:1 test:src at zero skips* — is the model this
starter already follows.

- **Structural gates shipped (✅):** `authz-coverage-gate.mjs` (RBAC + audit
  coverage, permission no-broadening), `rls-migration-gate.mjs` (tenant RLS
  coverage), sidecar header-lockstep, base-integrity manifest, and clean-diff
  checks on every generated catalog. Each gate has its own `--test` suite in CI
  (`.github/workflows/ci.yml`) — the audit's "test the guards themselves".
- **Security-core tests (✅):** `pkg/business/rls_*_test.go` prove cross-tenant
  blocking; `pkg/infra/*_test.go` cover grants/roles/policies; per-persona
  table-driven authorization tests. FE: pure-`core/` Vitest + hooks/MSW +
  Playwright journeys (login/signup/invite/impersonate).
- **Lessons to honor going forward:**
  1. **Every new test suite goes into CI the day it exists** — the audit's worst
     testing failure was suites wired to no workflow. Add a meta-check that each
     `tests/` dir maps to a job if suites start proliferating.
  2. **Write perimeter tests as forgery tests** — "can a client-supplied header/
     claim ever become trusted?" The starter's header-lockstep invariant is this
     framing; keep new proxy/sidecar tests in it.
  3. **Never allow environment-conditional skips in deny-path tests.** A deny
     test that can silently no-op reads as coverage while protecting nothing;
     fail loud when a fixture is missing. (See the parallel `go test` daemon
     flake note in [MODULE.md](./MODULE.md) — run `-p 1`, don't skip.)

## 6. Open gaps for the starter

Honest backlog implied by the mapping above; sequence and ownership live in
[ROADMAP.md](./ROADMAP.md) / [TODO.md](./TODO.md), not here.

- **Read-access auditing** (§1.8) — the access-set audit pattern with an inline
  cap + dedupe window is unbuilt.
- **`authMode` / require-SSO enforcement per org** (§1.1) — directory + WorkOS
  setup exist; the enforcement flag and the login/invite/signup split do not.
- **Solution-role dimension** (§1.2) — only tenant + platform roles ship; a
  per-`(member, product)` role vocabulary is the audit's third layer, deferred
  until the starter hosts multiple products.
- **Org access-type model for support/delegation** (§1.6) — impersonation is
  platform-role-gated; an org-level `standard`/`internal`/`delegated` access type
  with an explicit allowlist is not built.
- **Approvals primitive** (§Approvals) — the Postgres-backed engine scaffolding
  ships (`pkg/business/approvals.go`, `pkg/infra/postgres_approvals.go` + tests),
  but the `saas/approvals/v1` proto, `approvals:decide` permission, the
  `requireApproval` method-policy gate, and migrating `delegation_grants` onto it
  are design-stage ([APPROVALS_DESIGN.md](./APPROVALS_DESIGN.md)). Real
  approval-shaped flows (delegation, GDPR) are still hand-rolled with
  `requireOrgAdmin` placeholders today.
- **Per-tenant agent kill switch** (§1.5) — only global principal revoke + job
  controls exist.

---

*Source: issue #247 (external platform audit), reconciled against the shipped
accounts service and the authoritative docs cited throughout. Update this file
when a gap above closes; it is a living map, not a snapshot.*
