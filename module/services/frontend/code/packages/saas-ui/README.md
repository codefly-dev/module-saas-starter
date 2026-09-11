# @codefly-dev/saas-ui

Reusable SaaS-domain frontend components the portal **and** solutions import — so
there is one `<DatasourcesPanel>`, not a per-consumer copy. Components are built on
`@codefly-dev/ui` primitives, styled with the shared token classes so they render
identically in the host app and in a Module-Federation remote.

The components drive a `DatasourceClient` contract. There are two ways to bind it:

- **Gateway binding** (solution remotes) — pass
  `gateway={{ apiBase, getAccessToken, refreshAccessToken }}` and the panel self-wires:
  it builds a `@codefly-dev/saas-sdk` client over a scoped transport that stamps the host's
  bearer token on every request and, on an `Unauthenticated` response, calls
  `refreshAccessToken` and retries once — so a data call survives the short-lived token
  expiring mid-session. It also mounts its own `@tanstack/react-query` provider. A
  Module-Federation remote gets the full UI from `SolutionPageProps` alone — no ambient
  query/auth context, no re-implemented fetch.
- **Injected client** (the portal) — pass `client={…}` built with
  `datasourceClientOverTransport(transport)` over the app's own transport, and provide
  the surrounding `QueryClientProvider`. Use this when the app already owns
  auth/transport (e.g. its own token-refresh interceptors) — one adapter, shared with
  the gateway path.

## Datasources

- `<DatasourcesPanel gateway={{ apiBase, getAccessToken }} orgId={…} />` or
  `<DatasourcesPanel client={…} orgId={…} />` — lists an org's connected sources
  (repo · paths · branch · boundary · webhook · last sync) with per-row **Sync**/**Delete** and
  a **Connect GitHub** action. The last-sync cell shows whichever clock applies:
  `last_synced_at` for a provider that pulls, or `last ingest <date, time> ·
  <short commit>` for a github source, whose change sets the compiler enqueues
  (webhook delivery, periodic reconcile, or a tenant's "Sync now" forced
  reconcile) — it never sets `last_synced_at`, so "Never" is dropped rather than
  shown above live provenance. Loading/error/empty are first-class.
- `<ConnectGitHubForm onSubmit={…} … />` — the connect form (repo, paths, branch,
  target collection, access token, webhook secret).
- `createDatasourceClient({ apiBase, getAccessToken, refreshAccessToken })` — builds the
  gateway-bound `DatasourceClient` (with 401 refresh-and-retry) directly, for driving the
  hooks outside the panel. `datasourceClientOverTransport(transport)` does the same over a
  transport you already own.
- Hooks over a `DatasourceClient`: `useListSources`, `useAddGitHubSource`,
  `useSyncSource`, `useDeleteSource`, `useAccessibleScopes`.

### Data boundaries

Each source binds to a collection scope node. Connecting or creating a collection
creates **no creator, default-team, or organization-wide read grant**. Accounts
is the authority for `documents/read`; source labels and flat administrative
roles never substitute for a collection grant.

The host's `/admin/datasources` picker lists existing collections, their active
read grants (including inherited grants), and the creator's current read access.
Administrators can choose a member or team and grant a role containing only
`documents/read`, or revoke a displayed grant. Revoking an inherited grant removes
that role at its ancestor and all descendants; the confirmation names this impact.
Accounts checks admin authority and emits its transactional grant/revoke audit and
lifecycle events. The host audit page resolves actor names, and grant rows display
the granting actor. The host adapter supplies `listCollections`,
`listGrantSubjects`, `grantCollectionRead`, and `revokeCollectionRead` to the
transport-free `CollectionGrants` component. Gateway-bound source panels link to
that host action; admin permission APIs are not added to the public SDK.

`createDatasourceClient` queries the SDK's caller-scoped `ListMyAccessibleScopes`
with `documents/read`, follows every page, and propagates failures. No readable
collection and permission-service failure have separate states; neither is an
empty search result or proof of an indexing failure.

A consuming collection or chat page can wrap **all** private state beneath
`CollectionReadBoundary` (inside its React Query provider), passing `client`,
`orgId`, and the collection's `nodeId`. It unmounts its children on denied or
failed permission refreshes. Permission queries refresh every five seconds,
including in background tabs, and on focus; local grant mutations invalidate
them immediately in the same query client. Server-side document/search/stream
requests must still enforce current Accounts grants. Browser timers can be
throttled, so this polling is not an instantaneous revocation guarantee.

The consuming page owns cancellation and deletion of any private query caches,
stream buffers, persisted history, or state outside the boundary. Dispose those
on unmount and reauthorize before restoring content. Do not keep private content
in an ancestor of the boundary. Render "No indexed content" only inside an
allowed boundary after a successful empty content response. This repository has
no consuming collection or chat screen; its wrapper tests exercise state disposal,
while end-to-end ingestion and those screens require verification in a consuming
solution.

The `dev-admin` fixture provisions a `Wiki` collection and a `Collection reader`
role via Accounts' registration/grant service paths. Only `admin@acme.com` and
`bob@acme.com` receive that grant. Select this existing collection when connecting
a demo source; other fixture viewers remain ungranted. Reseeding reconverges the
declared grants, so test revocation without restarting the fixture runtime.

## Installing from a solution

This package is published to GitHub Packages under the org's `@codefly-dev`
scope, so a consumer needs that scope routed to the GitHub registry with a read
token:

```
@codefly-dev:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${GITHUB_PACKAGES_TOKEN}
```

Everything this package renders with is a **peer**, not a bundled dependency, so
that the host and every Module-Federation remote share one instance of each (a
second copy of React Query or React Hook Form means a second context, which is
the split the sealing invariant exists to prevent). That means the consumer
installs them:

```
npm i @codefly-dev/saas-ui @codefly-dev/saas-sdk \
      react react-hook-form @hookform/resolvers zod \
      @tanstack/react-query @connectrpc/connect @connectrpc/connect-web @bufbuild/protobuf
```

Omitting one does not fail the install — npm only warns about unmet peers — it
fails later at import. (`peer-docs.test.ts` pins this list to `peerDependencies`,
so it cannot drift the way a hand-maintained list otherwise would.) `@codefly-dev/saas-sdk` is deliberately a range rather
than an exact pin: it versions independently of this package, so an exact pin
here would make an SDK patch bump uninstallable against the published saas-ui.

## Styling

Components are styled with Tailwind utility classes against the shared shadcn
design tokens (`bg-primary`, `text-muted-foreground`, `border-input`, …), same as
`@codefly-dev/ui`. They ship no compiled CSS, so **the consuming app's Tailwind must
scan this package's source** or the utilities used only here (e.g. the modal's
`bg-black/50` overlay) won't be generated and the components render unstyled.

- In this monorepo the portal gets this for free: Tailwind v4 automatic content
  detection already scans `packages/**`.
- An external consumer that installs the built package from `node_modules` (which
  Tailwind v4 excludes by default) must opt it in, e.g.
  `@source "../node_modules/@codefly-dev/saas-ui/dist";` in its CSS.
