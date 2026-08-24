# Frontend plugin public API

Status: frozen for contract major 2 (`FP-002`, `FP-012`); service inventory,
logical allowlist, runtime capability handshake, and contribution isolation
added in `FP-006`/`FP-007`, `FP-043`, and `FP-047`/`FP-048`
Machine-readable map: `services/frontend/code/frontend-plugin-public-api.json`
Ownership: [frontend-plugin-maintainers.md](frontend-plugin-maintainers.md)

This import map is product-neutral. Warden, Mind, Codefly-owned products, and
other consumers use the same package entry points and are subject to the same
private-host restrictions.

## Active packages

`@codefly/saas-plugin-contract` exposes its React-free metadata API at the
package root:

```ts
import {
	DEFAULT_FRONTEND_APPEARANCE,
	FRONTEND_APPEARANCE_TOKEN_NAMES,
  FRONTEND_PLUGIN_CONTRACT_VERSION,
  buildFrontendServiceAllowlist,
  defineFrontend,
  definePlugin,
	resolveFrontendAppearance,
	type FrontendAppearanceDefinition,
  type FrontendPlugin,
  type FrontendServiceBinding,
	type FrontendThemePreference,
} from "@codefly/saas-plugin-contract";
```

The public root contains the versioned plugin/config types, retained navigation,
route, widget, and logical service requirement types, and pure
composition/validation. The exact package version and per-entrypoint export
lists are recorded in the JSON map and checked against the package manifests
and modules. There are no supported undeclared deep imports.

Product packages define their JSON-safe manifest with `definePlugin({...})`.
Route and widget entries contain stable IDs and presentation metadata, never
React components. The helper preserves literal IDs, service aliases/protocols,
and route paths while running package-local validation with actionable
diagnostics. The pure `defineFrontend` composition validates the complete
metadata set, including cross-plugin and filesystem-route collisions.

`defineFrontend` also resolves application-owned branding and a complete,
immutable semantic light/dark appearance preset. Consumers may configure the
default `light`, `dark`, or `system` preference, per-mode semantic color token
overrides, and the shared structural/typographic tokens — `radius`,
`fontSans`/`fontHeading`, `fontSizeBase`, `spacing` (density), `sidebarWidth`,
`sidebarWidthIcon`, `borderWidth`, and `shadowStrength`. Color is per-mode;
structure and typography are shared across modes. The host projects this
resolved preset during server rendering. Plugins do not contribute branding,
raw CSS, or appearance state. Tenant branding is a validated runtime overlay
owned by the host.

`@codefly/saas-plugin-react` owns the separate React registration and service
runtime. Every declared route/widget ID must receive exactly one component;
missing, duplicate, and extra registrations fail before rendering:

```tsx
import { lazy } from "react";
import {
  definePlugin,
  FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly/saas-plugin-contract";
import {
  defineReactFrontend,
  defineReactPlugin,
} from "@codefly/saas-plugin-react";

const manifest = definePlugin({
  contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
  name: "example",
  routes: [{ id: "overview", path: "/admin/example" }],
});

export const examplePlugin = defineReactPlugin({
  manifest,
  routes: [{ id: "overview", component: lazy(() => import("./overview.js")) }],
});

export const frontendConfig = defineReactFrontend({
  branding,
  plugins: [examplePlugin],
});
```

`FrontendReactConfig.metadata` retains the complete React-free inventory; its
resolved `routes` and `widgets` attach the validated lazy components for host
outlets. Product controllers use the separate runtime entry point:

```tsx
import { usePluginService } from "@codefly/saas-plugin-react/runtime";

export function useExampleRepository() {
  const service = usePluginService("example", "api");
  return () => service.request("overview", { query: { window: "24h" } });
}
```

The host injects a closure-backed `PluginRuntime`. Product code receives only a
stable `(plugin, alias)` service transport: it cannot read the host token
accessor, select an origin, add cookies, replace the bearer, or set trusted
`x-*`, identity, tenant, host, origin, or forwarding headers through that
transport. Each request reads the latest host session token and targets only
`/api/plugins/{plugin}/{alias}/{safe-relative-path}` with fixed same-origin,
no-store, no-redirect policy. `createPluginRuntime` and `PluginRuntimeOptions`
are host construction primitives; products consume the injected runtime through
`usePluginRuntime` or `usePluginService`.

The runtime and BFF security obligations, including organization switching,
expired credentials, platform roles, and cross-tenant resource substitution,
are defined by the
[authentication and tenant matrix](frontend-plugin-auth-tenant-matrix.md).
Product packages do not gain backend authority from this frontend API.

`@codefly/saas-plugin-contract/capabilities` is the protobuf-defined backend
handshake surface. Its package version is `2.1.0`; the React package version is
`0.4.1`. The published contract package includes the canonical
`proto/saas/frontend/plugin/v1/capabilities.proto`, generated message/service
schemas, strict ProtoJSON helpers, and fixed REST/Connect operation constants.
Backend implementations generate their native bindings from that proto instead
of maintaining a parallel JSON response type.

Each installed service answers with schema version `1`, the stable contract ID,
its exact contract major, and at most 128 unique product-owned capability IDs.
The host accepts only strict ProtoJSON, normalizes the capability order, and
compares contract/major with the application allowlist. Unknown fields, unsafe
IDs, missing operations, malformed responses, and mismatches become the same
non-sensitive `backend_incompatible` problem.

The runtime also converts non-successful BFF responses into a small,
non-sensitive availability contract:

```tsx
import {
  pluginErrorFromResponse,
  usePluginService,
} from "@codefly/saas-plugin-react/runtime";

const service = usePluginService("example", "api");
const response = await service.request("overview");
if (!response.ok) throw await pluginErrorFromResponse(response);
```

The host wraps every route and every widget independently with
`PluginErrorBoundary` from `@codefly/saas-plugin-react/ui` and its own Suspense
fallback. `loading` means the lazy contribution is pending, `ready` means it is
rendering normally, and failures are one of `unavailable`, `incompatible`, or
`failed`. The public failure descriptor contains only a stable code, an
optional validated request ID, and an optional bounded retry delay. It never
contains backend detail, a URL, an exception message, or a stack. Unknown
render exceptions become `failed/render_failed`. Retrying resets only the
owning contribution boundary.

The optional request ID is accepted only from the safe host `x-request-id`
response header. Product problem bodies cannot supply or override it. See the
[request-correlation contract](frontend-plugin-request-correlation.md).

Before rendering a contribution, the host probes every service declared by its
owning plugin through `PluginServiceTransport.capabilities()`. Successful
handshakes are cached for the current runtime; failures are not cached, so the
boundary retry performs a fresh probe. A clean plugin with no declared backend
service performs no probe.

The public UI entry point deliberately provides containment, not a component
library or product styling. SaaS Starter owns the canonical visual fallback;
product packages may use the error type/response converter and keep their own
domain-specific empty, stale, and authorization states.

`@codefly/saas-settings` is the schema-agnostic settings runtime available to
the Starter host and installed product plugins. It binds a product's generated
protobuf `Settings` and patch types once, then supplies typed fields, presence
and default handling, recursive sibling-preserving patch composition, and
field-mask reset requests. It has no generated-schema, React, Next.js, host, or
product dependency. A product such as Warden owns its concrete settings proto
and generated bindings; changing that schema never forks this runtime.

## Generic service requirements

A plugin may declare one or more backend requirements without embedding a
deployment location:

```ts
const service = {
  alias: "api",
  protocol: "rest",
  routePrefix: "/api/v1/example",
  compatibility: { contract: "example.api", major: 1 },
} as const;
```

Aliases are plugin-local. Composition attaches the owning plugin and produces a
deterministically ordered, immutable service inventory. Validation rejects
unknown fields, unsafe identifiers or paths, unsupported protocols, missing or
invalid compatibility metadata, duplicate aliases, and ambiguous routes.

This metadata does not resolve Codefly bindings, grant network access, expose
backend URLs, or make the frontend authoritative for backend compatibility.

## Logical application bindings and allowlist

The application, never the plugin, maps every installed `(plugin, alias)` pair
to a logical Codefly target in `frontend.config.ts`:

```ts
export const serviceBindings = [
  {
    plugin: "example",
    alias: "api",
    target: { module: "example-module", service: "example-api" },
  },
] as const satisfies readonly FrontendServiceBinding[];
```

`buildFrontendServiceAllowlist(frontendConfig.services, serviceBindings)`
requires an exact one-to-one mapping and emits entries in stable
`plugin/alias` order. The endpoint name is derived from the plugin's validated
`rest` or `connect` protocol; bindings cannot override it. Bindings accept only
safe logical identifiers—URLs, hosts, credentials, arbitrary endpoints,
unknown fields, missing mappings, extra mappings, and duplicates fail
generation.

The checked-in result lives at
`services/frontend/code/server/plugin-service-allowlist.generated.json` and is
empty in the canonical starter. It is server routing input, not a browser
transport configuration. Resolving its logical targets to deployment URLs is a
server-only responsibility implemented by the generic BFF. Product repositories
call `/api/plugins/{plugin}/{alias}/{relative-path}` and never construct a
backend origin. See [the BFF contract](frontend-plugin-bff-contract.md).

## Reserved packages

The import map reserves this package without making it active yet:

- `@codefly/saas-plugin-testkit`: `.`.

The React package root, `./runtime`, and the narrow `./ui` isolation entry point
are active. A shared visual component library remains deliberately unpublished.
Reserved packages are not permission to import a private starter substitute.

## Forbidden imports

Product packages may not import the starter's `@/` alias or escape through
relative paths into `src`, `app`, `features`, `plugins`, `lib`, or `components`.
In particular, auth state, providers, Connect transports, page files, feature
controllers, and host query keys remain private.

There are no host compatibility import paths. The former `admin-core`,
`admin-config`, private contract/composition barrels, `AdminPlugin`, and the
parallel framework plugin interface were removed under FP-012A.

Boundary tests consume the JSON map, so adding a package, entry point, export,
or exception requires an explicit contract decision rather than an ad hoc
product-specific allowlist.
