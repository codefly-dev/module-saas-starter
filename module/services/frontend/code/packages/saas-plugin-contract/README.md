# @codefly-dev/saas-plugin-contract

Product-neutral manifest types and pure composition for trusted, compile-time
plugins installed in the Codefly SaaS frontend host.

```ts
import {
  FRONTEND_PLUGIN_CONTRACT_VERSION,
  definePlugin,
} from "@codefly-dev/saas-plugin-contract";

export const examplePlugin = definePlugin({
  contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
  name: "example",
  services: [
    {
      alias: "api",
      protocol: "rest",
      routePrefix: "/api/v1/example",
      compatibility: { contract: "example.api", major: 1 },
    },
  ],
	 routes: [{ id: "overview", path: "/admin/example" }],
});
```

`definePlugin` preserves literal names, aliases, protocols, routes, and
contribution IDs while validating the package-local manifest immediately. The
manifest is JSON-safe: routes and widgets contain stable IDs and presentation
metadata, never React components. The host validates the complete installed set
again for cross-plugin and filesystem-route collisions.

Service requirements contain logical and API compatibility metadata only.
Deployment URLs, credentials, resolved Codefly bindings, and browser transport
configuration are host-owned and never enter the plugin contract.

## What the transport carries

A contributed page reaches what the module's bindings declare, in both shapes
those bindings come in. **Both REST and Connect are carried** — a page is not
restricted to REST bindings.

**A custom verb is carried on the last segment.** A method that is not one of a
resource's standard verbs is bound in the Google HTTP form as
`…/<collection>:<verb>` (AIP-136), and the colon is the separator rather than
part of a name, so it reaches the upstream literal:

```ts
await service.request("records:recover", { method: "POST", body });
await service.request("records/record-1/interactions/interaction-1:respond", {
  method: "POST",
  body,
});
```

One colon, on the last segment only, with a lowerCamelCase verb. A colon
earlier in the path, a second colon in one segment, or an empty resource or
verb is refused in the browser before a request leaves it —
`isFrontendServicePath` is the exported predicate, and the host's BFF reads the
same one, so the two halves cannot drift. The resource half is a segment like
any other: `..:recover` is a traversal wearing a verb and is refused with it.

**A Connect requirement names its mount and its services.** The Connect
protocol puts a unary procedure at `/<fully.qualified.Service>/<Method>` from
the origin, which is also where the fixed capability handshake below is probed,
so `routePrefix: "/"` is a backend serving Connect at its root — and a product
path such as `/v1/connect` is the same backend mounted behind a path. Either
way, `connectServices` is **required**: it lists the fully qualified services
that alias may reach.

```ts
const procedures = {
  alias: "procedures",
  protocol: "connect",
  routePrefix: "/",
  connectServices: ["example.records.v1.RecordService"],
  compatibility: { contract: "example.procedures", major: 1 },
} as const;

await service.request("example.records.v1.RecordService/RecoverRecord", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: "{}",
});
```

The list is required because the protocol's path carries the service name, so
the CALLER supplies it and the prefix cannot bound it: a requirement declaring
only a prefix could reach every service its upstream origin serves, under the
viewer's own credential. One entry is an alias scoped to one service; several
are several services of one backend behind one alias and one handshake. The same
name twice is refused, and so is `connectServices` on a `rest` requirement.

`resolveConnectProcedure(services, segments)` is the exported rule — the
procedure a declared list admits, or `null` — and the host's BFF reads the same
one. A procedure of an undeclared service, a REST-shaped path, and a method with
no service are all refused; the root prefix stays Connect-only, since a REST
requirement declaring it would make its alias a prefix-less proxy of the whole
upstream. A route outside the alias a requirement declared is refused whatever
its shape.

The application binds each installed requirement to a logical Codefly target
and generates its server-only routing inventory:

```ts
import {
  buildFrontendServiceAllowlist,
  type FrontendServiceBinding,
} from "@codefly-dev/saas-plugin-contract";

const bindings = [
  {
    plugin: "example",
    alias: "api",
    target: { module: "example-module", service: "example-api" },
  },
] as const satisfies readonly FrontendServiceBinding[];

const allowlist = buildFrontendServiceAllowlist(frontendConfig.services, bindings);
```

Bindings cannot contain URLs, credentials, or endpoint overrides. Every
installed requirement must have exactly one binding, and the generated order is
stable. A clean starter uses empty requirements and bindings.

The application also owns branding and the semantic appearance preset. Plugins
cannot inject brand identity, raw CSS, or theme side effects:

```ts
import { defineFrontend } from "@codefly-dev/saas-plugin-contract";

export const frontendConfig = defineFrontend({
	branding: {
		name: "Example",
		mark: "E",
		title: "Example",
		description: "Example application",
		logo: { lightSrc: "/brand/logo.svg", alt: "Example" },
		favicon: "/brand/favicon.svg",
	},
	appearance: {
		defaultTheme: "system",
		radius: "0.75rem",
		// Typography and structural tokens are shared across both modes.
		fontSans: '"Inter", system-ui, sans-serif',
		fontSizeBase: "1rem",
		spacing: "0.25rem", // density: base unit for every spacing utility
		sidebarWidth: "18rem",
		sidebarWidthIcon: "3rem",
		borderWidth: "1px",
		shadowStrength: "1", // 0 (flat) … 2 (deep); unitless multiplier
		light: { primary: "oklch(0.52 0.22 270)" },
		dark: { primary: "oklch(0.72 0.17 270)" },
	},
	plugins: [],
	filesystemRoutes: [],
});
```

`FRONTEND_APPEARANCE_TOKEN_NAMES` and `DEFAULT_FRONTEND_APPEARANCE` here are the
single source of truth for the shared token vocabulary; the kit documents that
vocabulary as the contract every layer above consumes by name — names, roles,
and light/dark defaults — in
[`@codefly-dev/ui`'s TOKENS.md](../codefly-ui/TOKENS.md).

The appearance preset has two kinds of input. **Color** is per-mode: the full
semantic token set for `light` and `dark`. **Structure and typography** are
shared across modes: `radius`, `fontSans`/`fontHeading`, `fontSizeBase`,
`spacing` (density), `sidebarWidth`/`sidebarWidthIcon`, `borderWidth`, and
`shadowStrength`. Light and dark are not layered on top of a skin; the skin
contains both palettes and the active mode simply selects which one the shared
semantic variables read.

Missing tokens resolve from the neutral default preset, so omitting any field
leaves the rendered product unchanged. Lengths must be `0` or a `px`/`rem`/`em`
value and `shadowStrength` a unitless number in `0…2`; unknown fields, unsafe
asset paths, and unsafe CSS values fail composition. The result is deeply
immutable and can be projected during server rendering without a wrong-theme
flash.

This package has no React dependency or peer dependency. Product packages bind
lazy components to the declared IDs through `defineReactPlugin` from the
separately versioned `@codefly-dev/saas-plugin-react` package.

## Backend capability handshake

The publishable `proto/saas/frontend/plugin/v1/capabilities.proto` file is the
source of truth for the runtime backend handshake. Generated TypeScript schemas
and strict helpers are available from the separate public entry point:

```ts
import {
  defineFrontendPluginCapabilities,
  frontendPluginCapabilitiesToJson,
} from "@codefly-dev/saas-plugin-contract/capabilities";

const response = defineFrontendPluginCapabilities({
  contract: "example.api",
  contractMajor: 1,
  capabilities: ["calls.read", "traffic.read"],
});

return Response.json(frontendPluginCapabilitiesToJson(response));
```

REST backends expose the ProtoJSON response at
`/.well-known/codefly/frontend-plugin-capabilities`. Connect backends implement
the generated `FrontendPluginCapabilityService`. The host probes that fixed
operation and compares it with the installed manifest before rendering product
routes or widgets. Product deployment addresses and raw backend details never
enter this response.

Regenerate the checked-in TypeScript descriptors only through Codefly:

```sh
codefly generate proto --proto ./proto --output . --local --template buf.gen.local.yaml
```

The package root and `./capabilities` are public.

The default light muted foreground uses achromatic OKLCH lightness 0.54 so small captions meet WCAG AA 4.5:1 on background, card, muted and accent surfaces. The default dark pair also satisfies this check. Custom appearances own their chosen contrast; clients should use these semantic tokens rather than inventing local caption colors.
