# @codefly/saas-plugin-contract

Product-neutral manifest types and pure composition for trusted, compile-time
plugins installed in the Codefly SaaS frontend host.

```ts
import {
  FRONTEND_PLUGIN_CONTRACT_VERSION,
  definePlugin,
} from "@codefly/saas-plugin-contract";

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

The application binds each installed requirement to a logical Codefly target
and generates its server-only routing inventory:

```ts
import {
  buildFrontendServiceAllowlist,
  type FrontendServiceBinding,
} from "@codefly/saas-plugin-contract";

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
import { defineFrontend } from "@codefly/saas-plugin-contract";

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
		light: { primary: "oklch(0.52 0.22 270)" },
		dark: { primary: "oklch(0.72 0.17 270)" },
	},
	plugins: [],
	filesystemRoutes: [],
});
```

Missing semantic tokens resolve from the neutral default preset. Unknown
fields, unsafe asset paths, and unsafe CSS values fail composition. The result
is deeply immutable and can be projected during server rendering without a
wrong-theme flash.

This package has no React dependency or peer dependency. Product packages bind
lazy components to the declared IDs through `defineReactPlugin` from the
separately versioned `@codefly/saas-plugin-react` package.

## Backend capability handshake

The publishable `proto/saas/frontend/plugin/v1/capabilities.proto` file is the
source of truth for the runtime backend handshake. Generated TypeScript schemas
and strict helpers are available from the separate public entry point:

```ts
import {
  defineFrontendPluginCapabilities,
  frontendPluginCapabilitiesToJson,
} from "@codefly/saas-plugin-contract/capabilities";

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
