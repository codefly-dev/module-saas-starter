# ADR 0001: Frontend plugin packaging and composition

- Status: Accepted
- Date: 2026-07-15
- Scoped by: [ADR 0002](0002-remote-ui-tier-boundary.md) — the trust model below
  governs the starter SDK's Tier 1 (trusted compile-time plugins); a vetted
  first-party Module Federation tier is placed at the consumer boundary there.
- Tasks: FP-001 through FP-004, FP-007A, FP-010A, FP-012, FP-012A,
  FP-043, FP-047, FP-048, CONV-001 through CONV-007

## Context

The SaaS frontend already composes compile-time contributions, but its current
integration seam mixes host-private files, loose-file discovery, application
branding, and browser-visible service locations. Product consumers must install
cleanly without becoming part of the generic starter.

## Decision

### Package layout and ownership

The public SDK uses npm workspaces nested under the existing application and CI
package root, `services/frontend/code/packages/`. Keeping the workspaces below
`code/` preserves the copied starter's single `package-lock.json`, existing
`npm ci` command, and Docker build context:

- `@codefly/saas-plugin-contract` owns serializable metadata, compatibility,
  pure composition, `FrontendPlugin`, and the published protobuf backend
  capability handshake.
- `@codefly/saas-plugin-react` owns exact lazy-component registration, public
  React adapters, host services, and extension outlets.
- `@codefly/saas-plugin-testkit` owns conformance and render harnesses.

Product packages remain product-owned and depend only on
those published package entry points. They may not import starter-private
`src/` paths. The contract and pure composition surface are extracted in
`@codefly/saas-plugin-contract`. Host-local compatibility barrels are removed,
not a second public API. FP-012 moves component registrations to the React
package and FP-012B activates the product-neutral service runtime and hooks.
FP-047/FP-048 activate a narrow, styling-neutral error boundary and safe
availability descriptors; a shared visual component library and the testkit
remain reserved. The
machine-readable public/forbidden import map is
`services/frontend/code/frontend-plugin-public-api.json`.

The current application install shape places a product package as an additive
workspace below the existing `packages/*` wildcard. Product-specific metadata
belongs to that package's `package.json`; the protected root manifest is not
edited. npm regenerates the one application lockfile, Docker copies all
workspaces before `npm ci`, and all workspaces with build scripts are built.
Base integrity validates the lock's protected root metadata, exact workspace
set, per-workspace dependency metadata, and workspace links instead of hashing
the generated lock byte-for-byte.

### Version policy

The contract uses an integer exact-major `contractVersion`. A host accepts only
the major it implements and fails composition before rendering when a plugin is
incompatible. Public npm packages follow SemVer; breaking manifest or public
import-map changes require a package major and contract-version decision.

### Plugin scope and trust model

`FrontendPlugin` is the public React-free metadata target because contributions
can affect the authenticated application, not only administration. Contract v2
routes and widgets declare stable IDs and presentation metadata; they cannot
carry functions, components, or host objects. `defineReactPlugin` binds each ID
to exactly one lazy component. There is no `AdminPlugin` alias. Plugins are
trusted, compile-time dependencies installed with the application. Loading
remote JavaScript at runtime is out of scope for this tier; ADR-0002 scopes this
rule to the starter SDK's trusted compile-time tier and places a vetted
first-party Module Federation tier at the consumer boundary.

Every retained contribution has a host consumer: navigation, routes, and named
widgets. The unused generic `Resource` contribution is removed. Presentation
access is temporarily limited to `public`, `authenticated`, `admin`, and
`super_admin`, plus the starter's generated permissions, because those are the
semantics the host can evaluate consistently today.

### Composition root and branding

The sole handwritten application integration point is
`services/frontend/code/frontend.config.ts`. It owns branding, the semantic
appearance preset, and the explicit installed React plugin list.
`defineReactFrontend` first runs pure metadata composition, then joins the
validated component registrations. Render adapters receive its immutable
composed result through the frontend config provider; only build/composition
wiring may import the singleton directly.

Loose `project-plugins/` discovery ends with FP-003/FP-018; installed package
exports are listed directly in this file. The file
is intentionally excluded from base-integrity hashing so a composed application
can own it without whitelisting changes to generic starter source. All other
starter frontend files remain protected.

The generated plugin service allowlist is also the application input to the
frontend Codefly service-manifest compiler. The service manifest is excluded
from byte hashing but must carry the generated provenance header and exactly
the external dependencies derived from the allowlist. No product patches this
YAML directly.

Branding and base appearance are application data, never plugin side effects.
The resolved appearance contract supplies an SSR-projected light/dark semantic
token set, fonts, radius, and default theme. Warden-enabled applications
override identity and appearance only in `frontend.config.ts`. The host may
apply the current organization's validated logo, favicon, and primary color as
a narrow runtime overlay; no plugin can contribute branding or arbitrary CSS.

The user's light/dark/system selection uses the generated accounts protobuf
enum. One host controller updates `next-themes` immediately and persists the
same preference through UserSettings, so the header selector and settings page
cannot diverge or fight during synchronization.

### Framework compatibility

The initial supported line is Next.js `>=16.2 <17`, React `>=19.2 <20`, and
React DOM `>=19.2 <20`. CI pins the starter's tested versions (currently Next
16.2.10 and React/React DOM 19.2.4). The contract package has no React runtime,
type, or peer dependency. The React package enforces its React peer range;
framework-bearing UI packages will enforce corresponding Next.js and React DOM
ranges if they are activated.

### Runtime and ownership boundaries

`AdminLayout` is the only shell. The host owns authentication propagation,
composition, shared UI, navigation and route/widget outlets, independent
Suspense/error boundaries, and same-origin browser transport. Products own
manifests, domain models,
repositories, controllers, views, fixtures, generated clients, and domain
tests. Browser code uses relative same-origin paths; server-only binding code
resolves Codefly service locations.

Backend authorization remains authoritative. One presentation evaluator is
used by routes, navigation, widgets, commands, and tiles so the UI does not
invent renderer-specific role coercions.

Runtime backend compatibility is a host-verified protobuf handshake, not a
product UI convention. The published contract package carries
`saas.frontend.plugin.v1`; REST backends expose its ProtoJSON response at one
fixed well-known path and Connect backends implement its generated service. The
BFF compares the response with the installed `{contract, major}` and returns
only normalized safe metadata. Missing, malformed, or mismatched handshakes are
incompatible and remain local to the owning route/widget.

## Consequences

- A clean starter runs without any product plugin.
- Installing or removing a product plugin adds/removes one product workspace
  and edits `frontend.config.ts`; npm and generators update the lock, allowlist,
  and frontend service manifest, with no product edits under starter `src/`.
- Host compatibility shims and the parallel framework plugin interface are
  deleted. Integrations cannot depend on private barrels, loose-file discovery,
  public service URLs, or legacy endpoint environment variables.
- A product vertical slice starts only after the convergence gate passes and
  the public import map/package boundary is frozen.
