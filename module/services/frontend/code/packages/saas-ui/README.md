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
  (repo · paths · branch · boundary · webhook · last sync) with per-row
  **Sync**/**Delete** and a **Connect GitHub** action. Loading/error/empty are
  first-class.
- `<ConnectGitHubForm onSubmit={…} … />` — the connect form (repo, paths, branch,
  target collection, access token, webhook secret).
- `createDatasourceClient({ apiBase, getAccessToken, refreshAccessToken })` — builds the
  gateway-bound `DatasourceClient` (with 401 refresh-and-retry) directly, for driving the
  hooks outside the panel. `datasourceClientOverTransport(transport)` does the same over a
  transport you already own.
- Hooks over a `DatasourceClient`: `useListSources`, `useAddGitHubSource`,
  `useSyncSource`, `useDeleteSource`, `useAccessibleScopes`.

### Data boundaries

Each source ingests into a **data boundary** — the scope node its Entries land in
— so two teams in one org can see different collections through a grant change
rather than a code change. The **Boundary** column names that node and summarizes
the viewer's grants on it (`Read · Write`).

Naming a boundary needs the caller-scoped accessible-scopes RPC, which is *not*
part of the published `@codefly-dev/saas-sdk` surface (the SDK deliberately
excludes the authorization surface), so `DatasourceClient.listAccessibleScopes`
is **optional** and supplied by the consumer — see the portal's
`src/features/datasources/datasource-client.ts`. Without it the column falls back
to the boundary id and never claims anything about access, because a boundary
that could not be looked up is unknown, not ungranted.

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
