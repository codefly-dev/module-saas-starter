# AGENTS.md — frontend

The authenticated product, behind auth-gateway. Architecture is in
`FRONTEND_ARCHITECTURE.md` at the repository root; the page and plugin inventory in [../../FRONTEND_CATALOG.md](../../FRONTEND_CATALOG.md)
and [../../FRONTEND_PLUGINS.md](../../FRONTEND_PLUGINS.md). This file covers the
runtime-registration surfaces and the published client kit.

## Registering a solution's frontend half

`POST /api/solutions/register` (and `DELETE ?id=…`) at
`code/src/app/api/solutions/register/route.ts`. The POST body is the solution
manifest — `id`, `nav`, `frontend.manifestUrl` + `exposedModule`, optional
`backend.serviceAlias`, optional compatibility requirements — validated in
`src/solutions/registry.ts`, which then writes the frontend half **through the
gateway** (`POST /solutions/_frontend`).

`sources` is the optional declaration of what a solution is **built on**:
`[{ provider: github, repo: owner/name, paths, ref, label }]`. `repo` is held
to `AddGitHubSourceRequest`'s own pattern, and the paths and ref to its bounds,
at the far end of the same journey — the declaration is submitted unedited by
the kit's `<DeclaredSourceCard>`, so a repository the connect RPC would refuse
is refused when it is *declared* rather than weeks later when somebody presses
Connect. A malformed declaration fails the registration whole, as a dashboard
or a surface does, and one repository declared twice is refused outright:
nothing downstream could say which of the two a card renders. It is a
statement, not an authority — declaring a source connects nothing and grants
no read of its contents. The solution page reads it in process and hands it to
the remote it mounts as `SolutionBinding.declaredSources`, so it is on neither
the public nor the internal HTTP projection and no new RPC exists to serve it.

The route **relays the registry's own answer**: `409` for a revision conflict,
`403` when the id belongs to another publisher, `422` (`registration_rejected`)
when the registry will not admit the manifest, `503` when the registry cannot be
reached — and `503` (not `401`) when the key set it verifies the credential
against cannot be reached, because a credential that was never judged was not
refused. A registrant is never told it is serving when it is not.

A dashboard event carrying `fields` declares an audit event type the solution
owns (`packages/saas-plugin-manifest/src/data-graph.ts`). `assertDataGraph`
checks its shape here; accounts admits it into the audit registry in the same
write as the frontend half, and refuses the write — the `422` above, with a
`detail` naming the rule — when the namespace is not bound to the solution or
belongs to another producer, or a changed field set drops or retypes an
admitted field. accounts marks that refusal with a `google.rpc.ErrorInfo`
reason (`SOLUTION_AUDIT_DECLARATION_REJECTED`), and only that reason becomes
the gateway's `422 registration_rejected`; any other refusal keeps its old
mapping. Re-registering
a deregistered solution requires an explicit `reactivate: true`. The frontend
additionally enforces the declared runtime compatibility requirements before
activating a remote.

## Three projections, deliberately separate

- `GET /api/solutions/register` is **unauthenticated** and returns exactly the
  public navigation projection — `{id, nav}` per solution and nothing else. It is
  what the sidebar polls, and it answers `503` — **never an empty list** — when
  this replica cannot read the registry.
- `GET /api/solutions/surfaces?client=<kind>`
  (`src/app/api/solutions/surfaces/route.ts`) is the same class of public
  projection for a client that is **not** this host's web app: per solution, its
  `id`, its title, its `origin`, and the declared `surfaces` whose `client`
  matches. The kind is required and must be slug-shaped — absent is `400`,
  malformed is `400`, never the unfiltered set and never a misleading empty
  list — and an unreadable registry is again `503`. The host does not enumerate
  client kinds: which ones exist is deployment configuration (the
  registered-client registry), so a new kind needs no host release.
  The `origin` is the one piece of topology this projection does carry, and
  deliberately: a surface's `module` is a path the client fetches against that
  origin, so withholding it would not keep the origin from anyone who can use a
  surface — it would only make the answer unusable. The manifest path and the
  backend service are still withheld.
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
  and `features/platform-catalogue/model/facts.ts` renders it as a fact this host
  cannot state rather than a green cell. A reader sees green and stops looking,
  which is why that one cell is checked and the two that report trouble are not.
  The browser checks only that each piece of evidence is *present*: how long an
  observation speaks for the present is the host's to decide, and a copy of that
  window here would drift from it.

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

## The published client kit

`code/packages/saas-sdk` is `@codefly-dev/saas-sdk`, published to GitHub Packages
on every `v0.0.N` deploy-counter tag. A contract change that reaches that tree
needs a `version:` bump in its `package.json` — `scripts/publish-frontend-kit.mjs`
refuses to republish a version whose contents moved, and that refusal fails the
release. The package's `exports` map may not name a subpath into the generated
tree: a consumer imports the SDK's own surface, never a stub path
(`publish-frontend-kit.test.mjs` enforces it). See the `cut-a-release` skill.

`@codefly-dev/ui` and `@codefly-dev/saas-ui` publish beside it. The kit owns its
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
