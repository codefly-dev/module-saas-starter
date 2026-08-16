# SaaS Starter master TODO

Detailed design and sequencing: [ROADMAP.md](ROADMAP.md)
Last reviewed: 2026-07-26

Status legend:

- `[ ]` not started
- `[-]` in progress
- `[x]` complete with acceptance evidence
- `[!]` blocked; add the blocker directly below the item

Keep IDs stable so commits, PRs, tests, and release notes can reference them.

## P0 — stop-ship safety

### Authentication and fixtures

- [x] `P0-AUTH-001` Separate production OAuth and development fixture validators.
- [x] `P0-AUTH-002` Reject no-code authentication unless fixture mode is explicit.
- [x] `P0-AUTH-003` Derive fixture claims only from the fixture allowlist.
- [x] `P0-AUTH-004` Make empty/unknown/incomplete production auth configuration fail startup.
- [x] `P0-AUTH-005` Add bypass, claim-override, unknown-fixture, and provider-mismatch regression tests.
- [x] `P0-AUTH-006` Add an explicit `dev-admin` end-to-end fixture login gate.
- [x] `P0-AUTH-007` Move fixture auth registration behind a dev-only artifact/runtime boundary.
- [x] `P0-AUTH-008` Correct proto validation so OAuth-code requests do not require fixture fields.
- [x] `P0-AUTH-009` Remove client-only OAuth-state fallback; require server-signed state.
- [x] `P0-AUTH-010` Restrict OAuth redirect URIs to configured exact origins/paths.

### Authorization

- [x] `P0-AUTHZ-001` Inventory every proto method in a checked-in policy matrix.
- [x] `P0-AUTHZ-002` Mark every method public, authenticated, tenant-scoped, platform-scoped, or internal.
- [x] `P0-AUTHZ-003` Add deny-by-default Connect and gRPC policy interceptors.
- [x] `P0-AUTHZ-004` Require org admin for organization member mutation.
- [x] `P0-AUTHZ-005` Require membership for team member reads and admin for mutation.
- [x] `P0-AUTHZ-006` Bind API-key create/list/revoke to an authorized organization.
- [x] `P0-AUTHZ-007` Bind webhook administration and replay to organization admins.
- [x] `P0-AUTHZ-008` Bind notification read/delete to the authenticated owner.
- [x] `P0-AUTHZ-009` Bind GDPR requests/status/download/delete to the authenticated subject.
- [x] `P0-AUTHZ-010` Remove public access to identity resolution and API-key validation internals.
- [x] `P0-AUTHZ-011` Make missing internal credentials fail closed.
- [x] `P0-AUTHZ-012` Put internal RPCs on a private listener/network policy.
- [x] `P0-AUTHZ-013` Add actor/tenant/resource substitution test matrices.
- [x] `P0-AUTHZ-014` Remove or update stale unused OPA policy artifacts.

### MFA and step-up

- [x] `P0-MFA-001` Add durable one-use MFA transaction storage.
- [x] `P0-MFA-002` Prevent normal access/refresh issuance before MFA completion.
- [x] `P0-MFA-003` Add a login challenge RPC separate from enrollment verification.
- [x] `P0-MFA-004` Atomically consume a verified MFA transaction and mint the session.
- [x] `P0-MFA-005` Encrypt TOTP secrets with versioned Vault/KMS envelopes.
- [x] `P0-MFA-006` Hash, consume, regenerate, and notify on backup-code use.
- [x] `P0-MFA-007` Add WebAuthn/passkey factor support.
- [x] `P0-MFA-008` Add `amr`, `auth_time`, assurance level, and recent-step-up policy.
- [x] `P0-MFA-009` Rate-limit and lock challenge attempts without creating an account oracle.
- [x] `P0-MFA-010` Add enrollment, login, recovery, expiry, replay, and multi-replica tests.

### Gateway and operational security

- [x] `P0-GW-001` Strip untrusted identity/tenant/role/scope headers at the edge.
- [x] `P0-GW-002` Define the forwarded-identity trust contract and network enforcement.
- [x] `P0-GW-003` Replace full response-body logging with structured redacted diagnostics.
- [x] `P0-GW-004` Replace credentialed origin reflection with exact CORS allowlists.
- [x] `P0-GW-005` Validate trusted proxy chains before consuming forwarded client IPs.
- [x] `P0-GW-006` Replace the raw Redis limiter connection with a pooled TLS client.
- [x] `P0-GW-007` Define fail-open/fail-closed rate-limit behavior by route class.
- [x] `P0-GW-008` Make readiness cover all required upstreams and keep liveness process-only.
- [x] `P0-GW-009` Add header, CORS, proxy, dependency-failure, and protocol integration tests.

### Stripe billing

- [x] `P0-BILL-001` Replace inline dispatch with transactional Stripe inbox ingestion.
- [x] `P0-BILL-002` Add leased event worker states, attempts, and dead-letter handling.
- [x] `P0-BILL-003` Correct duplicate and failed-event semantics.
- [x] `P0-BILL-004` Reconcile current Stripe object state for out-of-order events.
- [x] `P0-BILL-005` Add Stripe idempotency keys to retryable mutations.
- [x] `P0-BILL-006` Move plans, prices, trials, and redirect origins to server-owned catalog config.
- [x] `P0-BILL-007` Require billing-admin permission and recent MFA.
- [x] `P0-BILL-008` Move reconciliation to a privileged worker database pool.
- [x] `P0-BILL-009` Add duplicate, retry, concurrency, ordering, and reconciliation tests.

### Outbound webhooks

- [x] `P0-WH-001` Generate, encrypt, reveal-once, and rotate webhook signing secrets.
- [x] `P0-WH-002` Add normalized URL and public-address validation for IPv4 and IPv6.
- [x] `P0-WH-003` Revalidate DNS at connect time and disable redirects.
- [x] `P0-WH-004` Add egress network policy blocking internal/metadata ranges.
- [x] `P0-WH-005` Replace in-memory queueing with a transactional outbox.
- [x] `P0-WH-006` Claim deliveries safely across replicas with leases/locking.
- [x] `P0-WH-007` Implement bounded backoff, endpoint concurrency, dead-letter, and replay.
- [x] `P0-WH-008` Version signatures over timestamp, event ID, and exact body.
- [x] `P0-WH-009` Add SSRF, DNS, redirect, restart, saturation, and concurrency tests.

### Dependencies and CI bootstrap

- [x] `P0-CI-001` Patch high-risk production frontend dependencies.
- [x] `P0-CI-002` Repair TypeScript/React ESLint flat configuration.
- [x] `P0-CI-003` Remove the workstation-local Codefly `file:` dependency.
  The frontend consumes an immutable Codefly JS SDK release artifact; clean
  installs contain no workstation path. Publishing the same package to npm is
  a distribution improvement, not a correctness blocker.
- [x] `P0-CI-004` Fix unavailable local dependency-agent versions.
- [x] `P0-CI-005` Replace stale Playwright `api` service references with `accounts`.
- [x] `P0-CI-006` Run Go tests, frontend lint/typecheck/test/build, and Buf checks in CI.
- [x] `P0-CI-007` Add secret/dependency/license/SBOM/image/provenance scanning.
- [x] `P0-CI-008` Add clean-checkout and explicit fixture-stack release gates.
  Evidence: `.github/workflows/ci.yml` (`Clean-checkout build and test gate`,
  `Codefly fixture-stack journeys`, and `Release gate`) and `RELEASE_GATES.md`.
- [x] `P0-CI-009` Update settings test doubles for identity-scoped Store access.
- [x] `P0-CI-010` Eliminate empty-string UUID queries and add optional-ID regression tests.
- [x] `P0-CI-011` Restore introspection metadata completeness for existing Team RPCs.
- [x] `P0-CI-012` Align Envoy tests with the renamed accounts upstream clusters.
- [!] `P0-CI-013` Move changed-service closure, dependency-aware test
  scheduling, dependency lifecycle, and all language/tool gates into Codefly's
  CLI and service plugins. `.github/workflows/ci.yml` is now only a pinned CLI
  bootstrap plus `codefly ci run`; the separate handwritten security workflow
  and repository-owned Go, Buf, Next.js, scanner, SBOM, and container matrices
  were deleted. Remaining upstream blockers are authoritative non-mutating sync
  contracts for the Redis, S3, Postgres, and Vault agents, plus Go test-suite
  dependency ownership for packages that currently call `WithDependencies`.
- [x] `P0-CI-014` Make the frontend and Codefly Next.js factory React
  Compiler-ready and server-first.
  React/React DOM `19.2.8` and the stable compiler are enabled; every App Router
  page/layout is a Server Component boundary, interactive routes use explicit
  client islands, `next-themes` script injection is removed, and boundary tests
  prevent regressions. Local Tailwind helpers replace the unused vulnerable
  `shadcn` CLI dependency. The Codefly Next.js `0.0.115` factory carries the
  same defaults, while the shared upgrader handles npm 11 workspace arrays and
  peer-coupled React upgrades atomically.
- [ ] `P0-CI-015` Release the shared Codefly core upgrade changes and Next.js
  agent `0.0.115`, then pin starter consumers to the published agent.
  Local packages and tests are complete; publication and consumer pinning are
  intentionally separate release actions.

## P1 — contract compiler and protocols

### Versioned protobuf and policy schema

- [x] `P1-PROTO-001` Decide and document the stable versioned package namespace.
  Decision: `saas.accounts.v1`, `saas.gateway.auth.v1`, and shared
  `saas.policy.v1`; directory/import mappings and compatibility windows are in
  `module/CONTRACT_VERSIONING.md`.
- [x] `P1-PROTO-002` Split the monolithic proto into bounded-context files.
  The 249 accounts declarations now live in 22 dependency-correct files under
  `saas/accounts/v1`; gateway auth lives under `saas/gateway/auth/v1`. Codefly
  emits modular Go/Connect/gateway/OpenAPI/TypeScript outputs, and the edge
  rewrites exact legacy `customers.*` procedures during the migration window.
- [x] `P1-PROTO-003` Upgrade Buf configuration to v2 STANDARD lint.
  Both protobuf modules and both managed generation templates now use Buf v2
  syntax and `STANDARD`; remaining exceptions name P1 migration owners.
- [x] `P1-PROTO-004` Add format, lint, generation, and release-tag breaking checks.
  CI fetches full tag history, compares each module with the latest prior `v*`
  release, regenerates through pinned Codefly with exact local plugins, and
  fails when any checked-in protobuf, Go, Connect, gateway, OpenAPI, or modular
  TypeScript output changes. Version-pinned BSR templates remain a fallback.
- [ ] `P1-PROTO-005` Remove request/response naming lint exceptions through a
  compatibility-safe successor API; do not rename stable v1 messages in place.
- [ ] `P1-PROTO-006` Fix Codefly CLI local generation's hard-coded
  `<output>/go` goimports assumption. This starter emits Go under `code/pkg/gen`,
  so Codefly 0.1.3 currently logs a non-fatal `stat go` warning after successful,
  byte-stable generation; output roots should be discovered from the template.
- [ ] `P1-DOC-001` Document every enum, value, message, field, oneof, service,
  and RPC, then remove the remaining Buf `COMMENT_*` exceptions.
- [x] `P1-POLICY-001` Define custom method exposure and tenant options.
  `saas.policy.v1.MethodPolicy` provides finite, fail-closed public,
  authenticated, and internal exposure plus tenant requirements.
- [x] `P1-POLICY-002` Define permission/scope and resource-binding options.
  Repeated canonical permissions/scopes and typed request-field-to-resource
  lookup bindings are generated for Go and TypeScript without expressions or
  arbitrary handler names.
- [x] `P1-POLICY-003` Define MFA, audit, idempotency, sensitivity, and rate-limit options.
  The remaining finite policy vocabulary, extension descriptor assertions, and
  protobuf round-trip tests are active; `module/METHOD_POLICY.md` documents the
  compiler's fail-closed authoring rules.
- [x] `P1-POLICY-004` Annotate every existing RPC and review the generated matrix.
  All 125 RPCs declare a complete `method_policy`; descriptor validation rejects
  omissions, unspecified enums, invalid vocabulary/events, and nonexistent
  resource field paths. Runtime admission and the checked-in matrix now consume
  descriptors directly, with only editorial descriptions remaining handwritten.

### Codefly generation

- [x] `P1-GEN-001` Implement descriptor-to-normalized-service-catalog compilation.
  Generated `saas.catalog.v1` Go/TypeScript types define a deterministic
  25-service, 125-method artifact. The compiler discovers the accounts service
  graph without a handwritten service list and fails on policy, route,
  ownership, transport, ordering, grouping, or schema drift.
- [x] `P1-GEN-002` Generate Connect server registration and handler interfaces.
  The catalog plus strict `connect_bindings.yaml` now emit all 25 registrations
  and compile-time assertions against protoc-gen-connect-go interfaces. The
  binding schema permits only gRPC-field, business-service, or singleton
  sources; exact catalog coverage and all 125 mux procedures are tested.
- [x] `P1-GEN-003` Generate Envoy/Istio exact routes and upstream ownership.
  `saas.gateway.routes.v1` emits 355 deterministic routes with Codefly owner,
  named endpoint, exact/path-template semantics, descriptor exposure, and dated
  rewrites. Auth-sidecar consumes generated Connect routes; the exact Istio
  manifest remains undeployed until frontend/static routes land in P1-NET-003.
- [x] `P1-GEN-004` Generate auth/PDP method metadata and policy documentation.
  `saas.authz.methods.v1` projects all 125 policies into deterministic typed
  PDP metadata with policy SHA-256 fingerprints. Auth-sidecar consumes generated
  exposure, rate failure, and login-factor flags; REST parity caught and fixed
  stale protection on public user registration. `AUTHZ_MATRIX.md` remains the
  generated human review surface.
- [x] `P1-GEN-005` Generate opt-in REST/OpenAPI surfaces.
  `saas.rest.surface.v1` now projects 119 explicitly annotated public-edge
  routes across 24 services. Strict generated/plugin bindings emit complete
  grpc-gateway registration, an exact/template runtime allowlist, auth-sidecar
  routing, and a verified 119-operation OpenAPI document. Seven internal RPCs no
  longer carry HTTP annotations. Five non-protobuf routes remain explicit
  extensions; descriptor-equivalent YAML has been removed.
- [x] `P1-GEN-006` Generate TypeScript clients and permission/entitlement constants.
  The normalized catalog now owns 21 permissions, 19 API-key scopes, and five
  entitlement definitions. Its deterministic frontend projection creates
  typed clients for all 25 accounts services and exports finite constants,
  metadata, wildcard grant types, and runtime guards. Frontend role gates,
  common client hooks, and entitlement administration consume the generated
  vocabulary; generator and Vitest parity checks pin all 125 procedures.
- [x] `P1-GEN-007` Generate Codefly endpoints, dependencies, and network policy.
  One strict topology binding now generates the module interface, all seven
  service manifests, a typed 7-service/11-endpoint/8-dependency deployment
  catalog, and 15 Kubernetes NetworkPolicies. Descriptor protocol parity
  requires the accounts gRPC, Connect, and REST endpoints. Codefly parsing,
  unsafe-input, Kustomize rendering, determinism, and checked-in parity tests
  replace the former handwritten endpoint/dependency/network inventories.
- [x] `P1-GEN-008` Generate frontend plugin route/navigation registry inputs.
  A strict binding plus filesystem discovery now emits a typed inventory of
  three built-in plugins, 36 Next.js pages, and 25 canonical navigation items.
  Generated TypeScript drives the built-in plugins, active sidebar, command
  palette, and user menu; Go/Vitest parity rejects owner, route, permission,
  access, surface, ordering, collision, and checked-in drift.
- [x] `P1-GEN-009` Remove manual registration and route inventories after parity.
  Catalog generation now owns the 15 native raw-gRPC registrations on both
  listeners from the same strict implementation binding used by Connect; nine
  Connect-native services remain explicitly derived for P1-NET-001. Nineteen
  mixed REST YAML inventories were replaced by one five-route extension-only
  source, and startup rejects disabled entries or descriptor collisions.

### Protocol and deployment convergence

- [ ] `P1-NET-001` Serve Connect, gRPC, and gRPC-Web from one Connect-Go port.
- [ ] `P1-NET-002` Make REST transcoding explicitly opt-in.
- [x] `P1-NET-003` Keep frontend page/static/plugin routes at the frontend
  product entry instead of duplicating them in the backend gateway. The
  auth-sidecar accepts only generated API routes; unknown page-like paths fail
  closed.
- [ ] `P1-NET-004` Deploy one edge data path using Istio/Envoy plus auth/PDP.
- [x] `P1-NET-005` Repurpose the Go gateway as the private authenticated API
  gateway behind the frontend's same-origin proxy.
- [x] `P1-NET-006` Add generated service-to-service network allow policies.
  The broad namespace allow is gone. Generated caller/target policies permit
  only declared dependency endpoint ports, plus finite DNS, Istio control
  plane/ingress, and accounts/frontend HTTPS egress rules under default deny.
- [ ] `P1-NET-007` Teach the Codefly Go gRPC runtime to inject/bind multiple
  same-API endpoints, then move internal gRPC from the private REST h2c
  listener to a generated named endpoint. Export that endpoint with module
  visibility and generate least-privilege dependency/network-policy edges for
  installed product services. `UsageService.ConsumeUsage` is the first
  cross-module acceptance slice; do not expose the mixed listener meanwhile.
- [x] `P1-NET-008` Declare and validate `frontend` as the module service entry,
  teach Codefly to resolve it from a module or single-module workspace, and
  smoke-test the complete fixture stack from the repository root. Frontend is
  the public product endpoint and depends on private `auth-sidecar/rest`;
  auth-sidecar depends on Accounts and infrastructure, so the graph is acyclic
  and one default command starts the whole product.
- [ ] `P1-NET-009` Make the generated public OpenAPI document available inside
  the standalone frontend artifact and serve it through `/api/openapi`.
  The current route still resolves the removed
  `../../api/openapi/user.swagger.json` path, so it returns 404 in native and
  container layouts even though the canonical document exists under
  `accounts/openapi/api.swagger.json`.

### Contract CI

- [x] `P1-CI-001` Add proto/handler/protocol/OpenAPI/Envoy/TS parity tests.
  Connect registration now has catalog-to-config generation parity,
  compile-time handler-interface parity, and catalog-to-runtime-mux parity for
  all 125 procedures. Gateway generation adds catalog-to-runtime Connect parity,
  355-route target-neutral/Envoy/Istio parity, and internal-route exclusion.
  Authorization and REST/OpenAPI catalog/runtime parity are also complete. The
  TypeScript catalog now verifies all 25 clients, 125 procedures, 21
  permissions, 19 API-key scopes, and five entitlements. Frontend plugin parity
  additionally pins three plugin sources, 36 filesystem pages, 25 navigation
  items, all four consumer surfaces, access tiers, and permission references.
- [x] `P1-CI-002` Add generated-clean-diff checks.
  Protobuf/Go/Connect/gateway/OpenAPI/TypeScript outputs are covered by
  P1-PROTO-004; the normalized service and authorization catalogs, policy
  documentation, generated auth-sidecar policy lookup, Connect/raw-gRPC registration,
  gateway inventory/runtime, REST catalog/registration/auth-sidecar routing,
  filtered OpenAPI, Istio matches, frontend client/vocabulary catalog, normalized
  deployment topology, module/service Codefly manifests, NetworkPolicies, and
  frontend page/plugin/navigation catalogs are also covered. New P1-GEN
  producers must join the same gate when added.
- [x] `P1-CI-003` Snapshot public/internal API exposure for review.
  `AUTHZ_MATRIX.md` is a descriptor-generated 125-RPC exposure/policy snapshot,
  and the accounts suite fails when the checked-in review artifact drifts.
- [ ] `P1-CI-004` Render and schema-validate deployment artifacts.
  Base/local/AWS Kustomize rendering and strict generated NetworkPolicy parsing
  now pass; add pinned cluster-schema validation for all built-in and installed
  CRD resources before closing this gate.
- [ ] `P1-CI-005` Add plugin and API compatibility fixtures across supported versions.

## P2 — data, sessions, and durable work

### Database authority and RLS

- [x] `P2-DB-001` Create separate migration-owner, tenant-runtime, and privileged-worker roles.
  The migration connection owns every application relation; the managed runtime
  session owns none. Tenant, control-plane, billing-projection,
  webhook-projection, and generic job-worker roles have pinned fail-closed
  attributes and grants.
- [-] `P2-DB-002` Remove superuser/role-switch authority from runtime connections.
  Runtime roles are `NOLOGIN`, cannot create roles/databases, and the tenant
  role cannot assume worker roles. Codefly must still expose role-specific
  connection secrets so its managed request credential does not hold every
  application-role membership.
- [x] `P2-DB-003` Remove the application-settable RLS bypass flag.
  `app_control_plane` is the named cross-scope capability, `WithControlPlane`
  assumes it transaction-locally, and active RLS policy expressions are tested
  to contain no `app.bypass` branch.
- [x] `P2-DB-004` Inventory every table as tenant, user, global, or worker-owned.
  The complete public-table inventory, exact request-role grants, scope class,
  RLS enable/force state, and policy presence are tested and documented in
  `module/DATABASE_AUTHORITY.md`. Adding an unclassified table fails the gate.
- [x] `P2-DB-005` Repair user-row visibility using narrow views/operations.
  The request role now sees only its own full `users` row. Platform/pre-auth
  paths use the named control plane, worker paths keep named worker roles, and
  tenant co-member lookup is limited to the membership-checked
  `organization_member_primary_email` database operation. Handler authorization
  occurs before privileged lookup, and executable tests pin the policy and
  function authority.
- [-] `P2-DB-006` Add grant, ownership, policy, and cross-role migration tests.
  Role attributes, ownership separation, tenant-to-worker isolation, forbidden
  `TRUNCATE`/schema/temp authority, fail-closed future grants, complete relation
  inventory, RLS posture, every request/global/control-plane privilege, and the
  absence of application-settable policy bypasses are pinned. Separately
  credentialed request/control-plane integration tests remain.

### Sessions and organization context

- [x] `P2-SESSION-001` Make refresh consume/rotation atomic.
  `SessionStore.RotateRefresh` now locks the presented row and performs active
  family consumption plus successor insertion in one control-plane
  transaction. Replacement construction/insertion failure rolls the
  consumption back. Concurrent reuse has one rotation winner, then commits
  user-wide refresh-session revocation before returning `ErrRefreshReuse`.
- [x] `P2-SESSION-002` Re-resolve current user status, roles, memberships, and MFA on refresh.
  The locked refresh transaction now requires an active user, validates the
  selected organization membership, and loads current organization role,
  platform role, and verified MFA enrollment before constructing a successor.
  Removed memberships revoke the affected family; inactive accounts revoke all
  user sessions. Newly enrolled MFA forces a new authentication ceremony, while
  removed MFA downgrades persisted AAL2 evidence. Existing inactive identities
  are also rejected at interactive login.
- [x] `P2-SESSION-003` Revoke/version sessions after authorization-relevant changes.
  Migration 70 installs one locked-down, control-plane-owned trigger boundary
  for the exact facts persisted into tokens: user status, organization
  membership/role, platform role, and verified MFA enrollment. Affected active
  refresh sessions are revoked in the same transaction as each mutation,
  including writes outside current handlers. Tenant changes are scoped to that
  organization plus org-less sessions; user-wide facts revoke every family.
  Trigger revocation rolls back with a failed mutation, and non-rotation
  administrative revocation is no longer misclassified as token replay.
- [x] `P2-SESSION-004` Add absolute, idle, and per-device session policies.
  One validated policy now configures a fixed seven-day absolute family
  lifetime, a 24-hour idle window, and at most ten active device families by
  default. Refresh advances only the explicit idle expiry while preserving the
  family creation time and absolute expiry. Initial login admission is
  serialized on the user row and atomically evicts the least-recently active
  family at the configured cap. Bounded display-only device metadata survives
  direct login, the durable MFA handoff, and rotation; management APIs expose a
  stable family id and revoke the whole device family. All limits are generic
  Codefly `security` workspace configuration.
- [x] `P2-SESSION-005` Add an authorized `SwitchOrganization` token exchange.
  The proto-owned authenticated exchange accepts only a target organization id.
  Accounts locks the exact active session selected by the verified access-token
  `sub` and `sid`, resolves current membership, tenant role, platform role, and
  MFA enrollment from PostgreSQL, signs a fresh access token, and persists only
  the selected-tenant projection. It neither rotates the refresh credential nor
  creates a device, advances idle expiry, or slides the absolute lifetime.
  Non-membership is denied without mutation and stale session ids fail closed.
- [x] `P2-SESSION-006` Wire the frontend organization selector to token exchange.
  Organization selection is global authenticated state decoded from the signed
  `org` claim. The generic selector calls the generated Connect client, waits
  for the exchange before installing the token, serializes rapid selections,
  and disables while switching. All tenant-scoped admin pages use that session
  organization in their query keys and mutations instead of independent local
  filters; tenant-specific dialogs and revealed secrets are reset on change.
- [x] `P2-SESSION-007` Add concurrent-refresh, revocation, and tenant-switch tests.
  In-memory and real-Postgres gates now cover concurrent same-token refresh,
  committed replay revocation, failed-successor rollback, refresh-time role and
  membership changes, inactive users, terminal policy rejection, and MFA
  enrollment transitions. Database-trigger gates additionally cover scoped and
  user-wide authorization-change revocation plus rollback atomicity. Session
  policy gates cover absolute/idle termination, lifetime preservation, device
  metadata continuity, deterministic oldest-device eviction, and concurrent
  cap admission. Organization-exchange gates additionally prove exact current
  membership, refresh/family/lifetime/device preservation, denial without
  mutation, end-to-end refresh continuity, and switch-versus-refresh row-lock
  behavior without false replay revocation.

### Principal authority and Work Context

- [x] `P2-WORK-001` Make direct RBAC assignments target Principals.
  `SUBJECT_KIND_PRINCIPAL = 1` is now canonical while the legacy
  `SUBJECT_KIND_USER = 1` alias remains deprecated and wire-compatible.
  Migration 79 rewrites stored direct assignments from `user` to `principal`,
  updates the database constraint and authorization-revision trigger, and lets
  human, service, and Agent Principals receive explicit roles without semantic
  impersonation.
- [x] `P2-WORK-002` Add product-neutral Task/Session capability exchange.
  The permissions plugin owns generated `StartTask`, `StartRootSession`, and
  `StartChildSession` gRPC/Connect/REST operations. Accounts issues the shared
  Codefly Ed25519 Work Context and deliberately owns no product Task/Session
  lifecycle rows.
- [x] `P2-WORK-003` Resolve Work Context authority through verified
  `service-postgres` scope.
  One Reader transaction binds the authenticated tenant/owner, current
  membership, organization and Principal authorization revisions, immutable
  team attribution, active Agent Actor, and every exact
  resource/action/resource-id grant. Caller-selected tenant facts, revoked
  Actors, and scope widening fail closed.
- [x] `P2-WORK-004` Prove attenuation and database authority.
  Fresh Codefly-managed PostgreSQL tests cover direct Agent grants, human RBAC,
  current revisions, scope mismatch, foreign tenant substitution, revoked
  Actor, cross-tenant RLS, runtime roles, exact grants, complete relation
  inventory, and RLS-policy parity. RPC tests cover stale-parent rejection and
  fail-closed signer/store configuration.
- [ ] `P2-WORK-005` Add revisioned authorization caching with explicit
  invalidation.
  Cache only product-neutral computed facts keyed by organization, Principal,
  requested permission tuple, and current revision. A cache outage or stale
  entry may reduce performance or narrow presentation; it must never widen
  Work Context issuance, database RLS, or current revocation behavior.
- [ ] `P2-WORK-006` Publish a public Accounts client package for external
  products and tools.
  Consumers must use generated/public SDK clients for identity, directory, and
  Work Context exchange rather than importing Accounts-internal generated Go
  packages. The conformance suite must cover Go and TypeScript first.

### Inbox/outbox workers

- [x] `P2-JOB-001` Define common inbox/outbox schema and state machines.
  The product-neutral `saas.jobs.v1` protobuf contract now owns scoped
  inbox/outbox envelopes, leases, failures, attempts, and lifecycle vocabulary.
  Migration 72 adds immutable exact-byte messages, attempt and append-only
  transition relations, finite database-enforced state changes, monotonic state
  versions, required fencing fields, payload/metadata bounds, idempotency and
  scheduling indexes, and terminal replay lineage. Tenant request traffic has
  only a scoped enqueue-operation capability for its signed organization or
  subject and no raw job-table rights; the new
  `app_job_worker` role has exact job-table authority and no product-table
  access. Generated Go/TypeScript types, exhaustive Go transition tests, real
  PostgreSQL RLS/role/lifecycle tests, and cross-layer transition parity are
  active. See `module/JOBS.md`.
- [x] `P2-JOB-002` Implement leases, heartbeats, retries, schedules, and dead letters.
  Generated `saas.jobs.v1` commands now define bounded claims, lease references,
  heartbeats, success, scheduled retry, and permanent-failure inputs without
  adding a public Accounts transport. The product-neutral Go store validates
  those contracts and atomically uses PostgreSQL's database clock,
  `FOR UPDATE SKIP LOCKED`, per-attempt UUID fences, and attempt rows. Claims
  preserve ready priority plus strict per-key FIFO; expired attempts recover to
  retry or terminal dead letter; finalizers require the current live fence and
  commit attempt/message state together. Generated Go/TypeScript, unit contract
  tests, and real-PostgreSQL tests cover schedules, two concurrent workers,
  disjoint claims, unique tokens, ordering, heartbeat, retry, crash recovery,
  late workers, permanent failure, and attempt-budget exhaustion. See
  `module/JOBS.md`.
- [x] `P2-JOB-003` Add idempotency and ordering-key helpers.
  Generated `NewJob`, `JobOrderingKey`, and enqueue request/response contracts
  now drive the product-neutral producer without adding a public Accounts RPC.
  Collision-free component encoding supplies stable FIFO keys, while a
  domain-separated deterministic-protobuf SHA-256 fingerprint distinguishes
  exact retries from conflicting reuse of the same producer key. Request
  enqueue refuses a bare context and commits through the existing tenant or
  subject business transaction; privileged global/inbox ingestion uses the
  isolated job-worker pool. A security-definer PostgreSQL operation checks the
  actual caller role and signed scope, resolves concurrent insert-or-duplicate
  races, and leaves request traffic with no payload-table rights. Unit and
  fresh-PostgreSQL tests cover exact duplicate identity, conflicts, concurrency,
  rollback atomicity, tenant/subject/direction authority, privileged ingestion,
  raw-insert denial, and ordering-key compatibility. See `module/JOBS.md`.
- [x] `P2-JOB-004` Add worker metrics, traces, shutdown, replay, and operations UI.
  The product-neutral worker now provides bounded polling, concurrent handlers,
  lease heartbeats, safe typed failures, panic/error redaction, retry policy,
  low-cardinality generated metrics, Wool/OpenTelemetry spans, and graceful
  deadline shutdown. Super-admin PlatformAdmin RPCs expose database-derived
  queue health and payload-free job/attempt/transition metadata. Dead-letter
  replay requires recent MFA and one transport/request idempotency key; a
  worker-only security-definer operation copies payload in PostgreSQL, preserves
  immutable lineage, resolves exact retries, rejects conflicting key reuse, and
  emits one success audit. The generated/cataloged `/admin/platform/jobs` UI
  filters and seek-pages metadata, displays history, and offers dead-letter-only
  replay without receiving payloads. Race-enabled unit, fresh-PostgreSQL,
  catalog, backend, and frontend gates cover the slice. See `module/JOBS.md`.
- [x] `P2-JOB-005` Migrate Stripe processing.
  Stripe's signed exact raw body is now retained in the generated
  `saas.billing.v1.StripeWebhookJob` payload and enqueued through the generic
  privileged producer with a stable queue/topic/source/schema contract and the
  Stripe event ID as its idempotency key. The generic worker owns claims,
  heartbeat, retries, fences, safe failures, dead letters, telemetry, replay,
  and graceful shutdown; a thin adapter validates routing and generated bytes
  before invoking the existing monotonic subscription projector. The dedicated
  Stripe inbox table, store API, worker runtime, migrations, grants, and tests
  were removed. `app_job_worker` owns lifecycle while `app_billing_worker` is
  restricted to cross-tenant product projection. Unit, role, fresh-migration,
  and signed HTTP-to-PostgreSQL-worker integration tests cover the slice. See
  `module/JOBS.md`.
- [x] `P2-JOB-006` Migrate outbound webhooks.
  Audit insertion, matching delivery history, and generated
  `saas.webhooks.v1.OutboundWebhookJob` enqueue now share one organization
  transaction. The delivery UUID is the idempotency key and a structured
  subscription ordering key gives strict per-endpoint FIFO. The generic worker
  owns claims, heartbeats, fences, retry scheduling, attempt budgets, safe
  failure history, dead letters, telemetry, replay, and graceful shutdown. A
  thin handler validates routing, tenant scope, ordering, generated payload,
  and exact stored bytes before using the existing Vault/SSRF-safe signing
  transport. `webhook_deliveries` is now product history only; the specialized
  worker, queue store, lifecycle columns/indexes, and async compatibility
  emitter were removed. Test and replay also atomically queue generated jobs;
  no synchronous executor remains. `app_webhook_worker` can only read endpoint data
  and update delivery outcomes, while `app_job_worker` has no product-table
  access. Unit, frontend, role, and audit-to-HTTP PostgreSQL integration tests
  cover the slice. See `module/JOBS.md` and `module/WEBHOOKS.md`.
- [x] `P2-JOB-007` Migrate email and notification delivery.
  The generated `saas.notifications.v1.EmailDeliveryJob` owns exact rendered
  recipients, headers, HTML/text bodies, and bounded tags on the common job
  envelope. Invitation and email enqueue share one tenant transaction;
  magic-link token and global email enqueue share one audited pre-auth
  transaction. Billing uses a stable Stripe event/template key and returns
  enqueue failure to its source worker. The generic email worker is the only
  provider path and owns leases, retries, dead letters, replay, safe failures,
  telemetry, and shutdown. Templates reject malformed/missing variables,
  escape HTML values, and persist immutable output; built-in billing messages
  use subscription management, never pricing. In-app notification rows remain
  their own durable owner-bound destination. Unit and fresh-PostgreSQL tests
  cover exact generated bytes, replay identity, provider idempotency,
  transaction rollback, role authority, and end-to-end delivery. See
  `module/JOBS.md`.
- [ ] `P2-JOB-008` Migrate audit and privacy exports.
- [ ] `P2-JOB-009` Migrate agent approval execution.

### Privacy, retention, and entitlements

- [ ] `P2-PRIV-001` Build a tested data inventory and retention classification.
- [ ] `P2-PRIV-002` Implement durable subject-bound privacy export jobs.
- [ ] `P2-PRIV-003` Upload encrypted exports and issue short-lived signed URLs.
- [ ] `P2-PRIV-004` Correct deletion identifiers and implement holds/ownership transfer.
- [ ] `P2-PRIV-005` Add retry, verification, audit, and completeness tests.
- [ ] `P2-ENT-001` Separate catalog, payment, subscription, and computed entitlements.
- [ ] `P2-ENT-002` Generate shared entitlement types for backend/frontend/plugins.
- [ ] `P2-ENT-003` Add cache invalidation and reconciliation.
- [-] `P2-ENT-004` Add seat and usage meter primitives.
  Seat/API-key cardinality gauges use authoritative rows and now serialize
  direct-member, pending-invitation, and API-key admission with their writes.
  Migration 60 and `UsageService` add a tenant-RLS event ledger, monthly
  aggregates, idempotent receipts, and atomic hard-quota consumption. Remaining
  before this closes: reconciliation jobs/metrics and customer billing-provider
  reporting.

## P3 — plugin platform and Mind

### Unified plugin contract

- [x] `P3-PLUGIN-001` Replace the two frontend plugin interfaces with one versioned contract.
  Contract v2 is the only metadata model and has no React dependency. The
  public React package binds lazy components by stable metadata ID; the former
  `AdminPlugin`, `admin-core`, private barrels, and parallel framework interface
  are deleted and guarded against resurrection.
- [x] `P3-PLUGIN-002` Define `plugin.codefly.yaml` and its JSON/protobuf schema.
  `@codefly/saas-plugin-manifest` defines the unified manifest — identity,
  services, api exposes/consumes, events, ui, needs, permissions, entitlements,
  config, migrations, egress, lifecycle, integrity — as a canonical JSON Schema
  plus a pure JSON-safe validator that reuses the frontend contract for the `ui`
  block. `toSolutionSpec` projects the shared facts onto obin's lodestar
  `SolutionSpec` and carries starter-only sections through `extensions`, so the
  two manifests converge instead of forking. See
  `module/docs/plugin-manifest-schema.md`.
- [ ] `P3-PLUGIN-003` Include API compatibility, routes, nav, permissions, entitlements, config, migrations, events, and egress.
- [ ] `P3-PLUGIN-004` Generate frontend/backend/gateway/networking registration.
- [ ] `P3-PLUGIN-005` Detect duplicate IDs, routes, permissions, and migrations.
- [ ] `P3-PLUGIN-006` Drive the active admin shell from the generated registry.

### Compile-time and runtime plugins

- [ ] `P3-PLUGIN-007` Complete typed compile-time lazy routes and error boundaries.
- [ ] `P3-PLUGIN-008` Add plugin-scoped settings, query keys, flags, and entitlements.
- [ ] `P3-RUNTIME-001` Build the controlled same-origin Warden plugin registry.
- [ ] `P3-RUNTIME-002` Verify signed manifests and pinned artifact hashes.
- [ ] `P3-RUNTIME-003` Add host/plugin API and backend/frontend compatibility handshake.
- [ ] `P3-RUNTIME-004` Enforce capability ceilings, CSP/egress, rollout, rollback, and kill switch.
- [ ] `P3-RUNTIME-005` Provide iframe/worker/remote-app isolation for untrusted extensions.

### Mind capabilities and approvals

- [ ] `P3-MIND-001` Add short-lived workload identity for agents and services.
- [x] `P3-MIND-002` Model agent principals separately from users/API keys.
  The unified Principal directory has explicit HUMAN/SERVICE/AGENT kinds, and
  direct RBAC assignments now target Principals rather than a user-only
  subject. The Codefly fixture proves a Claude Code Agent with independent
  `evidence:append` authority.
- [-] `P3-MIND-003` Issue audience/subject/org/action/resource-bound capabilities.
  Work Context issuance binds audience, owner, current Actor, organization,
  Task, Session, action/resource scopes, attribution, expiry, replay policy, and
  authorization revision. Automatic workload authentication and propagation
  across all agent/tool boundaries remain.
- [-] `P3-MIND-004` Make capabilities short-lived, one-use, revocable, and replica-safe.
  TTL, monotonic attenuation, current authorization revisions, idempotent versus
  single-use policy, and durable consumer replay semantics exist. The generic
  single-use replay store and workload revocation distribution remain.
- [ ] `P3-MIND-005` Persist risk inputs, policy version/hash, approvals, and redemption.
- [ ] `P3-MIND-006` Require approver roles and recent MFA for high-risk actions.
- [ ] `P3-MIND-007` Add immutable audit, notification, timeout, cancel, and replay safety.

## P4 — reusable SaaS product depth

### Account and tenant lifecycle

- [ ] `P4-ACCOUNT-001` Complete email verification and secure account recovery.
- [ ] `P4-ACCOUNT-002` Add passkeys/WebAuthn and identity linking/unlinking safeguards.
- [ ] `P4-ACCOUNT-003` Add user-facing session/device management.
- [ ] `P4-ORG-001` Complete invitation expiry, revocation, resend, and acceptance flows.
- [ ] `P4-ORG-002` Add ownership transfer, suspension, deletion, export, and domain policy.
- [ ] `P4-ORG-003` Define team nesting/bulk admin and SCIM-ready identifiers.
- [ ] `P4-SSO-001` Add optional enterprise SSO and SCIM plugin.

### Notifications and email

- [ ] `P4-NOTIFY-001` Add per-user/per-org event and channel preferences.
- [ ] `P4-NOTIFY-002` Add durable in-app, email, Slack, and webhook delivery.
- [x] `P4-NOTIFY-003` Version templates and validate template variables.
  The system-managed template catalog carries an explicit version. Rendering
  rejects malformed and unresolved placeholders, escapes HTML-context values,
  and stores only the exact rendered result in the versioned generated email
  workload, making later catalog edits irrelevant to an existing delivery.
- [ ] `P4-NOTIFY-004` Add preview, test send, localization readiness, and delivery logs.
- [ ] `P4-NOTIFY-005` Add digests, quiet hours, unsubscribe, bounce, and complaint handling.

### Billing product depth

- [ ] `P4-BILL-001` Add seat reconciliation and usage metering.
- [ ] `P4-BILL-002` Add taxes, coupons, invoices, trials, grace periods, and dunning.
- [ ] `P4-BILL-003` Add cancel/reactivate and safe plan migration flows.
- [ ] `P4-BILL-004` Add customer billing history and admin reconciliation tooling.
- [ ] `P4-BILL-005` Add deterministic Stripe test-clock lifecycle suites.

### Administration and support

- [ ] `P4-ADMIN-001` Add scoped support roles and time-bound audited impersonation.
- [ ] `P4-ADMIN-002` Add privacy-aware organization/user search.
- [ ] `P4-ADMIN-003` Add expiring approved feature/entitlement overrides.
- [ ] `P4-ADMIN-004` Add event/job/webhook/billing dry-run and replay tooling.

### Reliability, security, and releases

- [ ] `P4-OPS-001` Add correlated OpenTelemetry traces, metrics, and logs.
- [ ] `P4-OPS-002` Define SLOs, dashboards, alerts, and runbooks.
- [ ] `P4-OPS-003` Add backup, restore, PITR, and disaster-recovery exercises.
- [ ] `P4-OPS-004` Add zero-downtime schema/application rollout tests.
- [ ] `P4-OPS-005` Add autoscaling, disruption, resource, and topology policies.
- [ ] `P4-SEC-001` Maintain threat models and schedule penetration tests.
- [ ] `P4-SEC-002` Add secret rotation and incident-response procedures.
- [ ] `P4-RELEASE-001` Version starter, contract, generator, and plugin SDK independently.
- [ ] `P4-RELEASE-002` Publish compatibility matrices, upgrade guides, and migration fixtures.

## Completed evidence

Add completion records here in the form:

```text
- YYYY-MM-DD ID — implementation files; tests/commands; migration or release note.
```

- 2026-07-12 `P0-AUTH-001`–`P0-AUTH-005` — separate production/development
  validator wiring in `pkg/business` and fail-closed provider selection in
  `work.go`; regression coverage in `auth_oauth_test.go`, `work_auth_test.go`,
  and `pkg/auth/dev/validator_test.go`; passed `go test . ./pkg/auth/dev` and
  focused full-stack `go test ./pkg/business` through Codefly dependencies.
- 2026-07-12 `P0-CI-004` — pinned available S3, Postgres, and Vault Codefly
  agents (`0.0.10`, `0.0.97`, `0.0.9`); the focused Codefly-backed business
  test progressed through dependency resolution and passed.
- 2026-07-12 `P0-CI-009` — updated the user-settings fake store to model the
  identity-scoped `Store.As(...).Within(...)` contract; removes the full-suite
  nil-interface panic without weakening production RLS behavior.
- 2026-07-12 `P0-CI-011` — added the missing `UpdateTeam` and `DeleteTeam`
  catalog metadata, including exact HTTP paths, org-admin policy, and audit
  classification; the descriptor-driven completeness test now passes.
- 2026-07-12 `P0-CI-012` — replaced stale `api_rest`/`api_connect` assertions
  with the generated `accounts_rest`/`accounts_connect` cluster names; the
  complete auth-sidecar Go suite passes.
- 2026-07-12 `P0-AUTH-006` — added a real fixture-validator/business login
  integration test and passed all six `tests/e2e/login.spec.ts` journeys against
  the Codefly `dev-admin` stack and a production Next build. The gate also made
  fixture seeding idempotent under RLS, exempted same-origin backend proxy paths
  from page middleware, and fixed cookie refresh proto-JSON injection.
- 2026-07-12 `P0-AUTH-008` — replaced overloaded profile/identity fields with a
  generated required `authentication` oneof (`oauth_code` or `fixture`), added
  Protovalidate contract tests, regenerated Go, REST, OpenAPI, and TypeScript
  artifacts with Buf/Codefly, and migrated frontend consumers.
- 2026-07-12 `P0-AUTH-009`–`P0-AUTH-010` — OAuth initiation and exchange now
  fail closed without a server signer and exact provider/redirect policy;
  the original static `IDENTITY_ALLOWED_REDIRECT_URIS` startup requirement was
  superseded by the trusted Codefly public-origin capability (the static list
  remains an optional direct-access fallback); backend state/policy tests,
  frontend typecheck, and the production build pass.
- 2026-07-12 `P0-CI-005` — Playwright endpoint resolution and readiness now use
  the generated `accounts` service name; the real Codefly fixture login suite
  passes (`6 passed`).
- 2026-07-12 `P0-AUTHZ-001`–`P0-AUTHZ-014` — descriptor-derived 109-RPC policy
  inventory, deny-by-default Connect/gRPC admission, private internal gRPC,
  handler/RLS tenant and ownership gates, and checked-in substitution evidence
  in `AUTHZ_MATRIX.md` and `AUTHZ_SUBSTITUTION_MATRIX.md`; removed the unused
  divergent Rego policy. Accounts `go test -p=1 ./...` and `go vet ./...` pass.
- 2026-07-12 `P0-GW-001`–`P0-GW-009` — authenticated gateway identity stamping,
  edge header stripping, exact credentialed CORS, trusted proxy chains, pooled
  TLS Redis limiting, per-route dependency failure policy, catalog-derived
  readiness, process-only liveness, and REST/Connect/internal-gRPC integration
  tests. Auth-sidecar `go test ./...` and `go vet ./...` pass.
- 2026-07-12 `P0-AUTH-007` — fixture validator registration and fixture seeding
  now require both a selected Codefly fixture and
  `CODEFLY__ENVIRONMENT=local`; staging/production fixture confusion tests and
  the complete accounts suite pass.
- 2026-07-12 `P0-MFA-001`–`P0-MFA-004` — added hashed, five-minute,
  database-backed MFA login transactions; primary authentication and magic
  links issue no access/refresh session for enrolled users; generated the
  distinct public `CompleteMFAChallenge` gRPC/Connect/REST/OpenAPI/TypeScript
  contract; atomically validate/consume the factor, insert the session, and
  consume the transaction. PostgreSQL integration coverage proves zero
  pre-challenge sessions, cross-user rejection, expiry, replay rejection,
  exactly-one-winner concurrent completion, and MFA preservation across
  refresh. Focused accounts, auth-sidecar, frontend typecheck, and all 205
  frontend tests pass.
- 2026-07-12 `P0-MFA-005`–`P0-MFA-006` — TOTP seeds now use a
  purpose-bound `cfs1` application envelope over versioned Vault Transit
  ciphertext with no plaintext fallback; startup performs a restart-safe,
  fail-closed migration of legacy base32 rows before serving. Recovery codes
  remain bcrypt-hashed, rotate atomically, are consumed in the same transaction
  as MFA/session issuance, and create an atomic user security notification plus
  audit event. Vault envelope/malformed/purpose tests, legacy migration tests,
  and recovery use/replay/regeneration/notification integration tests pass.
- 2026-07-13 `P0-MFA-008`–`P0-MFA-009` — access tokens and refreshable
  sessions now carry durable `amr`, `auth_time`, `acr` (`aal1`/`aal2`), and
  separate MFA-verification time; refresh rotation preserves rather than
  renews this evidence, and sensitive operations require AAL2 verified within
  the Codefly-configured freshness window (15 minutes locally). Auth-sidecar
  projects only signed assurance headers and both
  gateway/accounts strip untrusted copies. MFA completion has a dedicated
  10/minute trusted-client-IP edge budget plus a database-atomic five-failure
  transaction lock shared by every replica; invalid, expired, consumed,
  replayed, cross-user, and locked challenges use one rejection sentinel.
  The shared Codefly `security` workspace configuration supplies both the
  step-up window and edge attempt budget with fail-fast validation. Migration
  `51_authentication_assurance` and assurance/refresh/header/rate-limit/lock
  integration tests added; accounts vet, serialized tests, and the
  auth-sidecar test/vet suites pass (the two recurring Codefly control-socket
  package failures passed immediately in isolated reruns).
- 2026-07-13 `P0-MFA-007` and `P0-MFA-010` — added production-shaped
  WebAuthn/passkey registration and second-factor login ceremonies using
  `go-webauthn`. Exact RP ID/origin policy comes from the Codefly `security`
  configuration; user verification is required and discoverable credentials
  are preferred. Full credential records and server ceremony state use
  purpose-bound Vault envelopes, while globally unique public credential IDs
  support lookup and ownership enforcement. Registration and assertion state
  are short-lived, server-side, one-use, RLS-scoped, and login ceremonies are
  bound to the existing opaque MFA transaction. Credential counter/state
  updates, ceremony consumption, session insertion, and MFA-transaction
  consumption share one locked database transaction. Native gRPC,
  grpc-gateway REST, Connect, OpenAPI, and generated TypeScript contracts are
  exposed with matching gateway policy and rate limits. The Next.js settings
  and login flows use SimpleWebAuthn with passkey/security-key UX. Added
  encrypted-at-rest, global ownership, AAL2/`amr`, cross-transaction, replay,
  configuration, generated-policy, and cleanup coverage; focused Go tests,
  Go vet, auth-sidecar tests, TypeScript, all 205 frontend tests, and the
  production Next build pass.
- 2026-07-13 `P0-BILL-001`–`P0-BILL-003` — replaced request-scoped Stripe
  dispatch with signature-verified durable ingestion. The exact raw payload,
  event/type, Stripe creation time, API version, and live/test mode are retained
  in a generated `StripeWebhookJob` before the endpoint returns `2xx`; the
  generic job platform owns receipt time, lifecycle, attempts, leases, bounded
  retry, and safe dead-letter diagnostics. Atomic `FOR UPDATE SKIP LOCKED`
  claims support concurrent replicas, heartbeats and fenced expiring leases
  recover crashed work, and exact duplicate Stripe deliveries never overwrite
  or suppress an internally retrying event. Handler, adapter, generic-worker,
  and fresh-PostgreSQL tests cover signature rejection, exact-payload receipt,
  duplicate/conflict behavior, retry safety, lease recovery, and dead letters.
- 2026-07-13 `P0-BILL-004` and `P0-BILL-009` — every
  subscription-changing webhook now retrieves the current Stripe subscription
  instead of trusting an out-of-order event snapshot. Migration
  `55_stripe_subscription_reconciliation` records provider-read observation
  time, recognizes Stripe's complete subscription status enum, and makes the
  Stripe subscription ID unique. PostgreSQL advisory transaction locks
  serialize projections by organization; monotonic observation checks prevent
  an older, slower provider read or a late event for a prior subscription from
  overwriting the newer current subscription. Processor and real-Postgres
  tests cover current-state hydration, reverse completion, prior-subscription
  delivery, duplicate receipt, worker retry, lease recovery, concurrency, and
  dead-letter exhaustion.
- 2026-07-13 `P0-BILL-005`–`P0-BILL-008` — every Stripe POST now carries a
  required, operation-namespaced idempotency key stable across client/gateway
  retries. Checkout accepts only a catalog plan key; migration
  `56_billing_catalog_policy` owns checkout availability, price mapping,
  currency, trial duration, and tax behavior, while validated `APP_BASE_URL`
  owns exact checkout/portal redirects. The removed portal `return_url` field
  is reserved in protobuf and Go, OpenAPI, and TypeScript clients were
  regenerated through Codefly. REST and Connect require an organization
  owner/admin or delegated `billing:write` plus fresh AAL2 evidence; no-MFA
  enrollment is not a money-moving bypass. Migration `57_billing_worker_role`
  and a dedicated four-connection pool isolate reconciliation under the
  least-privilege `app_billing_worker` BYPASSRLS role. Client, catalog,
  authorization, CORS, URL-policy, role-isolation, and cross-tenant projection
  tests pass; TypeScript and all 205 frontend tests pass.
- 2026-07-13 `P0-WH-001`–`P0-WH-004` and `P0-WH-008` — endpoint creation now
  generates a 256-bit signing key, stores only a subscription-bound Vault
  Transit envelope, and reveals plaintext once in the generated API response.
  Startup encrypts legacy keys and disables unrecoverable empty-key rows.
  Rotation supports a bounded dual-signature overlap and requires recent AAL2.
  Registration accepts normalized public HTTPS/443 endpoints only; every A/AAAA
  answer is checked against IPv4/IPv6 special-use ranges, and the custom dialer
  resolves again, pins the validated address, bypasses environment proxies, and
  never follows redirects. Kubernetes egress policy mirrors the internal,
  metadata, loopback, link-local, multicast, documentation, and benchmark
  denylist. Versioned signatures cover Unix time, stable event ID, and exact
  persisted body bytes; generated Go/REST/Connect/OpenAPI/TypeScript contracts
  expose one-time secrets and delivery-history detail. `module/WEBHOOKS.md`
  documents consumer verification, replay tolerance, and rotation.
- 2026-07-20 `P0-WH-005`–`P0-WH-007`, `P0-WH-009`, and `P2-JOB-006` — audit
  insertion, exact-byte delivery history, and a generated outbound-webhook job
  now commit atomically. The generic job runtime owns strict subscription FIFO,
  multi-replica claims, heartbeats, fenced lease recovery, bounded retry, safe
  failures, and dead letters; the specialized webhook queue lifecycle and
  legacy async emitter were deleted. `app_webhook_worker` has only endpoint
  reads and delivery-history updates, with no job or unrelated product access.
  Test and manual replay create a new pending history row plus a generated job;
  replay preserves the stable event ID and exact body. Unit and real-Postgres
  tests cover private/special IPv4 and IPv6, metadata, alternate
  numeric forms, mixed DNS, rebinding, redirect refusal, exact bytes, rotation
  overlap, transactional fan-out, generic worker execution, endpoint ordering,
  retry redaction, role separation, and absence of specialized lifecycle state.
- 2026-07-13 `P0-CI-001`–`P0-CI-002` — upgraded Next.js and its matching
  ESLint preset to `16.2.10`, refreshed Sentry/OpenTelemetry, Hono, URI, YAML,
  Vitest/Vite, WebSocket, HTTP-client, and other transitive fixes, and pinned a
  safe PostCSS override for Next's stale nested version. `npm audit` now reports
  zero production or development vulnerabilities. The flat config now composes
  the official Next core-web-vitals and TypeScript presets before the starter's
  feature-slice constraints; correctness violations remain errors, known
  external-state React compiler migrations are explicit warnings, and generated
  clients/tests have scoped policy. Fixed render-purity, internal navigation,
  and same-feature import findings. ESLint has zero errors, TypeScript passes,
  all 208 Vitest tests pass on Vitest `3.2.7`, and Next `16.2.10` produces all
  40 application routes successfully.
- 2026-07-13 `P0-CI-006` — expanded `.github/workflows/ci.yml` into explicit
  Go static, Go test, protobuf, frontend, and base-integrity gates. All three Go
  modules build and vet independently. Tests run on pinned Go and Codefly CLI
  versions; accounts packages that own real dependency stacks execute in
  isolated `go test` processes to avoid reusing a closed Codefly CLI-server
  connection. Both protobuf services run pinned-Buf canonical format, lint, and
  descriptor builds. Existing monolith package/naming/comment debt is captured
  as narrow, documented v1 exceptions owned by `P1-PROTO-001`–`004`; all other
  rules remain enabled, and the current protovalidate/sidecar-format findings
  were fixed. The frontend builds a commit-pinned `sdk-js` checkout for the
  unpublished file dependency, then runs clean install, ESLint, TypeScript, all
  208 Vitest tests, and the 40-route production build. `actionlint`, each local
  gate, and the canonical base check pass.
- 2026-07-13 `P0-CI-007` — added blocking history-secret, pull-request
  dependency/license, source/configuration, runtime-image, SBOM, independent
  SBOM-vulnerability, and GitHub provenance/attestation gates. All external
  actions and scanner inputs are immutable or version pinned; all four runtime
  images are digest-pinned, non-root, minimal, and locally passed HIGH/CRITICAL
  vulnerability plus secret scans.
- 2026-07-13 `P0-CI-008` — added fail-closed clean-checkout and fixture-stack
  aggregates plus the final `Release gate`, all triggered for `main`, pull
  requests, version tags, and manual runs as appropriate. The browser gate
  bootstraps the pinned Codefly CLI/SDK on an empty runner, builds the frontend
  for production, resolves all ports through Codefly, retains failure evidence,
  and clears the dependency graph unconditionally. Local acceptance passed all
  Go static/integration suites, both Buf graphs, actionlint, canonical integrity,
  npm audit, 208 Vitest tests, the 40-route build, and all 40 Playwright journeys
  against a cold `dev-admin` stack.
- 2026-07-13 `P0-CI-010` — fixed the remaining org-less identity-resolution
  query that bound `""` to `role_assignments.org_id` and aborted PostgreSQL UUID
  parsing before its OR predicate could run. The storage path now selects the
  NULL-org query shape without a UUID argument, retains global-role behavior,
  and checks cursor errors. A real-Postgres regression provisions a user and
  provider identity with no organization, then proves resolution succeeds with
  empty optional org/role fields; the complete infrastructure suite passes.
- 2026-07-13 `P1-PROTO-001`–`P1-PROTO-004` — moved accounts and gateway auth to
  stable, directory-aligned v1 packages; split accounts into 22 bounded-context
  files; preserved every legacy public procedure through exact edge rewrites;
  and activated Buf format/lint/build, prior-release breaking, Codefly
  regeneration, and checked-output gates. CI generation now uses exact local Go
  modules and lockfile-pinned `protoc-gen-es` rather than depending on BSR quota;
  consecutive generations produced identical directory hashes.
- 2026-07-13 `P1-POLICY-001`–`P1-POLICY-003` — added the finite
  `saas.policy.v1.MethodPolicy` extension and all exposure, tenant,
  permission/scope, resource-binding, MFA, audit, idempotency, rate-limit, and
  sensitivity vocabulary. Codefly generated matching Go/TypeScript bindings;
  descriptor identity and extension round-trip tests pass alongside Buf, Go,
  frontend lint/typecheck, 208 Vitest tests, and the 40-route production build.
- 2026-07-13 `P1-POLICY-004`, `P1-CI-003` — annotated all 114 RPCs and moved
  runtime admission, REST mappings, scopes, resource bindings, platform/MFA
  floors, audit events, idempotency, rate class, and sensitivity to descriptor
  options. The generated matrix exposed stale paths and coarse policy drift;
  fail-closed descriptor validation and matrix-current tests now block both.
  Review also found and fixed cross-tenant invitation revocation: the handler
  resolves the invitation's organization under a narrow bypass, requires that
  tenant's admin role, and rejects a foreign UUID before mutation.
- 2026-07-15 `P1-GEN-001`–`P1-GEN-009` — completed the descriptor-driven
  service, route, authorization, REST/OpenAPI, frontend, deployment, and plugin
  catalogs. The final retirement gate removed handwritten raw-gRPC service
  lists and descriptor-equivalent REST YAML; generated registration parity and
  a five-route extension-only collision boundary now prevent either inventory
  from returning.
- 2026-07-18 `P2-SESSION-001` — replaced refresh lookup, revocation, and
  successor insertion across separate transactions with the atomic
  `SessionStore.RotateRefresh` capability. PostgreSQL locks the presented row,
  rolls back consumption if replacement construction or insertion fails, and
  commits user-wide refresh revocation on replay. Concurrent in-memory and
  real-Postgres tests prove one winner and strict reuse handling.
- 2026-07-20 `P2-JOB-007`, `P4-NOTIFY-003` — generated the exact-rendered
  transactional email workload and moved invitation, magic-link, and billing
  delivery onto the common outbox/worker runtime. Product rows and their email
  commands now share the correct tenant or pre-authentication transaction;
  billing enqueue failures retry from the stable Stripe event. Provider calls
  exist only in the email worker, with per-job idempotency, safe failure
  classification, bounded retry, dead letters, and intentional replay. The
  strict renderer rejects missing/malformed variables and escapes HTML values;
  canonical billing copy points to subscription management rather than a
  pricing route. Unit and fresh-PostgreSQL coverage pins generated payloads,
  role authority, rollback atomicity, replay, and end-to-end delivery.
- 2026-07-21 `P0-CI-014` — upgraded the starter and Codefly Next.js factory to
  Next.js 16.2.11 and React/React DOM 19.2.8 with the stable React Compiler;
  removed `next-themes`
  script injection; converted all 40 App Router page/layout boundaries to
  server-first wrappers with focused client islands; and made compiler state
  diagnostics blocking. Codefly core now parses npm 11 workspace-shaped
  `outdated` output, groups coupled upgrades atomically, resolves workspace
  aliases, and reports workspace-only changes. The local Next.js agent packages
  as 0.0.115. Core/agent Go tests, 383 frontend tests, lint/typecheck, the
  40-route production compile, the six-service container build, and the live
  seven-service gateway stack pass.
- 2026-07-23 `P2-WORK-001`–`P2-WORK-004` — added the permissions-plugin-owned
  Work Context authority and made direct RBAC assignments explicitly
  Principal-based. Migrations 78–79 provide monotonic authorization revisions,
  migrate the old `user` database subject to `principal`, and keep the
  deprecated protobuf alias wire-compatible. The generated 25-service /
  125-method catalog now includes Task, root-Session, and child-Session
  capability exchange across gRPC, Connect, REST, OpenAPI, auth metadata, and
  TypeScript. Codefly compile, three RPC tests, two real-Postgres Work Context
  tests, human RBAC E2E, direct Agent authority, cross-tenant RLS, and six
  database role/grant/inventory/policy checks pass.
