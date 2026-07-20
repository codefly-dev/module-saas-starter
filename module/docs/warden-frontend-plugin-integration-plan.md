# Warden frontend plugin integration plan

Date: 2026-07-16
Status: implemented in `warden-platform`; live dogfood verified; remaining release gates tracked below
Platform authority: this repository's generic frontend-plugin ADR, public map,
architecture, BFF contract, and TODO remain authoritative

## Purpose

Integrate one deliberately small Warden frontend package into the current SaaS
Starter plugin platform without copying Warden behavior into the generic host or
reintroducing a consumer fork of the Starter.

The first proof is exactly:

- one Warden Overview route;
- one Warden traffic widget;
- the traffic and recent-calls read repositories/controllers needed by that UI;
- one logical Warden backend requirement;
- starter-only, installed, unavailable-backend, incompatible-backend,
  live-backend, and uninstalled verification.

This is the execution plan for a Warden-side agent. It is intentionally more
specific than the generic implementation plan, but it does not create a
Warden-specific exception to the public SDK.

## Execution checkpoint (2026-07-16)

The integration has converged on one direction: SaaS Starter owns the generic
host, public plugin SDK, explicit composition root, BFF, and generated Codefly
dependency seam; Warden owns a compile-time product package containing its
models, repositories, controllers, views, manifest, and product tests.

Completed:

- [x] Install one explicit Warden package without a scanner or private Starter
  import.
- [x] Route browser traffic only through the host BFF and the generated logical
  `(warden, api) -> (platform, warden, rest)` Codefly binding.
- [x] Restore the Warden operator surfaces as package-owned routes, navigation,
  widgets, call/session details, and product formatting components.
- [x] Make request/response evidence primary, format JSON/Markdown/text by
  detected content, and keep the Warden decision path collapsed as debug data.
- [x] Persist calls, captures, pricing, session IDs, and work-unit IDs in the
  shared Warden storage, with authenticated tenant-scoped console APIs.
- [x] Dogfood both a direct LLM request and a Claude Code session through the
  gateway with governed request/response capture and spend data.
- [x] Keep the local fixture to one Codefly organization, one super-admin user,
  and zero teams.
- [x] Verify frontend typechecking, the integrated frontend suite, touched Rust
  and Go suites, and canonical base integrity in the Warden consumer.
- [x] Run the local Phase 8 gates: plugin package build, generated dependency
  checks, lint, typecheck, tests, production Next build, `codefly verify`, and
  whitespace checks.
- [x] Align the installed Warden copy with the current canonical Starter
  topology, regenerate the protected base manifest, and prove `1048` protected
  files intact with `40` additive Warden files and no base edits.
- [x] Build and run the coordinated local agent set (`go-grpc 0.1.9`, `nextjs
  0.0.112`, Redis `0.0.72`, Postgres `0.0.101`, S3 `0.0.14`, Vault `0.0.13`),
  then re-run a Claude Code session through the gateway. Session
  `6e46bc48-57e6-4a89-a5c9-99417f72bb0e` has one priced call and a complete,
  consent-verified JSON request/event-stream response capture.
- [x] Pin Starter application Go modules to registry-available Core `v0.2.18`
  and SDK `v0.1.42`, and prove the Warden auth, infrastructure, billing, and
  catalog packages with `GOWORK=off` while resolving runtime services through
  the Codefly SDK.

Remaining release TODO:

- [ ] Move the Warden package to Starter contract `2.1.0` and React package
  `0.4.1`: keep its manifest JSON-safe, register lazy routes/widgets separately
  with `defineReactPlugin`, and compose through `defineReactFrontend`.
- [ ] Generate Warden backend bindings from the capability proto shipped in
  contract `2.1.0`, implement the fixed REST capability response for
  `warden.console` major `1`, and prove both compatible and incompatible gates.
- [ ] Publish the prepared `codefly` JS SDK `0.0.29` release, then advance the
  frontend dependency from `0.0.28`. The current registry release omits usable
  declarations and predates `getCurrentFixture`; an app-local type/runtime shim
  is deliberately rejected.
- [ ] Prove the clean-install/container subset of Phase 8 with a fresh `npm ci`
  and frontend container build after the SDK release is available. The build
  already passes clean resolution, plugin package compilation, and generated
  service-dependency verification before stopping at the missing SDK types.
- [ ] Publish the coordinated agent release stream after Core `v0.2.21` (and
  SDK-Go `v0.1.44` where the Go service template requires it) is available,
  then publish the exact agent revisions validated above. Native local builds
  are green through `codefly agent build`; remote/clean consumers cannot fetch
  these versions until those releases exist.
- [ ] Record explicit starter-only and uninstall/reinstall lifecycle proofs;
  installed and backend-error behavior already have automated coverage.
- [ ] Complete every applicable `AT-*` product-backend row in the canonical
  authentication/tenant matrix, plus the backend-down proof from Phase 7. This
  requires two real organizations and real foreign resource IDs.
- [ ] Finish generated-client ownership task `FP-009` if Warden keeps a client
  generator beyond the current package-local contract.
- [ ] Cut immutable Starter and Warden revisions after the existing dirty-tree
  work is split into reviewable commits.

The phase-by-phase material below remains the audit trail and release checklist;
it is no longer an unstarted agent handoff.

## Read first

The Warden agent must read these canonical documents from the exact SaaS Starter
revision being integrated:

1. [Convergence brief](frontend-plugin-convergence-brief.md)
2. [Architecture](frontend-plugin-architecture.md)
3. [Implementation plan](frontend-plugin-platform-implementation-plan.md)
4. [Platform TODO](frontend-plugin-platform-todo.md)
5. [Packaging ADR](adr/0001-frontend-plugin-packaging.md)
6. [Public API](frontend-plugin-public-api.md)
7. [BFF contract](frontend-plugin-bff-contract.md)
8. [Authentication and tenant matrix](frontend-plugin-auth-tenant-matrix.md)
9. [Request correlation](frontend-plugin-request-correlation.md)
10. [Install and uninstall procedure](frontend-plugin-installation.md)
11. [Ownership and review policy](frontend-plugin-maintainers.md)

The Warden repository's older
`modules/platform/services/warden/code/SAAS-STARTER-MIGRATION.md` is historical
context only. In particular, its loose-file auto-discovery decision has been
superseded by explicit `frontend.config.ts` composition.

## Non-negotiable decisions

1. SaaS Starter is the host and SDK source of truth. Warden consumes it.
2. Warden remains a trusted compile-time dependency; there is no runtime remote
   JavaScript loading.
3. Installation is explicit. There is no directory scanner, generated plugin
   registry, or side-effect registration.
4. Warden product code imports only active public package entry points:
   `@codefly/saas-plugin-contract`, `@codefly/saas-plugin-react`, and
   `@codefly/saas-plugin-react/runtime`. Backend capability adapters use
   `@codefly/saas-plugin-contract/capabilities`. Warden may use the generic
   containment primitive from `@codefly/saas-plugin-react/ui`, but must not
   replace the Starter-owned route/widget boundary.
5. Warden code does not import `@/`, `src/`, `app/`, `features/`, `plugins/`,
   `lib/`, or `components/` from the Starter.
6. Browser code never reads `NEXT_PUBLIC_WARDEN_REST`, chooses a backend origin,
   imports the host token store, or creates an alternative authenticated fetch.
7. The browser calls only `/api/plugins/warden/api/{safe-relative-path}` through
   the injected runtime. The host BFF resolves the concrete Codefly endpoint.
8. The Warden backend validates the forwarded bearer, derives tenant and user
   authority from it, and remains the authorization boundary.
9. Demo fixtures never silently replace a failed live backend. Package tests may
   inject fixtures or MSW handlers; production live mode fails visibly.
10. The generic `/pricing` page stays removed. Warden's product-specific spend
    and cost analytics are separate domain surfaces and are not the SaaS pricing
    route.

## Repositories and ownership

Use variables in notes and shell transcripts so paths remain unambiguous:

```sh
STARTER_ROOT=/Users/antoine/development/deus/codefly/module-saas-starter
WARDEN_ROOT=/Users/antoine/development/deus/warden-platform
STARTER_MODULE="$STARTER_ROOT/module"
WARDEN_SAAS="$WARDEN_ROOT/modules/saas"
WARDEN_FRONTEND="$WARDEN_SAAS/services/frontend/code"
```

| Owner | Paths |
| --- | --- |
| SaaS Starter agent | Generic files under `$STARTER_MODULE`, public packages, host runtime/BFF, install seams, canonical tests and docs |
| Warden agent | Warden product package, Warden application `frontend.config.ts`, Warden service binding, Warden tests and Warden documentation |
| Generated | Product package `dist`, root `package-lock.json`, plugin allowlist, Codefly service manifests/endpoints, build output |
| Never Warden-owned | Starter `src/`, public SDK package implementations, host auth/token code, host BFF policy, shell/providers |

The Warden agent must treat the Starter repository as read-only. If the slice
needs a new generic primitive, stop that part, write a minimal API request with
an acceptance test, and hand it back to the Starter agent.

## Current Warden state observed on 2026-07-16

The current Warden tree is transitional, not a clean baseline:

- both repositories have large dirty worktrees;
- Warden still has `scripts/generate-plugin-registry.mjs` and
  `src/plugins/registry.generated.ts`;
- Warden still has both `AdminLayout` and `AppShell` paths;
- `project-plugins/warden.ts` imports the private `@/lib/plugins/contracts`;
- Warden UI imports private `@/shared/ui`, `@/lib/utils`, and other host paths;
- `project-plugins/lib/warden/api.ts` still reads
  `NEXT_PUBLIC_WARDEN_REST`, permits a product-selected URL, and calls a
  host-private `fetchPluginApi`;
- the Warden copy's `src/lib/plugins/runtime.ts` currently accepts an arbitrary
  URL and permits an explicit product bearer; that implementation is replaced
  by the new public runtime;
- Warden's client generator currently emits a client with configurable
  `baseUrl`, bearer, and API-key inputs inside the host frontend tree;
- Warden's old SaaS copy still contains the generic `/pricing` feature;
- partial new runtime adapter files are present, but the Warden package manifest
  and public SDK dependencies are not installed in its root `package.json`;
- the Warden frontend service already declares a direct `platform/warden`
  dependency, but it does so as an inline difference from the old base.

Do not "clean up" this state with reset, checkout, mass deletion, or a blind
copy. Preserve every unrelated Warden change.

## Hard prerequisites in SaaS Starter

FP-007A and FP-010A are implemented in the Starter hardening described below.
The remaining prerequisite is an immutable reviewed Starter revision containing
those changes. The live Warden and production network proofs remain consumer
gates, not assumptions.

### Prerequisite A: immutable Starter input

The validated Starter changes currently exist in a dirty development tree. The
Warden agent needs an immutable commit, tag, release, or reviewed patch series
that includes at least:

- `@codefly/saas-plugin-contract` 2.1.0, including its published capability
  proto and `./capabilities` entry point;
- `@codefly/saas-plugin-react` 0.4.1;
- `frontend-plugin-public-api.json`;
- explicit `frontend.config.ts` composition;
- service inventory and deterministic allowlist generation;
- generated frontend Codefly dependencies and their Node build drift gate;
- additive `packages/*` install, semantic lock validation, and Docker workspace
  installation;
- the canonical `frontend-plugin-installation.md` lifecycle procedure;
- the server-only Codefly endpoint resolver and generic BFF route;
- the host `PluginRuntimeProvider` adapter;
- one-shell/config/presentation convergence;
- `/pricing` removal and `/admin/billing` subscription navigation;
- current frontend boundary tests and base manifest.

Record the immutable identifier in the Warden PR. Do not identify the source by
`HEAD` if the Starter worktree is dirty.

### Prerequisite A2: immutable Next.js agent substrate (`FP-010B`)

The generic Codefly `nextjs` 0.0.112 cleanup is prepared and locally validated,
but an uncommitted/local agent is not a Warden integration input. Before the
Warden derivation:

1. review, commit, tag, and publish `service-nextjs` 0.0.112;
2. confirm its release contains the neutral `packages/*` workspace seam, no
   template-local plugin contract/registry, Next.js 16.2.10, a digest-pinned
   Node builder, clean lock-derived container dependencies, and matching
   `nextjs:nodejs` UID/GID 1001 deployment policy;
3. keep the Starter topology pinned exactly to `0.0.112`;
4. regenerate `module.codefly.yaml`, the frontend service manifest, topology
   inventory, network policy, and base integrity manifest;
5. rerun Starter frontend, catalog, Docker, and `codefly verify` gates; and
6. record both immutable identifiers in the Warden PR.

This release supplies only the Next.js framework/build substrate. Do not move
`@codefly/saas-plugin-*`, `FrontendPlugin`, the host BFF, or product composition
into the service agent.

### Prerequisite B: consumer dependency/install integrity (`FP-010A`)

This prerequisite is implemented by the protected root `packages/*` seam. Put
the Warden package below `services/frontend/code/packages/`; do not add a
Warden dependency or workspace entry to the root `package.json`. Warden's own
`package.json` is the additive package metadata and npm regenerates the root
`package-lock.json`.

Base integrity keeps the root manifest/scripts/dependencies protected while
semantically validating the lock's root entry, exact workspace set, each
workspace's dependency metadata, and npm workspace links. Docker copies all
workspace sources before `npm ci` and builds every workspace with a build
script. This supports the exact inverse on uninstall.

Do not solve this in Warden with a silent base-integrity allowlist or by omitting
the package from dependency metadata.

### Prerequisite C: Codefly endpoint dependency convergence (`FP-007A`)

`frontend.config.ts` can map `(warden, api)` to `(platform, warden)`, but the BFF
receives a concrete endpoint only when the frontend service declares the
corresponding Codefly REST dependency. The canonical Starter-only manifest
declares Accounts only.

FP-007A now uses the generated service allowlist as the generic application
input to the strict frontend service-manifest compiler. Run:

```sh
npm run generate:plugin-codefly-dependencies
```

The Warden-enabled output must add exactly one grouped dependency:

```yaml
- name: warden
  module: platform
  endpoints:
    - name: rest
```

The compiler guarantees are:

- no Warden name or target is added to canonical Starter defaults;
- starter-only output is unchanged;
- a consumer binding generates `platform/warden/rest` server-side;
- install and uninstall regenerate cleanly without an inline edit to a guarded
  base manifest.

The Warden live gate must still prove that the expected private
`CODEFLY__ENDPOINT__...__REST` value reaches the frontend server but never the
browser, and that a missing runtime endpoint yields the contained
`503 backend_unavailable` response.

`prepare:frontend` checks this convergence during dev, test, and build without
a Go toolchain. Do not retain Warden's current handwritten difference in
`services/frontend/service.codefly.yaml`; regeneration replaces it.

Production cross-module NetworkPolicy synthesis remains FP-007B because the
Starter does not own Warden's target namespace/port inventory. The Warden live
deployment must prove that policy separately; do not add broad private-network
egress to the generic Starter.

## Target architecture

```text
@warden/saas-frontend-plugin
  manifest (overview route + traffic widget + service requirement)
    -> imported explicitly by Warden frontend.config.ts
      -> SaaS Starter composition and contribution outlets

Warden view
  -> Warden TanStack Query controller
    -> Warden repository
      -> usePluginService("warden", "api")
        -> /api/plugins/warden/api/{traffic|calls}
          -> generated installed-service allowlist
            -> private Codefly platform/warden/rest endpoint
              -> /api/v1/plugins/console/{traffic|calls}
                -> Warden bearer validation + tenant authorization
```

No layer before the host resolver contains a Warden deployment URL.

### Required Warden capability implementation

Use the exact proto shipped by the immutable Starter contract package:

```text
node_modules/@codefly/saas-plugin-contract/
  proto/saas/frontend/plugin/v1/capabilities.proto
```

Feed that source into Warden's Codefly-owned protobuf generation configuration;
do not create a handwritten Rust, Go, or TypeScript response type. Because the
installed Warden alias uses REST, the Warden service must answer:

```text
GET /.well-known/codefly/frontend-plugin-capabilities
```

with the ProtoJSON equivalent of:

```json
{
  "schemaVersion": 1,
  "contract": "warden.console",
  "contractMajor": 1,
  "capabilities": ["calls.read", "traffic.read"]
}
```

Keep the list sorted and advertise only implemented behavior. This fixed
operation returns no tenant, user, deployment, storage, policy, model, or
endpoint data and requires no Warden domain permission. It may validate the
forwarded bearer consistently with the service edge, but any authenticated SaaS
operator who can load the plugin must be able to obtain this non-sensitive
metadata. The Starter BFF validates and normalizes the response before the
browser sees it.

Add Warden-side tests for the generated message, exact fixed path, schema and
contract constants, sorted unique IDs, absence of sensitive fields, and a live
request through the service router. Starter-side tests already own mismatch and
containment behavior; do not duplicate or patch the host implementation.

## Target Warden package

Recommended initial location inside the frontend npm/Docker context:

```text
modules/saas/services/frontend/code/packages/warden-frontend-plugin/
├── package.json
├── tsconfig.json
├── vitest.config.ts
├── README.md
├── src/
│   ├── index.ts
│   ├── manifest.ts
│   ├── model/
│   │   ├── call.ts
│   │   ├── traffic.ts
│   │   └── schemas.ts
│   ├── repository/
│   │   ├── problem.ts
│   │   ├── repository.ts
│   │   └── runtime-repository.ts
│   ├── controller/
│   │   ├── query-keys.ts
│   │   └── use-overview.ts
│   └── ui/
│       ├── overview-page.tsx
│       ├── traffic-widget.tsx
│       ├── calls-table.tsx
│       └── states.tsx
└── test/
    ├── manifest.test.ts
    ├── public-boundary.test.ts
    ├── repository.test.ts
    ├── overview-page.test.tsx
    └── traffic-widget.test.tsx
```

Suggested package identity: `@warden/saas-frontend-plugin`, version `0.1.0`,
private until its ownership and release channel are decided. Use exact sibling
SDK versions during the slice.

The package should depend on the public SDK packages and use peer dependencies
for React and the host-provided TanStack Query line so only one query context is
mounted. Avoid a Next.js dependency in the first slice by using semantic links
or an injected/public navigation primitive; do not import Starter-private Link
wrappers.

The package must have its own build and tests. A Next host build is not evidence
that it builds independently.

## Proposed manifest

The first package contributes only the proving slice:

```tsx
import { lazy } from "react";
import {
  FRONTEND_PLUGIN_CONTRACT_VERSION,
  definePlugin,
} from "@codefly/saas-plugin-contract";
import { defineReactPlugin } from "@codefly/saas-plugin-react";

export const wardenFrontendPluginManifest = definePlugin({
  contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
  name: "warden",
  navigation: { label: "Warden", placement: "primary", priority: 10 },
  navItems: [
    {
      label: "Overview",
      href: "/admin/warden",
      icon: "Shield",
      group: "Observe",
      requiredRole: "admin",
      order: 10,
    },
  ],
  services: [
    {
      alias: "api",
      protocol: "rest",
      routePrefix: "/api/v1/plugins/console",
      compatibility: { contract: "warden.console", major: 1 },
    },
  ],
  routes: [
    {
      id: "overview",
      path: "/admin/warden",
      requiredRole: "admin",
    },
  ],
  widgets: [
    {
      id: "warden-traffic",
      slot: "dashboard.widgets",
      requiredRole: "admin",
      priority: 10,
    },
  ],
});

export const wardenFrontendPlugin = defineReactPlugin({
  manifest: wardenFrontendPluginManifest,
  routes: [
    {
      id: "overview",
      component: lazy(() =>
        import("./ui/overview-page.js").then((module) => ({
          default: module.WardenOverviewPage,
        })),
      ),
    },
  ],
  widgets: [
    {
      id: "warden-traffic",
      component: lazy(() => import("./ui/traffic-widget.js")),
    },
  ],
});
```

`wardenFrontendPluginManifest` must round-trip through JSON. React components
exist only in `wardenFrontendPlugin`; every metadata ID has exactly one binding.

Do not copy the other Warden routes into this first manifest. Their migration
begins only after this slice passes all five lifecycle states.

## Repository and controller rules

The live repository accepts `PluginServiceTransport`; it does not accept a base
URL, bearer, token getter, generic fetch, or arbitrary `RequestInfo`:

```ts
import {
  pluginErrorFromResponse,
  type PluginServiceTransport,
} from "@codefly/saas-plugin-react/runtime";

export async function readTraffic(
  service: PluginServiceTransport,
): Promise<TrafficSummary> {
  const response = await service.request("traffic", {
    headers: { accept: "application/json" },
  });
  if (!response.ok) throw await pluginErrorFromResponse(response);
  return decodeTrafficResponse(response);
}
```

The controller obtains the injected service once and owns Warden query keys:

```ts
const service = usePluginService("warden", "api");

const traffic = useQuery({
  queryKey: ["warden", "overview", "traffic"],
  queryFn: () => readTraffic(service),
  refetchInterval: 5_000,
});
```

Requirements:

- validate response shape before rendering; do not cast arbitrary JSON directly
  to domain types;
- keep traffic and calls query keys inside the Warden package;
- distinguish loading, empty, unavailable, unauthorized, malformed-response,
  and generic failure states;
- use `pluginErrorFromResponse` for generic BFF problems instead of duplicating
  problem parsing or displaying endpoint details;
- treat BFF `404 plugin_service_not_found` as installation/config drift;
- treat BFF `503 backend_unavailable` as installed but unavailable;
- treat `502`/`504` as retryable contained backend failures;
- do not convert a live failure into fixture data;
- keep the Overview route and widget independently containable—a failed widget
  must not throw through the dashboard shell. Configure the query/view to
  rethrow `PluginAvailabilityError` when the generic host fallback should own
  the experience; keep Warden-specific empty and authorization states local.

For this slice, use only Warden console routes `traffic` and `calls`, which are
both under the declared `/api/v1/plugins/console` prefix. Do not widen the prefix
to `/api/v1` merely to accommodate future Warden pages.

## UI ownership for the first slice

`@codefly/saas-plugin-react/ui` now exposes only the generic
`PluginErrorBoundary` and safe failure types. The Starter already applies it to
every route/widget and owns its visual fallback. Therefore:

- do not import `@/shared/ui`, `@/components`, or `@/lib/utils`;
- do not ask the Starter to publish its whole component library for Warden;
- move the smallest Warden-specific stat, table, badge, loading, and error
  rendering into the product package;
- use semantic HTML, the host's public CSS variables/Tailwind contract where
  stable, and product-owned presentation classes;
- use `pluginErrorFromResponse` so backend availability reaches the host-owned
  boundary without leaking raw response details;
- keep any copied product primitive visibly Warden-owned; shared extraction is a
  later cross-product decision.

The existing Overview depends on `CallsTable`, `PageHeader`, `StatCard`,
`KindBadge`, error state, formatting helpers, and host Cards. Reimplement only
the subset required for the slice rather than moving the entire Warden UI
primitive library.

## Generated client ownership

The current `scripts/generate-warden-client.mjs` and generated
`project-plugins/lib/warden/generated/control-plane.ts` are in the host tree and
emit a client that controls `baseUrl`, bearer, and API key. Do not use that
client for the first runtime slice.

For the first slice:

- define and validate only the traffic/calls response contracts in the Warden
  package;
- preserve a test against the Warden OpenAPI or Rust-owned schema if practical;
- do not make full generated-client migration a prerequisite for Overview.

Under `FP-009`, follow with a Warden-owned generator that emits DTO/operation
types or a client parameterized by `PluginServiceTransport`, never by origin or
credentials. Generator input, command, output, digest, and drift test all move
with the Warden package.

## Detailed execution sequence

### Phase 0 — checkpoint and record baselines

1. Confirm no other agent owns overlapping Warden frontend paths.
2. Record `git status --short` in both repositories.
3. Record the Starter immutable revision/release and Warden starting revision.
4. Because Warden is heavily dirty, obtain approval for a checkpoint branch and
   commit, or create a separately reviewable worktree/patch archive. Never stash,
   reset, or overwrite another agent's changes.
5. Record `codefly version`; the audited CLI was 0.1.8.
6. Run current Warden frontend tests and record failures before migration.
7. Run `codefly verify` and record existing divergence. Do not "make it green"
   by expanding the consumer allowlist.

Exit: reproducible before-state and a recovery path exist.

### Phase 1 — materialize the new Starter in isolation

The current CLI's `codefly sync` synchronizes service dependencies; it is not a
general module-refresh command. Do not assume it updates `modules/saas`.

Preferred flow:

1. Publish/install or locally build the `saas-starter` module agent from the
   exact immutable Starter revision.
2. In a scratch workspace or isolated worktree, materialize a fresh module with
   `codefly add module --agent saas-starter --yes` (add `--local-agents` only
   when the installed local agent is proven to be that exact revision).
3. Verify that fresh output against its shipped base manifest.
4. Compare the fresh module to Warden's `modules/saas` and classify every Warden
   difference as:
   - application-owned input;
   - Warden side-addition/product code;
   - generic improvement already present in the new Starter;
   - obsolete fork to discard;
   - unresolved change requiring explicit owner review.
5. Re-derive into the Warden integration worktree only after the classification
   is reviewed. Do not recursively copy the dirty canonical development folder
   over Warden.

The new base should replace old base implementations as a unit. Do not
cherry-pick only the React runtime while retaining old scanner/shell/config
forks.

Exit: fresh Starter-only Warden worktree builds before Warden registration.

### Phase 2 — preserve only consumer-owned inputs

Reapply or author, against the new shapes:

- Warden's `modules/saas/module.codefly.yaml` identity/composition;
- Warden application branding in the new `frontend.config.ts`;
- workspace configuration and deployment inputs;
- the approved generated external service dependency input from FP-007A;
- the product package as a side-addition;
- Warden-owned tests and docs.

Do not recreate removed or protected paths such as:

- `src/lib/admin-core.ts`, `admin-config.ts`, providers, auth, token transport;
- `AdminLayout`, `AppShell`, slots, command palette, built-in plugins;
- loose registry generator/output;
- BFF route/resolver/policy;
- public SDK package implementations;
- `/pricing` files or navigation.

Exit: `codefly verify` recognizes the base plus explicit application-owned
inputs/side-additions, with no unexplained modified base file.

### Phase 3 — scaffold and independently build the Warden package

1. Add `services/frontend/code/packages/warden-frontend-plugin` and its exact
   package metadata. Leave the protected root `package.json` unchanged.
2. Add build, typecheck, package test, and public-boundary tests.
3. Add the Warden package path to the Warden repository's `.github/CODEOWNERS`
   with the product maintainer named by the canonical ownership policy.
4. Depend only on public SDK entry points and product-owned dependencies.
5. Add the two response models, schemas, problem mapping, live repository,
   controller, Overview, widget, and minimal product UI.
6. Add the manifest last, after its components and service requirement compile.
7. Prove the package build/test without importing the host application.
8. From the frontend root run `npm install --package-lock-only --ignore-scripts`
   and `npm ci`; review the generated workspace lock entries.

Exit: package build and tests work independently; a boundary scan finds no
Starter-private import, backend URL, `NEXT_PUBLIC_WARDEN*`, token accessor, or
arbitrary destination transport.

### Phase 4 — register explicitly in Warden application config

Starting from the canonical `frontend.config.ts` shape:

```ts
import { wardenFrontendPlugin } from "@warden/saas-frontend-plugin";

export const installedPlugins = [
  auditPlugin,
  coreUsersPlugin,
  platformAdminPlugin,
  wardenFrontendPlugin,
] as const;

export const serviceBindings = [
  {
    plugin: "warden",
    alias: "api",
    target: { module: "platform", service: "warden" },
  },
] as const satisfies readonly FrontendServiceBinding[];
```

Also set Warden application branding and `appearance` overrides there. Warden
may override semantic light/dark tokens, fonts, radius, logo, favicon, and
default theme at the application composition root. Do not add branding, raw
palette utilities, or appearance contributions to the product manifest. The
host owns user theme persistence and the constrained organization overlay.

Run `npm run generate:plugin-codefly-dependencies`. Verify exactly one
allowlist entry:

```json
{
  "plugin": "warden",
  "alias": "api",
  "protocol": "rest",
  "routePrefix": "/api/v1/plugins/console",
  "compatibility": { "contract": "warden.console", "major": 1 },
  "target": { "module": "platform", "service": "warden", "endpoint": "rest" }
}
```

Also verify that the generated frontend `service.codefly.yaml` contains exactly
the `platform/warden/rest` dependency shown under Prerequisite C and retains the
Accounts dependency. Do not patch either generated file.

Exit: explicit import and binding are the only handwritten host-application
registration; generated outputs are deterministic.

### Phase 5 — handle the legacy Warden tree deliberately

The old `project-plugins/warden.ts` cannot remain installed beside the new
manifest because both use plugin name `warden` and contribute duplicate route,
navigation, and widget IDs. Its private imports also violate the new boundary.

For the proving branch:

1. Preserve unmigrated Warden product source in a Warden-owned, non-imported
   location or separate branch/commit.
2. Remove the loose registry from the active build by taking the canonical
   Starter implementation, not by editing its generated output.
3. Activate only the new Overview/widget package.
4. Accept that other Warden console routes are intentionally absent from this
   proving configuration.
5. Do not merge a production feature reduction accidentally. After the slice
   gate, migrate the remaining Warden routes in reviewed batches or make the
   release decision explicit.

Exit: one installed `warden` plugin exists and no legacy product file is
reachable through the active host build.

### Phase 6 — test the five lifecycle configurations

#### A. Starter-only

- no Warden product dependency/package registration;
- no Warden service binding/deployment dependency;
- empty generated plugin service allowlist;
- Starter lint, typecheck, tests, production build, and base integrity pass;
- no `/admin/warden` navigation, route, or widget;
- no generic `/pricing` route.

#### B. Warden installed with controlled success

- product package dependency present;
- one explicit config import and one logical service binding;
- manifest route and widget render through host outlets;
- Warden's fixed capability response advertises schema `1`, contract
  `warden.console`, major `1`, and only sorted Warden capability IDs;
- mocked runtime returns valid traffic/calls;
- navigation and role presentation use the host policy;
- browser requests target only `/api/plugins/warden/api/traffic` and `calls`.

#### C. Warden installed, backend unavailable

- retain package/config/allowlist;
- omit or invalidate only the runtime Codefly endpoint in the test harness;
- BFF returns `503` with `backend_unavailable` and no target detail;
- Overview and widget show contained unavailable/retry states;
- shell, built-in navigation, and unrelated dashboard widgets still work;
- no fixture fallback occurs.

#### D. Warden installed, backend incompatible

- keep the endpoint reachable and the package/config/allowlist installed;
- return a different contract major, malformed ProtoJSON, or omit the fixed
  capability operation in separate cases;
- BFF returns `426 backend_incompatible` with no raw body or endpoint detail;
- Overview and widget show contained incompatible/retry states while the shell
  and unrelated Starter contributions remain ready;
- restore `warden.console` major `1` and prove retry renders without a host
  restart.

#### E. Warden uninstalled

- remove the product dependency/package registration and logical service
  binding/deployment input;
- remove the product workspace directory, run
  `npm install --package-lock-only --ignore-scripts`, `npm ci`, and
  `npm run generate:plugin-codefly-dependencies`;
- make no edit under Starter `src/`;
- output matches the Starter-only behavior again;
- no Warden string or generated route remains in the client bundle.

Exit: all five configurations are automated or reproducibly scripted, not only
described in a PR comment.

### Phase 7 — live Codefly/BFF proof

With the FP-007A generated output present:

1. Run the Warden/SaaS services with the normal Codefly workflow.
2. Confirm the frontend server receives one private endpoint record matching
   module `platform`, service `warden`, endpoint/protocol `rest`; do not print
   its address or bearer in committed logs.
3. Sign in through the normal SaaS host flow.
4. Load the Warden Overview and dashboard widget.
5. Confirm requests go to the same-origin BFF, not the resolved Warden address.
6. Confirm the BFF appends only `traffic` or `calls` below the installed prefix.
7. Confirm Warden validates the forwarded bearer and returns tenant-scoped data.
8. Exercise every applicable backend row in
   [the canonical authentication and tenant matrix](frontend-plugin-auth-tenant-matrix.md):
   expired/revoked token, allowed same-tenant principal, same-tenant principal
   without permission, wrong organization, a real foreign resource ID,
   support, super-admin, and any Warden-supported delegation/impersonation.
   Anonymous/malformed and forged-header rows remain Starter-owned but should
   also be covered by the live chain where practical.
9. Prove an expired bearer on the capability operation returns
   `401 authentication_required`, not `backend_incompatible`, and prove
   backend-down remains `backend_unavailable`.
10. Capture request IDs/statuses only; never capture tokens, cookies, private
   endpoint environment values, or tenant data in artifacts.
11. For two consecutive attempts, prove the browser-visible, Warden-received,
    and host problem request ID agree within each attempt and differ across
    attempts. Caller/backend `x-request-id` or `x-correlation-id` values must
    not replace the Starter-generated ID.

Exit: real traffic proves the complete browser → BFF → Codefly binding → Warden
authorization chain.

### Phase 8 — production and supply-chain gates

From `modules/saas/services/frontend/code`:

```sh
npm ci
npm run build:plugin-packages
npm run check:plugin-codefly-dependencies
npm run lint
npm run typecheck
npm test
npm run build
```

Additionally:

- run the Warden package's independent build/test command;
- run its OpenAPI/schema drift check if included;
- build the frontend container, not only `next build`;
- run `codefly verify` from the Warden workspace;
- scan production client JavaScript for `NEXT_PUBLIC_WARDEN`, backend origins,
  token fixture values, server binding artifact names, and private host paths;
- run `git diff --check`;
- inspect the package tarball or private workspace contents for expected files;
- report existing warnings separately from new errors.

Exit: clean reproducible install/build and no browser leakage.

## Acceptance matrix

| Gate | Starter-only | Installed | Backend unavailable | Backend incompatible | Uninstalled | Live Warden |
| --- | --- | --- | --- | --- | --- | --- |
| Package dependency | absent | present | present | present | absent | present |
| Config registration | absent | one | one | one | absent | one |
| Logical binding | absent | one | one | one | absent | one |
| Allowlist entry | zero | one | one | one | zero | one |
| Warden endpoint env | absent | mocked | absent/invalid | reachable | absent | one private server value |
| Capability handshake | absent | exact major 1 | unreachable | wrong/malformed | absent | exact major 1 |
| Overview route | absent | ready | contained unavailable | contained incompatible | absent | tenant-scoped ready |
| Traffic widget | absent | ready | contained unavailable | contained incompatible | absent | tenant-scoped ready |
| Direct product origin | none | none | none | none | none | none |
| Starter `src/` edits | none | none | none | none | none | none |
| Fixture fallback | n/a | explicit test only | forbidden | forbidden | n/a | forbidden |
| Auth/tenant matrix | host rows | mocked product rows | n/a | n/a | host rows | all applicable `AT-*` rows with two tenants |
| Request correlation | fresh host ID | same ID through mock backend | fresh ID | fresh ID | fresh host ID | same host ID in browser/BFF/Warden logs; new ID on retry |

## Boundary assertions the Warden package must add

Fail the package test if production source contains:

- an import beginning with `@/`;
- relative escape into Starter `src`, `app`, `features`, `plugins`, `lib`, or
  `components`;
- `NEXT_PUBLIC_WARDEN`, `NEXT_PUBLIC_API_*`, or `NEXT_PUBLIC_BACKEND_URL`;
- `http://`, `https://`, `localhost`, or a Codefly endpoint environment name;
- `token-store`, `getToken`, `Authorization` construction, cookies, identity,
  tenant, forwarding, or `x-*` trusted request headers;
- `fetchPluginApi`, the old generated registry, or the old host runtime;
- direct imports from unlisted SDK deep entry points.

Allow product-relative API route literals only in the manifest's installed
`routePrefix`; repositories pass safe relative segments to the runtime.

## Commit/PR structure

Keep the work reviewable:

1. **Warden checkpoint/inventory** — no behavior change.
2. **Fresh Starter convergence** — no Warden package yet.
3. **Warden package model/repository/tests** — independent, no host registration.
4. **Warden Overview/widget UI and manifest**.
5. **Explicit config, binding, generated artifacts**.
6. **Lifecycle/live tests and legacy proving-branch retirement**.

Do not combine unrelated Warden Rust/backend changes with the host convergence
commit. Do not rewrite another agent's dirty files to make the diff smaller.

## Handoff required from the Warden agent

```text
Task IDs: FP-005, FP-009 (only if generator moved), FP-010
Starter source revision/release:
Warden start revision/checkpoint:
Canonical decisions changed: no | yes (link to Starter ADR/change)
Owned files changed:
Legacy files preserved/retired:
Behavior delivered:
Lifecycle configurations proven:
Live BFF/backend evidence:
Tests/checks run and results:
Generated artifacts changed:
Base-integrity/codefly verify result:
Remaining risks/blockers:
Suggested next Warden migration batch:
```

## Copy/paste assignment for the Warden agent

```text
Implement the first Warden frontend package vertical slice using the canonical
SaaS Starter revision supplied with this task. Read
module/docs/warden-frontend-plugin-integration-plan.md and every canonical doc
listed under "Read first" before editing.

Own only Warden repository paths. Treat module-saas-starter as read-only. Do not
edit or extend the host contract, shell, auth/token code, BFF, or public package
implementations. Preserve the Warden dirty worktree and obtain a checkpoint
before re-deriving anything.

Deliver only Warden Overview + traffic widget + their traffic/recent-calls
model, validated live repository, TanStack Query controller, manifest, service
requirement, and tests. Use @codefly/saas-plugin-contract,
@codefly/saas-plugin-contract/capabilities, @codefly/saas-plugin-react, and
@codefly/saas-plugin-react/runtime only. Define
JSON-safe metadata with definePlugin, bind lazy components with
defineReactPlugin, and compose the application with defineReactFrontend. No @/
imports, NEXT_PUBLIC_WARDEN URL,
direct backend origin, token access, alternate authenticated fetch, loose-file
scanner, or silent demo fallback.

Register the product explicitly in Warden frontend.config.ts and map
(warden, api) to logical target (platform, warden). Run
npm run generate:plugin-codefly-dependencies; do not retain an inline edit to
the Starter service manifest. Put the product below the existing packages/*
workspace, leave the protected root package.json unchanged, and regenerate the
lockfile through npm.

Generate Warden backend bindings from the capability proto packaged with
@codefly/saas-plugin-contract 2.1.0. Implement the fixed REST well-known
response with schema 1, contract warden.console, major 1, and sorted
product-owned capability IDs. Do not handwrite a parallel response struct.

Prove starter-only, installed, unavailable-backend, incompatible-backend,
uninstalled, and live Warden states. Return the exact handoff block required by
the plan, including source revisions, changed paths, generated artifacts, and
every command/result.
```

## Work after the first slice

Only after the five lifecycle states pass:

1. migrate Warden generated-client ownership (`FP-009`);
2. migrate remaining read-only Observe/Investigate routes in small groups;
3. migrate mutations with explicit authorization and confirmation tests;
4. extend the capability list only when a migrated view consumes that exact ID;
5. migrate Warden fixtures to explicit package-local test/demo adapters;
6. retire the preserved legacy project-plugin tree;
7. publish or otherwise version the Warden package reproducibly;
8. add Warden to the generic testkit/release matrix without adding Warden
   behavior to the Starter.
