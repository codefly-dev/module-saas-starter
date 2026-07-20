# Frontend plugin platform implementation plan

Date: 2026-07-15
Status: active

## Objective

Provide one trusted compile-time frontend plugin platform usable by Warden,
Mind, Codefly-owned applications, and future products. A clean starter works
without products. Installing a product requires one additive package workspace
and one explicit composition entry, with no product edits under starter `src/`.

## Current foundation

- Packaging and compatibility decisions are frozen in ADR 0001.
- The public import map is machine-readable and enforced by tests.
- `@codefly/saas-plugin-contract` owns React-free JSON-safe manifest types and
  pure metadata composition.
- `@codefly/saas-plugin-react` binds lazy route/widget components to exact
  manifest IDs and produces the host render configuration.
- Product packages use the public `definePlugin` helper for literal-preserving
  package-local validation; the host revalidates the installed set.
- `@codefly/saas-plugin-react` also owns the active injected service runtime,
  public provider/hooks, fixed same-origin browser request policy, safe
  availability errors, and the styling-neutral contribution error boundary.
- `frontend.config.ts` explicitly lists installed plugins; scanning is removed.
- The host has one shell, one injected config path, neutral defaults, one
  presentation evaluator, and same-origin browser transport policy.
- Logical service requirements are validated and composed without resolving
  deployment locations.
- Application-owned logical Codefly bindings converge with installed
  requirements into a deterministic, checked server-only allowlist; missing,
  extra, duplicate, unsafe, and URL-shaped mappings fail generation.
- The same allowlist deterministically generates the frontend service's
  external Codefly dependencies. Node build gates check drift without requiring
  Go; the strict Go compiler owns regeneration.
- `packages/*`, Docker `npm ci`, all-workspace builds, semantic lock validation,
  and generated-service-manifest integrity support additive install/uninstall
  without mutating protected host metadata.
- The prepared Codefly `nextjs` 0.0.110 agent removes its competing local plugin
  registry, exposes a neutral `packages/*` seam, uses the Starter-tested Next.js
  16.2.10 line and pinned Node image, and proves non-root generated-service
  builds. It remains a framework substrate; the SDK stays Starter-owned. An
  immutable agent release and exact Starter pin are still required.
- The generic same-origin plugin BFF resolves only installed logical targets,
  strips caller identity/tenant headers, forwards the bearer for downstream
  validation, bounds unary traffic, rejects redirects, and emits stable
  non-sensitive problems. The auth/tenant matrix assigns host transport rows to
  Starter and claim, method-policy, tenant, resource, and storage rows to each
  product backend. One host-generated request ID follows each attempt through
  the BFF while caller/backend identifiers remain untrusted.
- A protobuf-defined, protocol-neutral capability handshake uses one fixed REST
  path or generated Connect procedure. The BFF normalizes and compares it with
  the installed contract/major, and each contribution suspends on all services
  declared by its owning plugin before rendering.
- Every route and widget has an independent Suspense/error boundary. The host
  renders canonical loading, unavailable, incompatible, and failed states;
  normal contribution rendering is the ready state. Unknown exception details
  never enter the fallback props or UI.

## Delivery phases

### 1. Public contract and composition

Keep metadata and pure validation framework-light. Every additive export follows
SemVer and updates the public map. Compatibility re-exports remain host-only and
carry removal tasks.

Exit evidence: clean package build, exact export test, incompatible manifest
diagnostic, consumer-style fixture, host typecheck, and starter production build.

### 2. First external product slice

Move one product overview route and one dashboard widget with their repository
and controller into a product-owned package. Warden is the current proving
consumer, but it receives no special host API. The package builds outside
starter `src/` and imports only active public entry points.

The consumer-side execution and lifecycle matrix are specified in the detailed
[Warden integration handoff](warden-frontend-plugin-integration-plan.md).

Exit evidence: starter-only build, independently built product package, enabled
host render tests, and clean removal.

The canonical mechanics and commands are in the
[installation procedure](frontend-plugin-installation.md).

### 3. Service inventory and constrained BFF

Turn composed logical requirements into deterministic server-only routing data.
Resolve Codefly bindings outside the browser and expose only installed aliases
and safe route prefixes. Enforce methods, headers, identity, tenant propagation,
body limits, timeouts, redirects, and problem mapping.

Exit evidence: arbitrary destinations and paths are inexpressible; anonymous,
cross-tenant, unsafe-header, traversal, timeout, and unavailable-service tests
fail predictably.

### 4. Public React/runtime package

Keep authenticated transport, exact component registration, contribution
contexts, error/status boundaries, and genuinely shared UI adapters behind
`@codefly/saas-plugin-react`. Do not export provider implementations, auth
stores, page files, or private feature code.

The metadata/React split, service runtime, safe availability mapping, and
per-contribution isolation are active under FP-012/FP-012B/FP-047/FP-048. Any
additional reusable UI surface still requires a separate cross-product review;
it is not implied by component registration.

Exit evidence: a product route and widget render without private imports and one
throwing contribution cannot break the host shell.

### 5. Product MVC and generated-client ownership

Products own repository interfaces, live/fixture adapters, query state machines,
views, generated clients, generator commands, and drift tests. Views do not read
deployment environment variables or construct backend URLs.

Exit evidence: package-local tests cover loading, empty, stale, unavailable,
error, mutation, and retry states without booting the host.

### 6. Capabilities and semantic permissions

Backend capability/version status is active under FP-043 with namespaced
product capability IDs. Next, introduce namespaced semantic permissions and
apply one presentation decision to every contribution and action while
retaining backend enforcement.

Exit evidence: missing, incompatible, unauthorized, and partially enabled
services affect only their owning product surfaces.

### 7. Testkit and release matrix

Publish manifest, import-boundary, repository, and render conformance helpers.
Run starter-only, product-enabled, incompatible, unavailable, unauthorized,
throwing-contribution, install, and uninstall configurations.

### 8. Remove migration paths and release v1

Remove compatibility barrels and any remaining product-owned files/scripts from
the starter only after all known consumers use packages. Publish ownership,
compatibility, deprecation, installation, and troubleshooting documentation.

## Non-goals

- Remote JavaScript or runtime npm installation.
- A third-party marketplace or untrusted execution sandbox.
- Product DTOs, endpoints, branding, or workflows in the starter.
- Frontend checks replacing backend authorization.
- Speculative protocols or contribution types without a host implementation.
