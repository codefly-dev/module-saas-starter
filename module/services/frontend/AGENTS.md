# AGENTS.md — frontend

The authenticated product, behind auth-gateway. Architecture is in
`FRONTEND_ARCHITECTURE.md` at the repository root; the page and plugin inventory in [../../FRONTEND_CATALOG.md](../../FRONTEND_CATALOG.md)
and [../../FRONTEND_PLUGINS.md](../../FRONTEND_PLUGINS.md). This file covers the
solution read projections and the published client kit.

## Reading declared solutions

`src/solutions/registry.ts` validates the gateway's durable registry snapshot and
caches it for five seconds. It exposes reads only. An expired snapshot is dropped
when a refresh fails, so callers receive unavailable instead of stale authority.
The manifest includes navigation, frontend loading information, backend alias,
compatibility requirements and optional dashboard, surfaces and source metadata.
These declarations grant no access to content. The solution page reads the full
manifest in process; browser projections carry only the fields described below.

## Three projections, deliberately separate

Two of them are **per viewer** (issue #949): they answer the solutions the
caller's organization installed and the caller's teams were granted, not the
deployment-wide registered set.

- `GET /api/solutions` is **authenticated** and returns the navigation
  projection — `{id, nav, available}` per entitled solution and nothing else. It is
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
forwards `/v1/*` and `/saas.accounts.v1.*` only), so no `ext_authz` stamp reaches
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
