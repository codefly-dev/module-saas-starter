# Frontend plugin composition and generated inputs

Status: active (`P1-GEN-008`, complete).

The frontend plugin projection provides one reviewable route/navigation input
for the trusted compile-time `FrontendPlugin` system. It does not introduce
runtime-loaded JavaScript or make UI visibility an authorization boundary.

## Sources and artifacts

| Path | Role |
| --- | --- |
| `services/frontend/frontend.bindings.codefly.yaml` | Strict plugin ownership, navigation metadata, finite surfaces, access, and permission references. |
| `services/frontend/code/src/app/**/page.tsx` | Next.js filesystem route source discovered by the compiler. |
| `services/frontend/generated/plugin-catalog.json` | Typed `saas.frontend.plugins.v1` inventory. |
| `services/frontend/code/src/gen/saas/frontend/v1/plugin_catalog.ts` | Typed runtime inputs for plugins, sidebar, command palette, and user menu. |
| `services/frontend/code/frontend.config.ts` | Application-owned explicit plugin installation and branding. |
| `services/frontend/code/frontend-plugin-public-api.json` | Frozen public package, entry-point, and export map. |
| `services/frontend/code/packages/saas-plugin-contract` | React-free JSON-safe contract and pure metadata composition package. |
| `services/frontend/code/packages/saas-plugin-contract/proto/saas/frontend/plugin/v1/capabilities.proto` | Published runtime backend capability handshake source of truth. |
| `services/frontend/code/packages/saas-plugin-react` | Exact lazy-component registration, injected runtime, service transport, provider, and hooks. |
| `services/frontend/code/server/plugin-service-allowlist.generated.json` | Deterministic server-only convergence of installed requirements and logical application bindings. |
| `services/frontend/service.codefly.yaml` | Generated base topology plus application-installed external plugin dependencies. |
| `services/accounts/code/pkg/cataloggen/frontend_plugin_service_dependencies.go` | Strict allowlist-to-Codefly dependency compiler. |
| `services/frontend/code/server/plugin-service-dependency-policy.ts` | Node-only build drift gate for the generated frontend service manifest. |
| `services/frontend/code/server/plugin-service-bindings.ts` | Exact server-only Codefly endpoint resolution for installed aliases. |
| `services/frontend/code/server/plugin-bff.ts` | Product-neutral same-origin proxy policy, limits, header filtering, and stable failures. |
| `services/frontend/code/src/app/api/plugins/[plugin]/[alias]/[...path]/route.ts` | The one browser-visible plugin backend route. |
| `docs/frontend-plugin-auth-tenant-matrix.md` | Canonical split and required evidence for host transport versus product authorization and tenant isolation. |
| `docs/frontend-plugin-request-correlation.md` | Host-owned diagnostic request-ID lifecycle and separation from untrusted trace context. |
| `services/accounts/code/pkg/cataloggen/frontend_plugins.go` | Filesystem-page inventory, joins, semantic validation, and deterministic renderers. |

The current catalog contains three built-in plugins, 36 pages, and 25
navigation items. Page access is inferred only from recognized filesystem
boundaries: `(auth)` is public, `(dashboard)` is authenticated, `/admin` is
admin-only, and `/admin/platform` is super-admin-only. The admin plugin catch-all is represented as
`/admin/{*slug}`. Unknown route groups and optional catch-alls fail generation.

## Navigation consumers

Each navigation item has one stable ID, plugin owner, exact filesystem route,
icon, optional group and accounts permission, minimum shell audience, global
order, and one or more finite surfaces:

- 20 sidebar entries;
- 16 command-palette entries;
- 21 plugin-registry entries;
- three user-menu entries.

The application composition root explicitly imports the installed packages and
projects generated inputs into the active sidebar, command palette, user menu,
and three built-in plugin modules. The
shared icon binding is exhaustive over the generated icon union. Platform-user,
feature-flag, job-operations, and session links use the same `super_admin` presentation rule
across every renderer.

Page access and navigation visibility are defense-in-depth UX. Accounts method
policy, RBAC, and RLS remain authoritative. Optional navigation permissions
must resolve to the generated accounts permission vocabulary, but consumers do
not infer backend authorization from a visible link.

## Plugin boundary

The application-owned `services/frontend/code/frontend.config.ts` is the sole
composition root. It imports every installed plugin package explicitly and
passes a deterministic array to `defineReactFrontend`; there is no directory
scan, generated registry, or implicit side-effect registration. Each product's
`definePlugin` manifest contains only JSON-safe metadata; `defineReactPlugin`
binds every declared route/widget ID to exactly one lazy component. Installing or
removing any product follows the same generic operation: change the application
dependency and this explicit list. Built-in modules no longer own handwritten
navigation arrays; they project their complete generated slice.
Startup rejects incompatible manifests, duplicate contributions, and plugin
routes that collide with a filesystem page.

Product plugins may also declare logical service requirements. These contain a
plugin-local alias, supported browser protocol, safe upstream path prefix, and
exact backend contract identity/major. Composition validates and inventories
the declarations but never resolves a deployment URL. The application maps each
installed alias to a logical Codefly module/service target in
`frontend.config.ts`; the generator rejects missing, extra, duplicate, unsafe,
or URL-shaped mappings and writes a stable server-only allowlist. The same
allowlist generates the frontend service's external Codefly dependencies; an
application never edits `service.codefly.yaml` inline. Actual service location
resolution is server-owned and the browser reaches it only through the
constrained `/api/plugins/{plugin}/{alias}/{relative-path}` BFF. The BFF never
trusts the frontend presence cookie or caller identity/tenant headers; it
forwards the bearer so the backend remains the authorization authority. Every
product runs the canonical authentication/tenant matrix with two real tenants;
Starter-only BFF mocks do not certify product authorization or storage
isolation.

Before rendering any route or widget for a plugin with declared services, the
host probes the protobuf-defined backend handshake through the reserved
`.well-known/capabilities` BFF path. REST and Connect targets use fixed
protocol-specific backend operations. The BFF accepts only strict schema `1`,
compares contract and major with the generated allowlist, and returns normalized
safe metadata. Missing, malformed, or mismatched responses become the contained
`backend_incompatible` state; successful probes are cached by the injected
runtime.

The packaging and compatibility decisions are frozen in
`docs/adr/0001-frontend-plugin-packaging.md`. The exact supported imports are in
`docs/frontend-plugin-public-api.md` and mechanically checked against
`frontend-plugin-public-api.json`. Product code imports the package root of
`@codefly/saas-plugin-contract`, its `./capabilities` backend entry point, and
the active root, `./runtime`, or `./ui` entry point of
`@codefly/saas-plugin-react`; starter-private `src/` paths are not SDK APIs. The
host supplies the current bearer through a closure-backed runtime, while
products select only an installed plugin/alias and safe relative path. They do
not construct deployment URLs or import the host token store.
The canonical architecture, execution rules, plan, and evidence-based backlog
live under `docs/frontend-plugin-*.md` in this repository; consumer repositories
may link them but do not redefine the host contract.

## Generation and validation

From `services/accounts/code`, after protobuf and service-catalog generation:

```sh
go generate ./pkg/cataloggen
```

Generation joins the validated service catalog, deployment topology, frontend
binding, plugin files, and page tree. It rejects unknown YAML fields, owner or
plugin drift, duplicate IDs/orders/surface paths, unknown pages, weaker link
access, unknown permissions/icons/surfaces, dynamic navigation targets, and
consumer-unsafe normalized JSON.

Go tests prove discovery, deterministic output, checked-in parity, strict
external dependency generation, and unsafe input rejection. Vitest
independently pins plugin, page, navigation, surface, permission, grouping,
role-visibility, additive workspace, and service dependency parity. CI
regenerates checked-in catalog outputs and rejects drift.

From `services/frontend/code`, installation uses:

```sh
npm run generate:plugin-codefly-dependencies
npm run check:plugin-codefly-dependencies
```

The first command builds every package workspace, writes the application
allowlist, and invokes the strict Go compiler for the frontend service manifest.
The second checks both outputs without writing. `prepare:frontend` regenerates
the allowlist and runs a Node-only dependency check before development, tests,
and production builds, so the frontend container does not require Go. See the
[canonical install/uninstall procedure](docs/frontend-plugin-installation.md).

The route inventory is the page/catch-all input for `P1-NET-003`. Static Next.js
assets and the final Istio frontend fallback are not cataloged yet, so the
generated API-only VirtualService remains undeployed.
