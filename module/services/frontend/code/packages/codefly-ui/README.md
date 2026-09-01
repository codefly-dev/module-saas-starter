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

- **Layout** (`@codefly/ui/layout`) and **Dashboard**
  (`@codefly/ui/dashboard`) — pure, data-in presentation (Tabs/Card/Section;
  `<Dashboard>`, charts, `fromDashboardData`). React only: no plugin runtime, no
  host context. This is the surface a solution fe-remote consumes.

## Entry points

| Import                        | Contents                                            |
| ----------------------------- | --------------------------------------------------- |
| `@codefly/ui`                 | Server-safe surface: plugin composition + skin      |
| `@codefly/ui/plugin-host`     | Contribution composition (`defineReactPlugin`, …)   |
| `@codefly/ui/plugin-host/runtime` | Client runtime adapters (`PluginRuntimeProvider`) |
| `@codefly/ui/plugin-host/ui`  | Client UI adapters (`PluginErrorBoundary`)          |
| `@codefly/ui/skin`            | `resolveSkin`, skin types                           |
| `@codefly/ui/layout`          | `Tabs`, `Card`, `Section` (React-only)              |
| `@codefly/ui/dashboard`       | `Dashboard`, charts, `fromDashboardData` (React-only) |

`react`, `@codefly/saas-plugin-react`, and `@codefly/saas-plugin-contract` are
**peer** dependencies — the host provides them so it and its Module-Federation
remotes resolve one shared instance each. This matters most for
`@codefly/saas-plugin-react`, which carries the plugin-runtime React context: a
second copy would split that context and break `usePluginRuntime` in a remote.

The two plugin peers are **optional** (`peerDependenciesMeta`): only `.`,
`./plugin-host`, and `./skin` touch them, and the host supplies them. The
`./layout` and `./dashboard` subpaths reference neither, so a consumer of just
those subpaths installs the kit without pulling the host-internal plugin
packages.

## Consuming from a solution

A solution fe-remote imports `@codefly/ui/layout` + `@codefly/ui/dashboard` and
shares them as Module-Federation singletons served by the host. Because the
plugin peers are optional, the solution only needs an `.npmrc` pointing the
`@codefly` scope at the GitHub Packages registry (with a read token) plus a
`react` peer it already has:

```
@codefly:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${GITHUB_PACKAGES_TOKEN}
```

`npm ci` then resolves `@codefly/ui` with no reference to the unpublished
`@codefly/saas-plugin-*` packages.

**Version discipline.** The kit is a Module-Federation singleton: at runtime the
solution shares the host's single instance. A solution must therefore pin an
`@codefly/ui` that is semver-compatible with the version this host ships.
`@codefly/ui`'s own `version` is the coupling point — it bumps when the kit's
public surface changes, and CI publishes that exact version from the release
commit, so the published bytes are the bytes the host serves. Pin the version
the host module release ships (the two move together on every release tag).

The release publish enforces this rather than trusting it: it compares the
freshly built tarball's integrity against the version already on the registry. A
release that didn't touch the kit re-publishes nothing (same bytes → skip); a
release that changed the kit *without* bumping `version` fails the publish, so a
stale `@codefly/ui` can never silently ship to solutions.

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
