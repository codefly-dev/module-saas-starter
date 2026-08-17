# @codefly/saas-plugin-manifest

The unified `plugin.codefly.yaml` manifest for the Codefly SaaS platform: one
file spanning a plugin's backend services, API compatibility, events, frontend
contributions, required platform capabilities, permissions, entitlements,
configuration, migrations, egress, lifecycle, and integrity.

This package is the schema half of `P3-PLUGIN-002`. It ships:

- `plugin.codefly.schema.json` — the canonical, language-neutral JSON Schema.
- TypeScript types and a pure, JSON-safe validator (`assertPluginManifest`,
  `loadPluginManifest`, `definePluginManifest`).
- `toSolutionSpec` — the projection onto obin's lodestar `SolutionSpec`.
- `examples/plugin.codefly.yaml` — a reference manifest exercising every field.

The frontend contribution block (`ui`) is validated by
`@codefly/saas-plugin-contract` so the frontend plugin contract and the unified
manifest never fork.

```ts
import { readFileSync } from "node:fs";
import yaml from "js-yaml";
import { loadPluginManifest, toSolutionSpec } from "@codefly/saas-plugin-manifest";

const manifest = loadPluginManifest(
  yaml.load(readFileSync("plugin.codefly.yaml", "utf8")),
);
const solution = toSolutionSpec(manifest);
```

## Manifest shape

| Section | Meaning |
| --- | --- |
| `metadata` | Identity: name, semantic version, publisher, description. |
| `services` | Backend services the plugin owns and their endpoint protocols. |
| `api.exposes` / `api.consumes` | Stable API contracts provided and required, with exact majors. |
| `events.publishes` / `events.subscribes` | Namespaced, versioned domain events and their handlers. |
| `ui` | Frontend navigation, routes, widgets, and BFF service requirements. |
| `needs` | Platform capabilities the backend requires (`store:postgres`, …). |
| `permissions` | `resource:action` permissions the plugin defines and enforces. |
| `entitlements` | Plan/grant gates on plugin features. |
| `config` | Typed configuration keys, including secret-only values. |
| `migrations` | Ordered, scoped database migrations the plugin owns. |
| `egress` | Allowed outbound network destinations. |
| `lifecycle` | Install, upgrade, and uninstall reconciliation steps. |
| `integrity` | Detached signature and pinned artifact hashes. |

The manifest is JSON-safe. It declares stable ids and compatibility metadata
only — never deployment addresses, credentials, or resolved bindings. Those are
host-owned and resolved after installation.

## Relationship to obin's SolutionSpec

The identity, services, api, events, ui, needs, permissions, and lifecycle
sections project directly onto lodestar's `SolutionSpec`. The starter-only
sections (entitlements, config, migrations, egress, integrity) carry through
`extensions['x-codefly']` so the projection is lossless. See
[`plugin-manifest-schema.md`](../../../../../docs/plugin-manifest-schema.md).
