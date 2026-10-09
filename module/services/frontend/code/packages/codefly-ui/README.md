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
  contribution composition, re-exported from `@codefly-dev/saas-plugin-react` so
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
  `Checkbox`, `Switch`, `Select`), data display (`Badge`, `Chip`, `List`,
  `DescriptionList`, `Avatar`, `Table`, `Skeleton`, `Separator`), feedback
  (`Banner`, `EmptyState`, `ErrorState`) and overlays (`Dialog`, `AlertDialog`, `Notice`, `Tooltip`,
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
  token utilities. `<Markdown>` also marks what it rendered with the bytes it
  came from (`sourceOffsets`) and lets the caller say where a link actually goes
  (`resolveLink`) — see *A rendered document an annotation layer can read*.

- **Board** (`@codefly-dev/ui/board`) — one collection in columns, one per value
  of a field, whose cards a reader drags from one column to another or moves from
  a menu. It commits nothing: a move is reported and the consumer confirms,
  writes or refuses it. See *A board commits no move*.

### Status, card headers, lists and empty states

A solution page is built from kit components, never from hand-laid utilities:
the kit is what makes such a page small. These exist because a consuming
solution measured where it had to reach past the kit.

- **Status tones.** `StatusTone` is `neutral | success | warning | danger |
  info`, shared by `Badge`, `Chip` and `Banner` so a page's statuses agree.
  `success`, `warning` and `info` are appearance tokens a skin may override
  ([TOKENS.md](./TOKENS.md)); `danger` reads `destructive`. A tone is a tint of
  its colour under text in it, and replaces a `variant`'s colours rather than
  stacking on them. Set up / Connected / Error is `neutral` / `success` /
  `danger`.
- **`<Badge tone dot size>`.** `dot` prefixes a decorative, `aria-hidden` circle
  in the badge's own colour, so a row of statuses is told apart by shape and
  not by colour alone; the text is still what a screen reader gets. `size` is
  `sm | default | lg`.
- **`<Chip>` / `<ChipGroup>`.** A badge a person can act on: `href` makes it a
  link, `onClick` a button, `render` swaps in a router link. `icon` leads,
  `meta` trails after a separator (`meta="unread"` reads "· unread"),
  `onRemove` + `removeLabel` add a remove control. The remove control sits
  beside the link, never inside it: a button in a link is two targets announced
  as one. `ChipGroup` is a named, wrapping list of them.
- **`<Card description>`** puts the card's one-line purpose under its title in
  the `card-description` slot, matching `Section`'s prop — not a muted
  paragraph in the body, and not `CardRoot` mixed into a page of `Card`s.
- **`<List>` / `<ListItem>` and `<DescriptionList>`** for the places a `Table` is
  too heavy: a few rows with an icon, a description, quiet `meta` and
  `actions` (`variant="divided"` rules lines between them), and term/value
  pairs, `inline` in two aligned columns or `stacked`.
- **`<Banner tone>`.** `neutral` (the default) is the product speaking and keeps
  the original look; the status tones add a glyph each. `danger` is
  `role="alert"`, the rest `role="status"`.
- **`<EmptyState reference>`.** "The source card above says where this stands"
  is a pointer, not an explanation (`description`) and not something to press
  (`children`).

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
| `@codefly-dev/ui/board`           | `Board` (React-only)                                |
| `@codefly-dev/ui/dashboard-catalog.json` | The dashboard components as an A2UI catalog — the exact document a consumer freezes |
| `@codefly-dev/ui/dashboard-data.schema.json` | The data model those catalog bindings resolve against |
| `@codefly-dev/ui/type-slots.css`  | The generated type-slot and control-rung utilities  |
| `@codefly-dev/ui/theme.css`       | The token layer: token → utility, light/dark binding, custom variants (Tailwind source) |
| `@codefly-dev/ui/preview.css`     | The kit compiled with the default skin, for previews only — see below |

`react`, `@codefly-dev/saas-plugin-react`, and `@codefly-dev/saas-plugin-contract` are
**peer** dependencies — the host provides them so it and its Module-Federation
remotes resolve one shared instance each. This matters most for
`@codefly-dev/saas-plugin-react`, which carries the plugin-runtime React context: a
second copy would split that context and break `usePluginRuntime` in a remote.

The plugin peers are published alongside the kit under `@codefly-dev` and are
required. GitHub Packages omits `peerDependenciesMeta` from version metadata;
marking unpublished peers optional therefore does not make a registry install
work. Normal npm resolution now installs a complete published peer graph.
The `./layout`, `./dashboard`, `./chat`, `./content`, `./board` and `./lifecycle`
entry points still do not import the plugin runtime; installing its peers does
not bundle them into those presentation entry points.

## Consuming from a solution

**A remote's declared range has to admit the version that has what it uses.** The
kit is versioned in 0.x and an additive release is a patch, so `^0.12.0` is
satisfied by 0.12.0 — which carries neither `Markdown`'s `sourceOffsets` nor
`@codefly-dev/ui/board`. A remote using either declares `^0.12.1`. (Registration
checks the declared range against the version the host publishes, so a range that
is too loose fails at runtime rather than at install.)

A solution fe-remote imports `@codefly-dev/ui/layout` + `@codefly-dev/ui/dashboard` and
shares them as Module-Federation singletons served by the host.
The solution needs an `.npmrc` pointing the
`@codefly-dev` scope at the GitHub Packages registry (with a read token) plus a
`react` peer it already has:

```
@codefly-dev:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${GITHUB_PACKAGES_TOKEN}
```

`npm ci` resolves the UI kit and its published plugin peers from the same scope.
UI imports are unchanged. A plugin author importing the former unpublished
`@codefly/saas-plugin-contract` or `@codefly/saas-plugin-react` must migrate both
imports and dependency keys to `@codefly-dev/saas-plugin-contract` and
`@codefly-dev/saas-plugin-react`; do not mix old and new runtime packages. Remotes
share the kit entry points they use as singletons. Direct plugin imports must
also share their exact entry points; the host publishes every entry point of
both plugin packages in the same sealed scope.

`node scripts/test-registry-ui.mjs` checks a fresh version-based install against
packed registry metadata that omits optional-peer flags. Release CI repeats it
with `--registry` against GitHub Packages after publication. Both proofs check
peer auto-installation, declarations, public exports, rendering, CSS and shared
plugin context without workspace links or peer-resolution bypasses.

**Styling.** The kit's components name their type slots and control rungs as
classes (`type-card-title`, `control-sm`) that the kit defines, not Tailwind. A
consumer that compiles the kit's source with its own Tailwind build imports the
token layer and the generated utilities into its entry alongside its `@source`
for the kit, as the host's own `globals.css` does:

```css
@import "tailwindcss";
@import "@codefly-dev/ui/theme.css";
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

### Notifications from a solution

The host mounts the kit's `<Toaster>`. A remote raises a notification with the
kit's own `toast` (`import { toast } from "@codefly-dev/ui/layout"`, e.g.
`toast.error("Couldn't delete this chat: <reason>. Try again.")`). Because
`@codefly-dev/ui/layout` is a shared singleton, the remote's `toast` is the
host's copy and writes to the store the mounted Toaster reads. Never import the
notification library directly in a remote: its bundled copy writes to a store no
Toaster reads, and the message is lost without a trace. Keep an in-place message
for anything the person must act on; a toast is a signal, not the record.

## The chart vocabulary is published, not just implemented

`./dashboard` draws five visualizations — `line`, `bar`, `area`, `number`,
`table` — and that list used to be a convention: written here in
`WidgetVisualization`, restated in `@codefly-dev/saas-sdk`'s data-graph schema and
in `@codefly/saas-plugin-manifest`, and retyped by any agent offering a
`visualization` enum. Nothing checked that they agreed.

The kit now publishes it once, as an [A2UI](https://a2ui.org) catalog:

- **`@codefly-dev/ui/dashboard-catalog.json`** names the components a renderer may
  be asked for — `Dashboard` (one resolved view, drawn whole), `DashboardGrid`,
  and `MetricLineChart` / `MetricAreaChart` / `MetricBarChart` / `StatTile` — with
  each property declared through A2UI's canonical `ComponentId` / `ChildList` /
  `Dynamic*` references. It is pinned at `protocolVersion: "0.9"`, the
  specification's production version: v1.0 is a candidate that spells a binding
  `{"@path": …}`, and the published renderers expose `v0_8`/`v0_9` and no `v1_0`,
  so a v1.0 catalog would describe properties no shipped client can bind.
- **`@codefly-dev/ui/dashboard-data.schema.json`** is the data model those bindings
  resolve to: `DashboardView` and the shapes beneath it. It describes series and
  carries no numbers. It is a second document because A2UI closes a catalog's
  root keys and limits its `$defs` to `anyComponent`/`anyFunction`, so a
  conforming catalog has nowhere to put a shared series schema; the catalog names
  this one by `$id` in its `instructions`.
- **`DASHBOARD_CATALOG`, `DASHBOARD_DATA_SCHEMA`, `DASHBOARD_VISUALIZATIONS`,
  `DASHBOARD_WIDGET_COMPONENT_BY_VISUALIZATION` and
  `dashboardCatalogComponents()`** are the same vocabulary as values, exported
  from `./dashboard`. The last one is the projection a consumer that *freezes* a
  catalog keeps — component names and their child-bearing properties — derived
  from the document's own references rather than retyped.

`table` maps to `null`: `<Dashboard>` draws a table body itself and the kit
exports no standalone table chart, so there is no component for a catalog to
name. The gap is a declared value, not a silence.

Both documents are generated from `src/dashboard/catalog.ts` by
`node scripts/generate-dashboard-catalog.mjs` (`--check` to fail on a stale one),
and they are committed because a consumer freezes a catalog by the digest of the
document it was approved under. Three things stop the kit drifting from what it
publishes, and each has been shown to fail when it should:

1. The component map is `Record<WidgetVisualization, string | null>`, so a sixth
   visualization does not compile until the catalog names it or declares its gap.
2. The data model's properties are
   `Record<keyof Required<DashboardView>, JsonSchemaNode>`, so a view field added
   here does not compile until the published schema describes it, and a
   `required` list is checked against which fields are actually required.
3. `src/dashboard/__tests__/catalog.test.ts` holds the committed bytes to the
   source, holds the document to A2UI's rules for a catalog, and validates
   `fromDashboardData`'s output against the published data model — exercising its
   refusals, so an evaluation that accepts everything cannot pass for evidence.

`src/solutions/__tests__/dashboard-vocabulary.test.ts` in the host closes the
other half: the kit, the SDK and the manifest package must hold one list, and the
manifest's own validator must accept every visualization the catalog publishes
and refuse one it does not.

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

`Banner` from `@codefly-dev/ui/layout` renders persistent feedback with optional
actions and dismissal. The caller owns data, authorization and read state.

## Previewing a solution without a host

Every class a kit component writes is defined only where something compiled the
kit with its token layer, and every variable those classes read is set only
where a host projected a skin onto `<html>`. A solution authors no CSS, so on
its own it could not render its page at all. `@codefly-dev/ui/preview.css` is
that compile done once, in this package: plain CSS, the kit's own source only,
with the default skin's values at `:root`.

It is generated (`npm run generate:preview-stylesheet`) and committed, not built
at publish. Its bytes depend on inputs outside this folder — the contract's
default skin and the Tailwind, Lightning CSS and tw-animate-css versions in the
lockfile — which the kit-version gate cannot see. Committed, a change to any of
them shows up as a changed file here, and the host's `preview-stylesheet` test
fails until it is regenerated.

```ts
// The preview harness's entry — never a module the remote exposes.
import "@codefly-dev/ui/preview.css";
```

**A preview build may use it** to render, review and screenshot a solution's
pages and stories locally, and to run visual tests against them. Dark mode is
`class="dark"` on an ancestor, as in the host.

**A preview build may not use it for:**

- **Anything a host loads.** Import it only from the preview harness's own
  entry, never from a module the remote exposes or anything that module
  imports. A remote renders inside the host's document, under the host's one
  compiled stylesheet and the deployment's skin; bundled, this would ship a
  second Tailwind preflight and the default skin's values into that document.
- **Evidence of how a deployment looks.** It is the default skin. A
  deployment's skin changes colour, type, radius and density, so a preview
  screenshot shows structure and states, not the product's appearance.
- **Styling anything but kit components.** Only classes the kit's own source
  writes are compiled in. A utility a solution writes itself renders unstyled
  in preview, and that is deliberate: it is the hand-laid markup the kit exists
  to replace. `@codefly-dev/saas-ui`'s components are not covered either; they
  render inside the host's stylesheet.

The host's `src/lib/__tests__/preview-stylesheet.test.tsx` renders every story
here and fails if any class one of them carries is missing from the file, so
"built from kit components" and "painted in preview" stay the same claim.

**Structure travels inline when the host does not compile you.** The host's
stylesheet is compiled over this kit and `@codefly-dev/saas-ui` only (its
`@source` lines); it compiles nothing a solution remote or another module's kit
ships. A utility that exists only because one file wrote it — an arbitrary value
such as `grid-cols-[…]` or `h-[var(--x)]` — is therefore defined for this kit
and missing everywhere else. `DescriptionList`'s
`grid-cols-[minmax(0,max-content)_minmax(0,1fr)]` is fine here for that reason. A
kit element meant to render from a remote carries its structure inline (a
`style` for layout) and takes only colour and type from the tokens.
`src/__tests__/remote-safe-structure.test.ts` holds the components written
against that rule to it, and names the ones it does not yet cover.

## A rendered document an annotation layer can read

A reader selects words in a rendered document and writes a comment about them.
Mapping that selection back to the document is not a matter of counting
characters on screen, because **rendered text is not its source**: markdown drops
`**`, a heading loses its `#`, a list gains a bullet nobody wrote. So the
renderer says which bytes it rendered, and the reading layer never has to guess.

```tsx
const front = version.indexOf("# ");          // the body, without its front matter
<Markdown
  headingLevel={2}
  sourceOffsets                               // mark what was rendered
  sourceStart={utf8Length(version.slice(0, front))}
  resolveLink={(href) => resolve(href)}       // where a link in this corpus goes
>
  {version.slice(front)}
</Markdown>
```

| Attribute | On | Means |
| --- | --- | --- |
| `data-source-start`, `data-source-end` | every block, and a `<span>` around every text run | the element's text was rendered from bytes `[start, end)` of the version |
| `data-source-exact="false"` | anything markdown rewrote | the text is not those bytes verbatim, so a selection inside it widens to the whole element |
| `data-source-ignore` | a code block's copy control | chrome the renderer added; it maps to nothing |

This is the contract a composed module's annotation kit already reads through
its text-range locator (its own `src/anchors/source-map.ts`); this kit matches it
rather than inventing one.

- **Offsets are UTF-8 bytes, half-open, counted in the version** — not string
  indices. `é` is one index and two bytes, an emoji two indices and four, so a
  document of plain ASCII passes a renderer that forgot the difference.
  `sourceStart` is the byte offset of what you passed inside the whole version,
  so a body rendered without its front matter still names the version's bytes.
- **A plain run is byte-exact; everything else says so.** Exactness is *measured*
  — each element's and each run's text is compared with the source it claims —
  rather than listed, so a construct nobody anticipated is marked inexact instead
  of lying. A fenced block is the shape in miniature: the `<pre>` carries the
  whole fence and is never exact, while the `<code>` inside carries the body
  alone and is, so a reader comments on the block as a whole and selects inside
  it character for character.
- **The two source-rewriting options are off.** `references` protects a `[n]`
  marker by rewriting the source before it is parsed, which would move every
  offset after it, so it is not applied under `sourceOffsets`. `lineBreaks` is
  applied: it splits a run rather than the source, and each piece keeps its own
  bytes.
- **Chrome the source did not write** — a GFM footnote's generated heading, its
  `↩` back-reference — has no range of its own and maps to its nearest marked
  ancestor as a whole.

`resolveLink` answers the other half. A link in a corpus is usually a path in a
repository (`../decisions/x.md#why`), and only the caller knows where that goes:
it returns `{ href, external }` for a destination it knows, `{ open }` to
navigate inside the product with no URL at all, `{ unavailable }` for a target it
cannot show, or `undefined` to leave the kit's own rule in place. **A resolved
href is held to exactly the same allowlist as one the content wrote** — http,
https or mailto, no credentials in the authority — so no resolver can turn
`javascript:` into a link.

That allowlist refuses a **relative** result too, so `{ href: "#p=guide" }` or
`{ href: "/pages/guide" }` renders as inert text rather than a link: a resolver
returning an in-product route must give `{ open }` (the kit navigates nowhere by
itself) or an absolute URL. The one exception is `linkBase`, which resolves a
relative result against the caller's own trusted location first and then applies
the same rules.

## A board commits no move

```tsx
import { Board } from "@codefly-dev/ui/board";

<Board
  items={requests}
  columns={[{ id: "open", label: "Open", empty: "Nothing waiting" }, …]}
  columnOf={(request) => request.stage}       // the field it groups by
  idOf={(request) => request.ref}
  labelOf={(request) => request.name}         // what to call one, in a sentence
  renderCard={(request) => <RequestCard request={request} />}
  onOpen={(request) => open(request)}
  onMove={(request, column) => confirmThenWrite(request, column)}
  canMove={(request, column) => mayMove(request, column)}  // optional
  searchText={(request) => `${request.name} ${request.owner}`}
/>
```

- **The board changes nothing.** A drop, or a choice in a card's Move menu, calls
  `onMove(item, column)` and stops there. The consumer confirms it with the
  reader, writes it, or refuses it, and passes `items` back with the new column. A
  board that moved the card itself would be showing a state the server never
  agreed to, and would have to take it back when the write failed.
- **Dragging is never the only way.** A pointer drag is a gesture a keyboard
  cannot make and a screen reader cannot see, so every card that may move carries
  a **Move** menu listing the columns it may go to, beside the control that opens
  it. (This is where `SortableGrid` asks its caller for the keyboard
  alternative — a board knows what the alternative is, so it ships it.) The
  screen-reader instructions name that menu rather than the arrow keys dnd-kit
  assumes.
- **It knows nothing about what a column means.** No status, no workflow, no
  vocabulary: `columnOf` names the field and `columns` names its values, so two
  columns or five cost the same. `canMove` narrows which moves exist at all — a
  column it refuses accepts no drop and is absent from the Move menu, and a card
  with nowhere to go does not drag.
- **Search is the consumer's.** `searchText` says what a search matches an item
  against; without it there is no box, because only the consumer knows which
  fields are worth searching.
- Structure is inline (see *Structure travels inline when the host does not
  compile you*), which `src/__tests__/remote-safe-structure.test.ts` holds it to:
  a board renders from a remote, where only what the host already compiled exists.

## A loading indicator never flashes

A product-wide rule, and it lives here so every kit gets it by composing rather
than by remembering: **an indicator that appears and vanishes tells the reader
nothing they could not already see, while pulling their eye off what they were
reading.** Two numbers make that unwritable-otherwise:

- Nothing is shown for the first **200 ms** (`LOADING_DELAY_MS`). Most waits end
  inside that window — a warm cache, a local read — and they render their
  answer with no intervening state at all.
- Once something *has* appeared it stays for at least **300 ms**
  (`LOADING_MIN_VISIBLE_MS`). Without a floor, a wait that ends just past the
  delay shows its indicator for a few milliseconds: the same blink, moved later
  rather than removed.

```tsx
import { DelayedLoading, Spinner, useDelayedLoading } from "@codefly-dev/ui/layout";

// The common case: the kit owns the timing and the indicator.
<DelayedLoading active={query.isPending} label="Loading data sources" />

// Your own indicator, the kit's timing.
<DelayedLoading active={query.isPending} label="Loading data sources">
  <PanelMessage>Loading data sources…</PanelMessage>
</DelayedLoading>

// The decision alone, for a surface that is not a subtree — a disabled button,
// a row that dims, an aria-busy on a container you already render.
const busy = useDelayedLoading(query.isPending);
```

`active` is simply whether the thing you are waiting for is outstanding; the
hook owns everything else. A wait that restarts while the indicator is up does
**not** restart the delay, so a series of quick refetches cannot tear the
indicator down and build it back up — that is the same flicker arriving by
another route. `delayMs` and `minVisibleMs` are overridable per call for a
surface whose timings genuinely differ; prefer the defaults, because the point
of a shared rule is that surfaces agree.

`Spinner` is the kit's busy indicator — `role="status"`, polite, with a
**required** `label`, because a spinner with no accessible name announces that
*something* is happening, which is the one thing the reader can already see.
Rendered directly it has no delay and will flash; reach for `DelayedLoading`
unless you are inside something that already gates it.

The clock is read in effects and timers, never during render, so two renders of
the same state can never disagree about what is on screen.

**A surface with an empty state needs three states, not two.** Feeding the
delayed boolean straight into a renderer says "not loading" during the delay,
and the branch after that is usually the empty one — so "No documents yet", and
its invitation to go and connect something, flashes for the first 200 ms of
every load. That is worse than the indicator the delay was meant to spare the
reader, because it is not merely noise, it is wrong. Every list, table and panel
with an empty state has this shape:

```tsx
const { indicator, quiet } = useLoadingPhase(query.isPending);
if (indicator) return <Spinner label="Loading documents" />;
if (quiet) return null;              // too early to say anything at all
if (!documents.length) return <EmptyState … />;
```

`DelayedLoading` cannot express this — a component that renders `null` while
hidden gives its caller no way to tell "hidden because idle" from "hidden
because it is too early to speak" — so reach for `useLoadingPhase` wherever
"nothing yet" is not neutral.

**Pass the wait in; never gate the mount on it.** This is the one way to hold
both numbers and still flash, and it reads as correct:

```tsx
// WRONG. The delay works; the floor cannot.
{query.isPending ? (
  <DelayedLoading active label="Loading data sources">…</DelayedLoading>
) : rows.length === 0 ? <EmptyState … /> : <Table … />}
```

The minimum-visible floor lives in state *inside* the component, so a branch
gated on the wait unmounts the indicator — and its floor — at the instant the
answer lands. A response at 210 ms shows the indicator for 10 ms: the blink the
floor exists to prevent, arriving by the one route the delay does not cover.
Either keep the component mounted and let `active` carry the wait, or lift the
decision out with `useLoadingPhase` and branch on `indicator` / `quiet` — which
is also what gives you the third state the empty branch needs. Found three times
in one panel in this repo, with the primitive already imported.

**`DataTable` already owns this.** Every table in the product renders through it,
so the rule is kept in one place: pass `isLoading` and it holds the frame and its
column headers for the first 200 ms, then shows a skeleton that stays long enough
to read, and it never falls through to `emptyMessage` while the read is
outstanding. The frame is held rather than returning `null`, because a query key
that changes under a mounted table — switching organization — puts `isLoading`
back to true with rows on screen, and `null` would collapse the table to nothing
and bring it back. A header is neither an indicator nor an answer about the data,
so holding it breaks neither rule. `DataTableSkeleton` is
that skeleton's appearance on its own, with no timing — for an example or a test
that exists to *show* it. A product surface passes `isLoading` to `DataTable`
and does not reach for the skeleton directly.

**Do not put any of these inside a `Suspense` fallback.** They hold state, and a
fallback that holds state makes React re-render the boundary's *content* when
that state settles: one mount and two renders of the child, which re-runs its
`useMemo` and can rebuild whatever that memo constructs. Gate it at the
boundary's parent instead; a fallback should be a pure element.

`Spinner` fades rather than rotates for a reader who asked for reduced motion
(`motion-safe:animate-spin` / `motion-reduce:animate-pulse`). Rotation is
vestibular-triggering, and dropping the animation entirely would leave a ring
that sits still and conveys nothing — both forms say "busy"; only one of them
moves through space.

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

## Navigation, selection and expandable sections

Available from `@codefly-dev/ui/layout` in **0.12.2**:

- `Disclosure title headingLevel` renders one expandable section. It accepts
  Base UI Collapsible state props (`open`, `defaultOpen`, `onOpenChange`).
  `Accordion`, `AccordionItem`, `AccordionHeader`, `AccordionTrigger` and
  `AccordionContent` expose Base UI's grouped behavior, including `multiple`.
- `Breadcrumb items` takes stable `id`, `label`, optional `href` and
  `onNavigate` per item. The last item is the current page, never a control.
  Long trails wrap. A callback intercepts the supplied link; it owns routing.
- `RadioGroup` and `Radio` retain Base UI's controlled values, form naming,
  validation, disabled behavior and keyboard selection. Give the group a name
  for assistive technology and associate each radio with a label.
- `Surface` paints the card surface and border without padding, flex direction,
  gap or shadow. It is the primitive for a caller-owned layout inside a border.
- `Timeline entries label` preserves the caller's sequence. Each entry has an
  `id`, `title`, optional `description`, `icon`, `actions`, and optional
  `time: { dateTime, label }`. Missing time stays absent; distance never encodes
  duration. The caller owns event meanings and timestamp formatting.

`Tree` takes `items` and an accessible `label`. Nodes carry unique `id`, `label`
(non-interactive content), and `textValue` for naming and typeahead, with optional
`children`, `hasChildren`, `loading` and `disabled`. Arrow keys navigate and
expand/collapse, Home/End move to the extremes, and Enter/Space invoke `onSelect`.
Focus does not imply selection. Disabled nodes remain discoverable but cannot
be selected or expanded. `selectedId` is controlled; the tree commits nothing.

Expansion can be uncontrolled (`defaultExpandedIds`) or controlled
(`expandedIds`/`onExpandedChange`). `onLoadChildren` asks the owner for missing
children on expansion; it does not fetch them. `loading` sets busy semantics and
uses the kit's delayed indicator. A changed `focusedId` reveals an externally
requested node once it becomes visible without stealing browser focus; callers
must expand its ancestors. Rows expose `data-node-id` for owner integration.

For large collections, `virtualize: { height, rowHeight, overscan? }` enables
fixed-height windowing. All visible rows must fit the chosen height. The active
row remains in the DOM even if pointer scrolling moves it outside the viewport,
so `aria-activedescendant` never names an unmounted item. Hierarchy levels,
positions and set sizes describe the complete collection. A collapsed focused
branch returns the active descendant to its nearest visible ancestor.

Owner stories in `stories/navigation.stories.tsx` demonstrate these controls
and a searchable multi-selection composition using the existing Popover,
Input, Checkbox and Chip primitives. The composition does not infer permissions
or interpret the selected values.

## Owning asynchronous viewer lifetimes

`@codefly-dev/ui/lifecycle`, first available from this release line at **0.12.2**,
exports `mountIsolated` and `createTaskTracker`. These are framework-independent
lifetime helpers, with no service client, domain data, authentication or network
access. The host shares this subpath as a versioned federation singleton too.

`mountIsolated(host, mount, { onReady, onError })` gives each asynchronous mount
its own child element and AbortSignal. `retire()` aborts and removes only that
child; a handle that arrives after retirement is disposed instead of presented.
Disposal happens once. Errors include whether that mount was already retired.
The caller remains responsible for its own error-reporting callback.

`createTaskTracker<Context, Outcome>()` captures immutable context identity when
work begins. Replacing or invalidating current presentation does not cancel
pending work or discard its eventual outcome. Settle each owned task once;
release outcome resources before `forget`. Pending tasks cannot be forgotten.
Enumeration uses explicit, validated offset and limit values.

These authored helpers recover the contract carried in a prior source archive
identified as `246c3905cc157c96abe0ab48bdb0af54ef15c8dd`, whose version label was
also 0.12.0. That archive is not evidence that the published 0.12.0 exports this
subpath. Consumers must require **0.12.2 or later**, and a PR tested against a
source-packed candidate must record its commit and archive hash without claiming
that candidate has been published. The lifecycle tests cover stale completion,
setup failure, retirement, retained outcomes and ownership errors.

`ViewportOverlay` draws decorative rectangles and polygons in its positioned parent’s CSS-pixel frame, with pointer events disabled. It leaves selection, geometry and content ownership to the caller. Its authored source was recovered from the same archive identified in the lifecycle provenance above. Stacked `DescriptionList` bounds its grid track so long values wrap within narrow containers.

Use `Disclosure keepMounted` when closing a section must preserve descendant drafts or nested expansion state. The default lazily unmounts the content; `keepMounted` is forwarded to the panel, not the root.

Version 0.12.3 refreshes the default preview palette from appearance contract 2.4.1: small muted captions now meet 4.5:1 on the default muted surface. This supersedes the 0.12.2 development candidate without changing the new component APIs.

Version 0.12.4 also makes destructive/danger Badge text mix 30% toward the active
foreground, retaining its status hue while meeting small-text contrast on its
tinted background in the tested default and supplied light/dark skins.
