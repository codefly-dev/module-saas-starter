# AGENTS.md — frontend

The authenticated product, behind auth-gateway. Architecture is in
`FRONTEND_ARCHITECTURE.md` at the repository root; the page and plugin inventory in [../../FRONTEND_CATALOG.md](../../FRONTEND_CATALOG.md)
and [../../FRONTEND_PLUGINS.md](../../FRONTEND_PLUGINS.md). This file covers the
solution read projections and the published client kit.

## Reading declared modules and solutions

`src/solutions/registry.ts` validates the gateway's durable registry snapshot and
caches it for five seconds. It exposes reads only. An expired snapshot is dropped
when a refresh fails, so callers receive unavailable instead of stale authority.
The manifest includes navigation, frontend loading information, backend alias,
compatibility requirements and optional dashboard, surfaces and source metadata.
Both kinds use this reader, the `/s/<id>` page and the same federation loader.
The gateway stamps `kind` from the applied declaration; a remote manifest cannot
choose its routing kind. A shared module has one deployment, while each
organisation installs it and grants team access independently through the same
target-based entitlement projection. The organization administration destination
is labelled "Modules & solutions" and retains `/admin/solutions`; its empty
state directs an administrator to the platform operator when nothing is available.
Each installed row links to `/admin/solutions/<installation id>`, that
installation's setup page: what it is and whether it is READY (the host's
`InstallationReadiness`, which is not the record's health), the unmet conditions
with who clears each and the act that does, and the teams a grant reaches there.
It reads the same `ListOrganizationInstallations` the row came from and selects
its own installation out of it, so the page can never disagree with the row that
linked to it. Granting a team a solution CARRIES, in the same act, the access the
solution declares it consumes on every module it reaches: the page states the set
before anyone grants, and after a grant it shows what was carried and — in the
host's own words, from the grant's own response — any module where nothing could
be carried. There is no per-module grant step for a person to perform
([MODULE_INSTALLATION.md](../../MODULE_INSTALLATION.md#a-grant-of-a-solution-carries-the-module-access-it-declares-it-consumes)).
The platform catalogue explains the delivery prerequisite. Headless components
contribute no page.
For a module page, relative data requests through the injected `apiBase` reach
`/v1/<alias>/<suffix>`; for a solution they reach `/solutions/<alias>/<suffix>`.
Public GET/HEAD assets use the common `/solutions/<alias>/assets` surface for
both kinds. Platform Connect procedures still reach the gateway root.

These declarations grant no access to content. The solution page reads the full
manifest in process; browser projections carry only the fields described below.

Solution groups are optional presentation metadata (`id`, `title`, `order`),
stamped from the solution's applied declaration. The member navigation first
filters each solution by installation and entitlement, then groups the visible
items. Home and sidebar share that grouping and the live navigation poll; empty
groups disappear. A group has no install, grant or consent action. The platform
catalogue also groups headless solutions and refreshes every 15 seconds while
open; modules remain outside solution groups.
Rename, reorder and membership changes preserve solution links and access.

The gateway stamps an opaque `assetVersion` from the applied binding, target,
generation and release. Browser assets use
`/api/solutions/<id>/versions/<assetVersion>/proxy/assets/...`; the manifest,
remote entry and relative chunks share that directory. The surfaces projection uses the
page's asset version for both modules and solutions, including relative imports.
Remote builds must use `publicPath: "auto"` and relative asset paths.
The gateway serves a historical version only while its admitted history and
live target permit it; unknown or expired versions receive 410. This is a cache
identity, not an access credential. The shared
viewer navigation poll carries it, removes withdrawn pages and refreshes an open
page when the version changes. The old remote is unmounted during the change,
and a previous loading failure cannot poison the new generation.

This does not by itself make a rolling backend serve immutable assets: delivery
must also bind the resolved endpoint and packaged frontend to the same build.
The browser cache tests do not qualify that producer/deployment boundary.

## The solution Setup tab

Every `/s/<id>` page has a Setup tab beside App (and Dashboard): the solution's
roles, what each grants (scopes → module operations → agent tools, and the
permission ceiling) and who holds them. It renders the kit's `<SolutionSetup>`
(`@codefly-dev/saas-ui/solution`), the one Setup UI every host renders — the
local authority's setup page and solution playgrounds mount the same component
— through `SolutionSetupPanel`, which adds only what this host knows: the
session's organization, the people and teams it can name, and, on the admin
installation page, this host's own unmet conditions. Every fact and refusal on
the tab is the authority's answer through the host installation interface. The
solution is named by its target, its registered route alias.

The Setup loading state follows the kit's delay and minimum visible duration,
including when the authority answers immediately after the indicator appears.

The browser reaches that interface at exactly the procedures the local
authority's edge serves a browser (`src/lib/host-setup-procedures.ts`):
InstallationService List, Review, ListRoleGrants, GrantRole, RevokeRole and
OfferService ListOffers, ReviewOffer. `src/proxy.ts` forwards them from a host
page and the solution proxy from a remote's `apiBase`, both to the gateway
root; Install and Current are not forwarded. The admin installation page keeps
this host's own team access card (its installation record and the team grants
the gateway admits by) beside the shared component.

## Three projections, deliberately separate

Two of them are **per viewer** (issue #949): they answer the solutions the
caller's organization installed and the caller's teams were granted, not the
deployment-wide registered set.

- `GET /api/solutions` is **authenticated** and returns the navigation
  projection — `{id, nav, available, assetVersion, group?}` per entitled component. It is
  what the sidebar polls (through `authedFetch`, so a lapsed access token is
  exchanged and retried rather than emptying the menu). It answers `401` to an
  unauthenticated caller and `503` when the registry or the authority cannot be
  read — **never an empty list** for either.
- `GET /api/solutions/surfaces?client=<kind>`
  (`src/app/api/solutions/surfaces/route.ts`) is the same class of projection for a
  client that is **not** this host's web app: per entitled solution, its `id`, its
  title, its `origin`, whether it is `available`, and the declared `surfaces` whose
  `client` matches. The kind is required and must be slug-shaped — absent is `400`,
  malformed is `400`, never the unfiltered set and never a misleading empty
  list — and an unreadable registry is again `503`. The host does not enumerate
  client kinds: which ones exist is deployment configuration (the
  registered-client registry), so a new kind needs no host release.
  The `origin` is the one piece of topology this projection does carry, and
  deliberately: a surface's `module` is a path the client fetches against that
  origin, so withholding it would not keep the origin from anyone who can use a
  surface — it would only make the answer unusable. The manifest path and the
  backend service are still withheld.

**Where the identity comes from, and why not from here.** Neither route may derive
the organization. `src/lib/auth-session.ts` can read an `org` claim, but
`decodeJWTPayload` only base64-decodes — it verifies nothing — so a route that
narrowed on it would let a caller read another tenant's menu by editing one field.
These `/api/*` paths are also not proxied through the gateway (`src/proxy.ts`
forwards `/v1/*`, `/saas.accounts.v1.*` and the Setup tab's seven host
installation procedures only), so no `ext_authz` stamp reaches
them either. `src/solutions/entitlements.ts` therefore forwards the caller's
credential to `GET /solutions/_entitlements` on the gateway, which authenticates
it, projects the organization and viewer from its own check, and answers from
accounts. `module/tools/solution_registration_boundary_test.go` holds both halves:
the projections must consult that read, and must not name the local decoders.

**`available` is a client contract.** An installed, granted solution whose
installation is unhealthy stays listed with `available: false` — the grant exists,
so hiding it would send someone looking for one that already does. It is a new
field on both projections: the host's own sidebar renders such a solution disabled,
and **a registered client of the surfaces projection must honour it the same way**
— a client that ignores the field will offer an unavailable solution's surfaces as
usable. The solution proxy also checks per-viewer installation admission. `/s/{id}`
reads deployment-wide state; see SOLUTION_REGISTRATION.md §4 for this boundary.

**Failure answers.** Neither projection answers an empty list for a failure.
`401` — the viewer is not signed in; `403 no_organization` — signed in with no
organization; `403 forbidden` — the gateway refused the viewer's credential;
`429 rate_limited` — the organization spent its read budget (the gateway meters the
entitlement read as a StandardRead per organization); `503` — the registry or the
authority could not be read, **including** when the gateway refuses this frontend's
own cluster-internal token. That last case is a deployment fault and is never
relayed as `401`: the gateway names its own refusals in
`X-Codefly-Entitlement-Refusal`, so a server credential problem cannot tell every
user to sign in again.

**What is cached, and what is not.** The entitlement answer is read from the
authority on **every** request; it is never reused. Reusing it on a revision that
grant writes advance would be wrong, because the answer changes with no grant write
at all — a grant's `expires_at` passes, a member leaves a team, a role loses a
permission, an owner of record is demoted. What is memoized is only the shaping of
manifests into a projection, keyed on organization, viewer, a digest of the
entitlement answer just read, client kind and registry revision, so it can never
describe an answer other than the one this request received.

The narrowing itself lives in `src/solutions/projections.ts`, apart from
`src/solutions/registry.ts`: the registry also feeds `findSolution`, which decides
whether `/s/{id}` renders, and it stays installation-blind.
- Everything else a manifest carries (`frontend`, `backend`) is deployment
  topology, served instead by `GET /api/internal/solutions`
  (`src/app/api/internal/solutions/route.ts`), gated on the cluster-internal
  token.
- The dashboard graph is on none of them: the solution page reads it in-process
  through `findSolution`.
- The platform Catalogue (`/admin/platform/catalogue`) is not one of them either:
  it reads accounts' `PlatformAdminService.ListPlatformCatalogue`, super
  administrators only, and that response carries each registration's status,
  revisions, contract versions and leases with the manifest, upstream and service
  alias withheld (`../accounts/AGENTS.md`). Each entry shows the registry's six
  states — desired, authorized, applied, observed, withdrawing, retired — one
  column each, and a fact the host has no record of is rendered as its gap
  reason ("Not recorded", "Not observed"), never as an empty cell, a zero or a
  match.
- **"Running authorized" is shown only beside the evidence it rests on.** The
  server judges the verdict and re-checks it at its read boundary, so a response
  that still arrives claiming the approved execution runs with no approval, no
  build incarnation, no observed execution or no time of observation is a defect —
  and `features/platform-catalogue/model/facts.ts` renders it as **"Inconsistent"**
  rather than a green cell. That label is deliberately not one of the server's gap
  reasons: the server did not say it could not tell, it said something its own
  response contradicts, and conflating the two would hide a defect among the facts
  this host simply does not hold yet. A reader sees green and stops looking, which
  is why that one cell is checked and the two that report trouble are not.
  The browser checks only that each piece of evidence is *present*: how long an
  observation speaks for the present is the host's to decide, and a copy of that
  window here would drift from it.

## Drawing a solution's declared dashboard

`src/solutions/SolutionDashboard.tsx` **mounts** the shared kit's
`<Dashboard>` (`@codefly-dev/ui/dashboard`); it draws nothing itself. Which
tier each widget and each coverage state is drawn with is the module's
[AUDIT_METRICS.md](../../AUDIT_METRICS.md) ("How a resolved series is drawn") —
one renderer for the host's pages, a declared solution dashboard and a solution
remote alike.

What this file owns is only what is the host's, composed through that
renderer's slots:

- **the audit queries**, one per widget, so a widget whose metric errors fails
  alone instead of blanking its siblings. Each lives in its own component
  (`SolutionWidget`) that resolves the series and then mounts the kit's
  `DashboardWidget`, because React fixes the number of hooks a component may
  call and a renderer taking the whole view cannot hold a varying number of
  queries;
- **the viewer's arrangement** — the declared sections, and within and between
  them the tiles: reorder or move a tile by drag or arrow keys, remove one, add
  it back from a +, rename, reorder or remove a section, add one of their own.
  It is saved per viewer and organization (`dashboard-layout.ts`, version 2,
  still the labelled browser-storage STOPGAP) and resolved here into the view
  the kit draws — the viewer's sections, in the viewer's order, under the
  viewer's titles, each widget in the section the viewer put it in. The
  renderer's `layout` slot lays it out as the kit's `SortableBoard`, each
  section headed by the kit's `DashboardSectionHeader` with the host's grip,
  rename, + and options in it. Every + offers only the declared widgets the
  viewer removed, asks each one's query only while it is open (the tile's own
  query, so a tile added back draws from that answer), and says, muted, when
  the tile would have nothing to show ("No data yet.", "Telemetry
  incomplete."). A chart, bar or table tile spans two grid rows, so two number
  tiles stack beside it;
- **the ⓘ** that says what a tile counts, from which audit events, over what
  range and who caused the newest ones (`metric-info.ts`, the principal
  directory, the recent-events drill-down), handed to each widget as its
  header actions.

A widget's `format` comes from the same resolved answer as its series, because
`runDashboard` is where a declared share became percentage points; binding it
from the declaration instead would pair a percentage with a share's format.

Re-implementing any part of the drawing is refused by
`src/components/__tests__/kit.test.ts`: a declaration taking a name
`@codefly-dev/ui` exports must render the kit's component of that name or carry
a reviewed entry, `class-variance-authority` is not a dependency of this app so
a kit-shaped primitive cannot be built here, and the two chart surfaces
(`src/components/charts/chart.tsx`,
`src/features/dashboard/ui/charts/line-chart.tsx`) stay re-exports.

## Loading a remote, and its CSP

`/s/[solutionId]` (`src/app/(dashboard)/s/[solutionId]/page.tsx`) loads the
remote via `SolutionOutlet` from the registered `manifestUrl` + `exposedModule`,
read in-process from the registry. An unknown or non-serving id renders the
segment's `not-found.tsx` inside the product shell; an unreadable registry is an
error, not a 404.

`manifestUrl` is either an absolute http(s) URL — loaded from that origin, which
the browser must be able to reach — or a **root-relative path on the solution's
own backend** (`/assets/mf-manifest.json`), which the host serves same-origin as
`/api/solutions/<id>/proxy/assets/mf-manifest.json` (`browserManifestUrl` in
`src/solutions/registry.ts`). The gateway serves a solution's `/assets` and
`/.well-known` without a bearer for exactly this, so a pod never has to know an
address the browser can reach — and a remote registered as `http://localhost:…`
can never load in a deployed cell. The remote is fetched in the browser only: a
server render shows the loading state instead of loading it from Node.

`src/proxy.ts` **cannot** read that registry — Next runs the proxy in a context
that shares no module singletons with route handlers — so it asks the internal
detail lookup over loopback with the cluster-internal token, and adds every
registered manifest origin to the CSP of every signed-in document. A freshly
registered cross-origin remote therefore loads with no rebuild, including after a
client-side navigation. **Without that token the policy stays self-only and says
so in the log.**

A solution remote executes in the host origin with the viewer's credentials; the
trust model and the registration/installation/entitlement boundary are in
[../../SOLUTION_REGISTRATION.md](../../SOLUTION_REGISTRATION.md).

**Open, and recorded here so the next reader does not have to rediscover it.**
Nothing pins what a cross-origin manifest serves. An absolute URL on any origin is
accepted, admitting it adds that origin to the script source, and there is no
integrity digest — so a holder of a solution's registration credential can point a
registered remote at code of its choosing and have it execute in the host origin
with the viewer's session. The audit raised this as a minor finding because the
live policy lists no foreign origin and every deployed remote is same-origin today,
which bounds it to the credential-to-code path.

Closing it means one of two things, and both are decisions about the solution
contract rather than a defect to repair:

- **Accept only root-relative manifests.** This removes the cross-origin-remote
  shape described above, and with it the runtime origin derivation in
  `src/proxy.ts` — the listing fetch, its cache, its outage reporting and the
  policy-size guard exist only to carry foreign origins.
- **Pin each remote by a digest recorded when it was published.** This keeps the
  shape and needs a publication-time provenance record the registration contract
  does not have.

Either is its own change, with the owner choosing which shape the contract keeps.

## The published client kit

`code/packages/saas-sdk` is `@codefly-dev/saas-sdk`, published to GitHub Packages
on every `v0.0.N` deploy-counter tag. A contract change that reaches that tree
needs a `version:` bump in its `package.json` — `scripts/publish-frontend-kit.mjs`
refuses to republish a version whose contents moved, and that refusal fails the
release. The package's `exports` map may not name a subpath into the generated
tree: a consumer imports the SDK's own surface, never a stub path
(`publish-frontend-kit.test.mjs` enforces it). See the `cut-a-release` skill.

`@codefly-dev/ui`, `@codefly-dev/saas-ui`, `@codefly-dev/saas-plugin-contract`
and `@codefly-dev/saas-plugin-react` publish beside it, in dependency order. Plugin
peers use required semver ranges: GitHub Packages omits optional-peer metadata.
Every plugin entry point is a host-shared singleton; UI wrapper imports and
direct plugin imports must resolve the same runtime context. The release runs
`node scripts/test-registry-ui.mjs --registry` from a fresh consumer after
publication; the pre-release proof simulates the registry metadata omission. The kit owns its
token layer (`packages/codefly-ui/src/skin/theme.css`: token → utility, the
light/dark binding, the custom variants); `src/app/globals.css` only imports it,
so a new appearance token is bound there, not here. The kit also ships
`src/skin/preview.generated.css` (`@codefly-dev/ui/preview.css`) — the kit
compiled with the default skin, so a solution can preview its pages with no
host. It is committed and checked for staleness, because its bytes depend on the
contract and the lockfile, which the kit-version gate does not see: after
touching either, run `npm run generate:preview-stylesheet --workspace
@codefly-dev/ui`. It is for previews only and never loaded by the
host or a remote; what it may and may not be used for is in
`packages/codefly-ui/README.md` § "Previewing a solution without a host".

### The kit publishes its chart vocabulary

The five visualizations `@codefly-dev/ui/dashboard` draws are a published
contract, not a convention repeated in three packages. The kit ships two
documents — `@codefly-dev/ui/dashboard-catalog.json`, an [A2UI](https://a2ui.org)
catalog naming the dashboard components, and
`@codefly-dev/ui/dashboard-data.schema.json`, the data model those bindings
resolve against — generated from `packages/codefly-ui/src/dashboard/catalog.ts`
by `npm run generate:dashboard-catalog`. They are committed, because a consumer
freezes a catalog by the digest of the document it was approved under, so a
hand-edited document would be a state no regeneration reproduces; the kit suite
holds the bytes to the source.

Three consequences for anyone touching the dashboard types here:

- **Adding a visualization is a four-line change, and the compiler says so.**
  `WidgetVisualization` gains a member, `catalog.ts`'s component map gains its
  entry (a component name, or `null` for one only `<Dashboard>` draws), and
  the documents are regenerated. Skip any of it and the kit does not build.
- **Adding a `DashboardView` field is the same.** The published data model's
  properties are typed `Record<keyof Required<DashboardView>, …>`, so a field the
  schema does not describe fails to compile.
- **The vocabulary is also restated in `@codefly-dev/saas-sdk` and
  `@codefly/saas-plugin-manifest`**, and
  `code/src/solutions/__tests__/dashboard-vocabulary.test.ts` holds all three to
  one list — including the manifest validator's own runtime list, which no type
  can reach. A member added to one package alone fails there.

Details, and why the catalog is pinned at `protocolVersion: "0.9"` rather than
the candidate v1.0, are in `packages/codefly-ui/README.md` § "The chart
vocabulary is published, not just implemented".

## Two rules every admin surface owes its reader

Both are product rules the owner has raised repeatedly, and both are kept by a
shared implementation rather than by each surface remembering them.

**A loading indicator never flashes** — nothing for the first 200 ms, then at
least 300 ms once anything has appeared. The primitive is the kit's
(`useLoadingPhase`, `DelayedLoading`, `Spinner`, re-exported from
`@/shared/ui`); never add another spinner beside it. Two traps, both found in
this tree with the primitive already in scope: gating a branch on the wait
unmounts the indicator together with the floor that lives inside it, so pass the
wait in or lift the decision out with `useLoadingPhase`; and a surface with an
empty state needs the hook's third state, because falling through the pre-delay
window lands on the empty branch and says "nothing here" about a read still in
flight. Tables get all of this from `DataTable` — pass `isLoading`.
`packages/codefly-ui/README.md` § "A loading indicator never flashes" has the
detail.

A third trap, found by review of the round that added the first two: the rules
govern a *finished* read, and a query is not finished when `isPending` is false.
A disabled query (`enabled: !!orgId`) sits at `isPending` with `fetchStatus`
`"idle"`, so `isLoading` is false and a surface falls straight through to its
empty branch about a read that was never issued — gate on
`isPending && fetchStatus !== "idle"`. And `isFetching` is its own state: after a
write, the cached answer is behind the database until the refresh lands, which is
how a successful add sat beside a count of zero.

**An empty state says which of two things it means** — "there is nothing here"
versus "you are not allowed to see what is here". A region that reads `data` and
`isLoading` but never `isError` renders a refused or failed read as an empty one,
and on a permissions console that is not a blank state, it is a wrong answer an
administrator will act on: "nobody holds this role" shown to someone whose read
was denied. `src/shared/lib/read-outcome.ts` classifies a finished read as
`empty` / `forbidden` / `failed` and supplies the wording; a surface with an
empty state passes `isError` and `error` through it.

Testing `rows.length === 0` before `isError` is not enough, because TanStack
keeps the last successful answer when a refetch rejects: a success followed by a
refusal leaves the previous rows rendered and the denial unreachable. The two
failures part company here, which is what `mayKeepRetainedRows` and
`staleReadNotice` are for — a **refusal** is about this reader's authority over
those very rows, so they go; a **transient failure** is not, so they stay with a
note that they may be out of date. Blanking a good table because one request did
not come back is worse than the staleness.

**Where a refusal acts is the read, not the component.** Withholding rows at
render answers for the error in hand and leaves the rows in the cache, so the
next transient failure serves them again — authority is a property of the read,
and so is what the read may leave behind. A refused read therefore deletes the
retained snapshots for every cached key of what it was reading, in the query
function where `readOutcome` classifies it, before the rejection reaches any
component. `src/solutions/SolutionDashboard.tsx` is the worked example: one
widget, several cached revisions of its declaration, and a refusal that may
arrive after the viewer has navigated away or the page has unmounted, where no
effect of theirs is left to run. Delete the data and nothing else: removing the
entry cancels the read reporting the refusal and strands recovery, clearing it
restarts every active read of it so a standing 403 never settles, and
`setQueryData(key, undefined)` is a documented no-op. Recovery is then exactly
one thing — a read that succeeds.

Versioned GET/HEAD asset requests reach the gateway even if the current page is
headless or absent from the advertised manifest. The gateway owns historical
version admission and live target checks; the proxy validates the version shape,
forwards it from a fresh header set and preserves its Cache-Control and errors.
Historical identities never open data routes or grant organisation access.
