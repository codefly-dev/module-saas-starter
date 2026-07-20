# Frontend plugin platform TODO

Date: 2026-07-15
Status: canonical execution backlog

Checkboxes require test or build evidence. Consumer repositories may link their
own implementation issues to these IDs but do not redefine their acceptance.

## Convergence and public boundary

- [x] **FP-001 / P0 — Record packaging ADR.** Package layout, SemVer policy,
  contract scope, composition root, branding, trust model, and framework line.
- [x] **CONV-001 / P0 — Keep one shell.** `AdminLayout` is the only mounted shell.
- [x] **CONV-002 / P0 — Keep one config path.** All adapters consume injected
  `FrontendConfig`.
- [x] **CONV-003 / P0 — Externalize branding.** Starter defaults are neutral;
  application identity lives in `frontend.config.ts`.
- [x] **CONV-003A / P0 — Centralize appearance and theme persistence.** The
  application owns one validated light/dark semantic token preset; the host
  projects it during SSR, applies only a constrained tenant overlay, and uses
  one generated-enum preference controller for both theme selectors.
- [x] **CONV-004 / P0 — Consolidate browser transport.** Browser calls are
  same-origin and duplicate clients are removed.
- [x] **CONV-005 / P0 — Narrow contributions.** `FrontendPlugin` is canonical and
  every retained contribution has a host consumer.
- [x] **CONV-006 / P0 — Unify presentation policy.** One evaluator serves all
  contribution adapters.
- [x] **CONV-007 / P0 — Remove compatibility paths.** Boundary tests prove the
  old host barrels/config aliases are absent and prevent their resurrection.
- [x] **FP-002 / P0 — Freeze public import map.** Exact active/reserved exports
  and forbidden private paths are mechanically enforced.
- [x] **FP-003 / P0 — Use explicit composition.** No scanner, generated registry,
  or side-effect registration remains.
- [x] **FP-004 / P0 — Extract contract/composition package.** Host and neutral
  fixture use public imports; compatibility shims remain temporary.
- [x] **FP-018 / P0 — Remove loose-file discovery.** Only declared imports enter
  `installedPlugins`.

## First product-neutral vertical slice

- [ ] **FP-005 / P0 — Extract the first consumer package.** Move only one overview
  route, one dashboard widget, and their repository/controller. Current proving
  consumer: Warden. Acceptance: independent package build plus host render tests
  through public imports.
- [x] **FP-006 / P0 — Define logical service requirements.** Alias, implemented
  protocol, safe route prefix, and backend contract compatibility are serializable,
  inventoried, and validated. No deployment URL enters the manifest.
- [x] **FP-007 / P0 — Generate constrained allowlist.** Produce deterministic
  server-only routing data from installed service requirements and application
  Codefly bindings.
- [x] **FP-007A / P0 — Converge bindings with Codefly service dependencies.**
  An application-owned product binding must generate or validate the matching
  private server endpoint dependency without editing a guarded base service
  manifest. Starter-only output remains product-neutral.
- [ ] **FP-007B / P0 — Synthesize cross-module production network policy.**
  Join installed external targets with workspace-level target namespace/port
  inventory. Emit least-privilege egress/ingress without product-specific
  Starter defaults or broad private-network exceptions.
- [x] **FP-008 / P0 — Implement same-origin plugin BFF.** Enforce identity,
  tenant, methods, headers, limits, timeout, redirects, trace, and stable errors.
- [ ] **FP-009 / P0 — Move generated-client ownership.** Generator, inputs,
  outputs, commands, and drift tests belong to the product package.
- [ ] **FP-010 / P0 — Prove install/uninstall.** Starter-only, enabled,
  unavailable-backend, and removed states pass without edits below starter `src/`.
- [x] **FP-010A / P0 — Support consumer package metadata reproducibly.** Permit
  additive product dependencies/workspaces and lockfile regeneration without
  weakening checks for Starter-owned package scripts/dependencies; make Docker
  `npm ci`, base integrity, and `codefly verify` enforce the same policy.
- [ ] **FP-010B / P0 — Pin the generic Next.js substrate.** The prepared
  `nextjs` 0.0.110 agent removes its competing plugin registry, supports
  application-owned workspaces, and passes generated-service/container gates.
  Acceptance requires an immutable agent release, an exact Starter topology
  pin, regenerated manifests, and clean Starter verification; `latest` is not
  an acceptable release input.

## Contract and package hardening

- [ ] **FP-011 / P0 — Define package ownership.** CODEOWNERS/maintainers for each
  public package and first-party consumer. Starter packages are protected and
  Warden ownership is defined; the Warden repository CODEOWNERS entry remains
  consumer-side acceptance under FP-005/FP-010.
- [x] **FP-012 / P0 — Split serializable metadata from React contributions.**
  Contract v2 is React-free and JSON-safe; the React package binds every
  declared route/widget ID exactly once and exposes the pure metadata inventory.
- [x] **FP-012A / P0 — Delete `admin-core` and compatibility barrels.** The old
  admin config, hook alias, private contract/composition barrels, and parallel
  framework plugin interface are removed and guarded by boundary tests.
- [x] **FP-012B / P0 — Activate the public React service runtime.** The host
  injects one closure-backed runtime; products use public hooks and a fixed
  same-origin `(plugin, alias)` transport without token or destination access.
- [x] **FP-013 / P0 — Add `definePlugin`.** Preserve literals and improve product
  diagnostics without a cast.
- [ ] **FP-014 / P1 — Namespace contribution IDs.** Routes, widgets, commands,
  slots, services, and permissions receive stable normalization rules.
- [ ] **FP-015 / P1 — Define compatibility ranges.** Distinguish SDK build-time
  compatibility from backend runtime compatibility.
- [x] **FP-016 / P1 — Check public API extraction.** Exact exports and entrypoints
  fail tests on accidental changes.
- [x] **FP-017 / P1 — Close marked compatibility re-exports.** Starter-owned
  compatibility exports are gone; consumer cleanup remains tracked by FP-068.
- [ ] **FP-019 / P1 — Produce reproducible installed inventory.** Stable order,
  input digest, package versions, contributions, and service requirements.
- [ ] **FP-020 / P1 — Validate before dev/test/build with actionable diagnostics.**

## Runtime, permissions, and isolation

- [x] **FP-030 / P0 — Resolve server-only Codefly bindings.**
- [x] **FP-031 / P0 — Specify forwarded identity and tenant semantics.**
- [x] **FP-032 / P0 — Certify proxy allowlist and traversal defenses.**
- [x] **FP-033 / P0 — Add auth/tenant contract matrix.** The canonical matrix
  separates opaque-token/header guarantees owned by the runtime and BFF from
  backend claim, method-policy, tenant, platform-role, delegation, and resource
  authorization. Host rows are executable; every consumer must run the backend
  rows with two real tenants before its supported configuration is released.
- [x] **FP-034 / P1 — Add safe request correlation.** Every BFF attempt owns a
  fresh opaque `x-request-id`; caller/backend correlation headers cannot
  override it, host problems repeat it consistently, retries receive new IDs,
  and the public failure mapper trusts only the bounded host response header.
- [ ] **FP-035 / P1 — Add transport observability.**
- [ ] **FP-036 / P1 — Retire direct production product URLs.**
- [x] **FP-043 / P1 — Define backend capability response.** A versioned
  protobuf defines one fixed REST/Connect handshake; the BFF strictly validates
  and normalizes it against the installed `{contract, major}`, and each
  contribution probes all owning services before rendering.
- [ ] **FP-045 / P1 — Introduce namespaced semantic permissions.**
- [ ] **FP-046 / P1 — Apply permissions consistently to every surface/action.**
- [x] **FP-047 / P1 — Resolve loading/ready/unavailable/incompatible/failed.**
  The public runtime maps stable BFF problems to non-sensitive failure
  descriptors; host Suspense/render adapters own the five canonical states.
- [x] **FP-048 / P1 — Add route and widget isolation boundaries.** Every plugin
  route and every widget has an independent retryable error boundary; tests
  prove an unavailable or throwing contribution cannot break a sibling/shell.

## Testkit, documentation, and release

- [ ] **FP-050 / P1 — Publish manifest certification helpers.**
- [ ] **FP-051 / P1 — Publish parsed import-boundary tooling.**
- [ ] **FP-052 / P1 — Publish product render harness.**
- [ ] **FP-053 / P1 — Build a neutral all-contributions reference plugin.**
- [ ] **FP-054 / P1 — Run the supported configuration matrix.**
- [ ] **FP-055 / P1 — Run one first-party full browser journey.**
- [ ] **FP-064 / P1 — Write plugin author guide.**
- [x] **FP-065 / P1 — Write generic install/uninstall guide plus consumer examples.**
- [ ] **FP-066 / P1 — Publish compatibility and deprecation policy.**
- [ ] **FP-067 / P1 — Migrate all known consumers.**
- [ ] **FP-068 / P1 — Remove compatibility shims and obsolete consumer paths.**
- [ ] **FP-069 / P1 — Publish reproducible v1 packages and release inventory.**

## Release gates

- [ ] Starter-only lint, typecheck, tests, production build, and browser smoke.
- [ ] First-party product packages build/test independently using public imports.
- [ ] Install and uninstall change only dependencies and application config.
- [ ] Production browser bundles contain no backend URL, binding secret, token,
  or private host module.
- [ ] Unsafe, anonymous, unauthorized, expired, cross-org, and cross-tenant BFF
  requests fail closed.
- [x] Missing/incompatible backends and throwing contributions remain local.
- [ ] Exactly one shell, config interface, manifest inventory, and transport
  policy serve every product.
- [ ] Generated output, package contents, and supported-version matrix are
  reproducible before v1.
