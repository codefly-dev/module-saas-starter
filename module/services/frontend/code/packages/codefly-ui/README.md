# @codefly/ui

The shared, versioned Codefly SaaS frontend kit. It is a standalone package so
the base-synced (hash-locked) host frontend and every Module-Federation remote
import — and dedupe — a single copy of the kit rather than each vendoring their
own. Generic by design: no consumer branding lives here. Look-and-feel is a
downstream skin (tokens as data), never code in the kit.

## What ships here

- **Plugin host** (`@codefly/ui/plugin-host`) — product-neutral React
  contribution composition, re-exported from `@codefly/saas-plugin-react` so
  host and remotes resolve one instance. Client adapters are on the
  `./plugin-host/runtime` and `./plugin-host/ui` subpaths.
- **Skin mechanism** (`@codefly/ui/skin`) — the tokens-as-data resolver: it
  overlays a validated, untrusted skin descriptor onto the compiled default and
  caches the result per host. Pure and env-free — the host supplies the
  delivery `SkinSource`s (mounted ConfigMap file, env blob); the kit never
  reads the environment or the filesystem itself.

The dashboard surface — Layout, the `<Dashboard>` component, chat, tiles, and
charts — lands in this package through its own follow-up issues.

## Entry points

| Import                        | Contents                                            |
| ----------------------------- | --------------------------------------------------- |
| `@codefly/ui`                 | Server-safe surface: plugin composition + skin      |
| `@codefly/ui/plugin-host`     | Contribution composition (`defineReactPlugin`, …)   |
| `@codefly/ui/plugin-host/runtime` | Client runtime adapters (`PluginRuntimeProvider`) |
| `@codefly/ui/plugin-host/ui`  | Client UI adapters (`PluginErrorBoundary`)          |
| `@codefly/ui/skin`            | `resolveSkin`, skin types                           |

`react`, `@codefly/saas-plugin-react`, and `@codefly/saas-plugin-contract` are
**peer** dependencies — the host provides them so it and its Module-Federation
remotes resolve one shared instance each. This matters most for
`@codefly/saas-plugin-react`, which carries the plugin-runtime React context: a
second copy would split that context and break `usePluginRuntime` in a remote.

## Skin resolution

```ts
import { resolveSkin } from "@codefly/ui/skin";

const skin = await resolveSkin({
  fallback: compiledDefaultSkin, // { appearance, branding }
  host: requestHost, // keys the per-host cache
  sources: hostConfiguredSources, // the host wires these from its environment
});
```

The first source returning a valid descriptor wins; an invalid one is logged
and skipped so the compiled default always renders.
