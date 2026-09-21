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

The route **relays the registry's own answer**: `409` for a revision conflict,
`403` when the id belongs to another publisher, `503` when the registry cannot be
reached. A registrant is never told it is serving when it is not. Re-registering
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
  `id`, its title, and the declared `surfaces` whose `client` matches. The kind
  is required — an absent one is `400`, never the unfiltered set — and an
  unreadable registry is again `503`. The host does not enumerate client kinds:
  which ones exist is deployment configuration (the registered-client registry),
  so a new kind needs no host release.
- Everything else a manifest carries (`frontend`, `backend`) is deployment
  topology, served instead by `GET /api/internal/solutions`
  (`src/app/api/internal/solutions/route.ts`), gated on the cluster-internal
  token.
- The dashboard graph is on none of them: the solution page reads it in-process
  through `findSolution`.

## Loading a remote, and its CSP

`/s/[solutionId]` (`src/app/(dashboard)/s/[solutionId]/page.tsx`) loads the
remote via `SolutionOutlet` from the registered `manifestUrl` + `exposedModule`,
read in-process from the registry.

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
