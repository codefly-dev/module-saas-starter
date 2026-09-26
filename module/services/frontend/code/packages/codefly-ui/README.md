# @codefly-dev/ui

The shared, versioned Codefly SaaS frontend kit. It is a standalone package so
the base-synced (hash-locked) host frontend and every Module-Federation remote
import — and dedupe — a single copy of the kit rather than each vendoring their
own. Generic by design: no consumer branding lives here. Look-and-feel is a
downstream skin (tokens as data), never code in the kit.

The layered design every primitive is checked against — the tier stack, the
compose-don't-re-inline / singleton / pure-data-in invariants, and the guard
that enforces them — lives in [ARCHITECTURE.md](./ARCHITECTURE.md). *Which*
components the kit ships — the agreed catalog (ships today / promote / propose)
and the v1 subset — is [CATALOG.md](./CATALOG.md).

## What ships here

- **Plugin host** (`@codefly-dev/ui/plugin-host`) — product-neutral React
  contribution composition, re-exported from `@codefly/saas-plugin-react` so
  host and remotes resolve one instance. Client adapters are on the
  `./plugin-host/runtime` and `./plugin-host/ui` subpaths.
- **Skin mechanism** (`@codefly-dev/ui/skin`) — the tokens-as-data resolver: it
  overlays a validated, untrusted skin descriptor onto the compiled default and
  caches the result per host. Pure and env-free — the host supplies the
  delivery `SkinSource`s (mounted ConfigMap file, env blob); the kit never
  reads the environment or the filesystem itself. The shared token vocabulary
  every layer above consumes by name — names, roles, and light/dark defaults —
  is the contract in [TOKENS.md](./TOKENS.md).

- **Layout** (`@codefly-dev/ui/layout`), **Dashboard**
  (`@codefly-dev/ui/dashboard`), and **Chat** (`@codefly-dev/ui/chat`) — pure,
  data-in presentation. `layout` carries the page containers (`Card`, `Section`,
  `Tabs`) and the shadcn primitives promoted into the kit as their single sealed
  home (issue #451) — actions (`Button`), forms (`Input`, `Textarea`, `Label`,
  `Checkbox`, `Switch`, `Select`), data display (`Badge`, `Avatar`, `Table`,
  `Skeleton`, `Separator`) and overlays (`Dialog`, `AlertDialog`, `Notice`, `Tooltip`,
  `DropdownMenu`); `dashboard` is `<Dashboard>`, charts, `fromDashboardData`; `chat`
  is `<Chat>`. React only: no plugin runtime, no host context.
  This is the surface a solution fe-remote consumes. `<Chat>` is fed by
  `@codefly-dev/saas-sdk`'s `useChatStream` — the hook owns the SSE/WS transport, the
  component stays pure, the same split as `runDashboard` → `<Dashboard>`.

- **Content** (`@codefly-dev/ui/content`) — the one way to render text the host
  did not write: ingested documents, model output, payloads. `<Content value
  format>` takes `markdown` (GFM), `json` (a collapsible, paged, copyable tree),
  `code` (monospace, syntax highlighting loaded on demand), `text` (whitespace
  kept) or `auto` (parseable JSON → json, markdown markers → markdown, else
  text), as a `block` or as one `inline` line of plain text for list rows and
  excerpts. The building blocks — `Markdown`, `JsonView`, `CodeBlock`,
  `TextBlock` — are exported too. Untrusted by default: no raw HTML, links only
  for http/https/mailto with `rel="noopener noreferrer"`, images off unless
  `allowImages`, and no `innerHTML` anywhere; colour only through the host's
  token utilities.

## Entry points

| Import                        | Contents                                            |
| ----------------------------- | --------------------------------------------------- |
| `@codefly-dev/ui`                 | Server-safe surface: plugin composition + skin      |
| `@codefly-dev/ui/plugin-host`     | Contribution composition (`defineReactPlugin`, …)   |
| `@codefly-dev/ui/plugin-host/runtime` | Client runtime adapters (`PluginRuntimeProvider`) |
| `@codefly-dev/ui/plugin-host/ui`  | Client UI adapters (`PluginErrorBoundary`)          |
| `@codefly-dev/ui/skin`            | `resolveSkin`, skin types                           |
| `@codefly-dev/ui/layout`          | `Card`/`Section`/`Tabs` + shadcn primitives (React-only) |
| `@codefly-dev/ui/dashboard`       | `Dashboard`, charts, `fromDashboardData` (React-only) |
| `@codefly-dev/ui/chat`            | `Chat` (React-only)                                 |
| `@codefly-dev/ui/content`         | `Content`, `Markdown`, `JsonView`, `CodeBlock`, `TextBlock` (React-only) |
| `@codefly-dev/ui/type-slots.css`  | The generated type-slot and control-rung utilities  |

`react`, `@codefly/saas-plugin-react`, and `@codefly/saas-plugin-contract` are
**peer** dependencies — the host provides them so it and its Module-Federation
remotes resolve one shared instance each. This matters most for
`@codefly/saas-plugin-react`, which carries the plugin-runtime React context: a
second copy would split that context and break `usePluginRuntime` in a remote.

The two plugin peers are **optional** (`peerDependenciesMeta`): only `.`,
`./plugin-host`, and `./skin` touch them, and the host supplies them. The
`./layout`, `./dashboard`, `./chat` and `./content` subpaths reference neither, so a consumer
of just those subpaths installs the kit without pulling the host-internal plugin
packages. `./layout` does pull the primitives' public runtime deps
(`@base-ui/react`, `lucide-react`, `class-variance-authority`, `clsx`,
`tailwind-merge`), declared as ordinary `dependencies` so a consumer resolves them
from the public registry with no extra config.

## Consuming from a solution

A solution fe-remote imports `@codefly-dev/ui/layout` + `@codefly-dev/ui/dashboard` and
shares them as Module-Federation singletons served by the host. Because the
plugin peers are optional, the solution only needs an `.npmrc` pointing the
`@codefly-dev` scope at the GitHub Packages registry (with a read token) plus a
`react` peer it already has:

```
@codefly-dev:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${GITHUB_PACKAGES_TOKEN}
```

`npm ci` then resolves `@codefly-dev/ui` with no reference to the unpublished
`@codefly/saas-plugin-*` packages.

**Styling.** The kit's components name their type slots and control rungs as
classes (`type-card-title`, `control-sm`) that the kit defines, not Tailwind. A
consumer that compiles the kit's source with its own Tailwind build imports the
generated stylesheet into its entry alongside its `@source` for the kit:

```css
@import "tailwindcss";
@import "@codefly-dev/ui/type-slots.css";
@source "../node_modules/@codefly-dev/ui/src";
```

Those utilities read custom properties (`--type-card-title-size`, …) that the
host projects onto `<html>` from the resolved skin. A remote renders inside the
host's document, so it inherits them; nothing else has to be set up. Every
utility sits at zero specificity, so a raw Tailwind class on the same element
(`text-lg`, `h-11`) overrides that one property and leaves the rest of the slot
standing — see [TOKENS.md](./TOKENS.md).

**Sealed downward.** A solution composes the kit but must not shadow it: the
host shares each layer package (React + kit + each module UI) as a
Module-Federation `singleton`, and a solution's own build shares them the same
way, so at runtime the solution renders against the host's one instance instead
of a copy it bundles. You build on the kit; you don't monkey-patch it. This is
separate from version pinning below (which version the singleton resolves to);
sealing is that there is a single shared instance at all. See invariant 5 in
[ARCHITECTURE.md](./ARCHITECTURE.md).

**Version discipline.** The kit is a Module-Federation singleton: at runtime the
solution shares the host's single instance. A solution must therefore pin an
`@codefly-dev/ui` that is semver-compatible with the version this host ships.
`@codefly-dev/ui`'s own `version` is the coupling point — it bumps whenever the
kit's published content changes, and CI publishes that exact version from the
release commit, so the published bytes are the bytes the host serves. Pin the
version the host module release ships (the two move together on every release
tag).

CI enforces this rather than trusting it, in two places. On every pull request,
`scripts/ci/kit-version.mjs` compares the kit's content against the newest
release tag — the baseline of what the registry serves — and fails when it
changed under an unchanged `version`. At release time the publish compares the
freshly built tarball's integrity against the version already on the registry: a
release that didn't touch the kit re-publishes nothing (same bytes → skip), and a
release that changed the kit *without* bumping `version` fails rather than
overwrite an immutable version, so a stale `@codefly-dev/ui` can never silently
ship to solutions.

The version is co-versioned with `@codefly-dev/saas-ui` and with the host's
`CODEFLY_KIT_VERSION` (`src/solutions/SolutionOutlet.tsx`); the
`kit-shared-version` test pins all three together, so one bump means three
edits.

## Skin resolution

```ts
import { resolveSkin } from "@codefly-dev/ui/skin";

const skin = await resolveSkin({
  fallback: compiledDefaultSkin, // { appearance, branding }
  host: requestHost, // keys the per-host cache
  sources: hostConfiguredSources, // the host wires these from its environment
});
```

The first source returning a valid descriptor wins; an invalid one is logged
and skipped so the compiled default always renders.

`Banner` from `@codefly-dev/ui/layout` renders persistent polite feedback with
optional actions and dismissal. The caller owns data, authorization and read state.

## Every blocking surface has a way out

A surface that covers the page must always let the user leave it, and the kit
makes the alternative impossible to write rather than a matter of review:

- `DialogContent`, `SheetContent` and `CommandDialog` show the kit's close button
  by default. Hiding it takes an `escape` — `showCloseButton={false}` alone, or a
  computed `showCloseButton={someBoolean}`, does not compile (`EscapeProps`). The
  `escape` is a `ReactElement` (so not `null`) that the kit renders inside the
  popup: a `DialogClose`, a footer whose button closes it, a sign-out action.
- `Notice` is the floating, non-modal notice that asks for a decision — accept
  updated terms, choose tracking preferences. Its `escape` (`{ label, onSelect,
  presentation? }`) is required, and the kit always renders it as an enabled
  button in the notice's tab order, before the notice's own actions. There is no
  way to pass a disabled one: when the decision is unavailable, the escape is
  what keeps the user from being trapped behind the notice.
- `AlertDialog` closes on Escape and composes an `AlertDialogCancel`. The cancel
  is a child, which no type can see, so the host's
  `escapable-surfaces-contract` test refuses an `AlertDialogContent` without one
  — and refuses a raw `role="dialog"`/`role="alertdialog"` element anywhere in
  application source.

What no type or test can see is an `escape` a caller keeps disabled forever, or
an `onOpenChange` that refuses every close. Those stay the caller's to get
right.
