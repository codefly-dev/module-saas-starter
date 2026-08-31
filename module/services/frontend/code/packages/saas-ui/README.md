# @codefly/saas-ui

Reusable SaaS-domain frontend components the portal **and** solutions import — so
there is one `<DatasourcesPanel>`, not a per-consumer copy. Components are built on
`@codefly/ui` primitives, styled with the shared token classes so they render
identically in the host app and in a Module-Federation remote.

The components drive a `DatasourceClient` contract. There are two ways to bind it:

- **Gateway binding** (solution remotes) — pass `gateway={{ apiBase, getAccessToken }}`
  and the panel self-wires: it builds a `@codefly/saas-sdk` client over a scoped
  transport that stamps the host's bearer token on every request, and mounts its own
  `@tanstack/react-query` provider. A Module-Federation remote gets the full UI from
  `SolutionPageProps` alone — no ambient query/auth context, no re-implemented fetch.
- **Injected client** (the portal) — pass `client={…}`, an adapter over the app's own
  transport, and provide the surrounding `QueryClientProvider`. Use this when the app
  already owns auth/transport (e.g. token-refresh interceptors).

## Datasources

- `<DatasourcesPanel gateway={{ apiBase, getAccessToken }} orgId={…} />` or
  `<DatasourcesPanel client={…} orgId={…} />` — lists an org's connected sources
  (repo · paths · branch · webhook · last sync) with per-row **Sync**/**Delete** and
  a **Connect GitHub** action. Loading/error/empty are first-class.
- `<ConnectGitHubForm onSubmit={…} … />` — the connect form (repo, paths, branch,
  target collection, access token, webhook secret).
- `createDatasourceClient({ apiBase, getAccessToken })` — builds the gateway-bound
  `DatasourceClient` directly, for driving the hooks outside the panel.
- Hooks over a `DatasourceClient`: `useListSources`, `useAddGitHubSource`,
  `useSyncSource`, `useDeleteSource`.

## Styling

Components are styled with Tailwind utility classes against the shared shadcn
design tokens (`bg-primary`, `text-muted-foreground`, `border-input`, …), same as
`@codefly/ui`. They ship no compiled CSS, so **the consuming app's Tailwind must
scan this package's source** or the utilities used only here (e.g. the modal's
`bg-black/50` overlay) won't be generated and the components render unstyled.

- In this monorepo the portal gets this for free: Tailwind v4 automatic content
  detection already scans `packages/**`.
- An external consumer that installs the built package from `node_modules` (which
  Tailwind v4 excludes by default) must opt it in, e.g.
  `@source "../node_modules/@codefly/saas-ui/dist";` in its CSS.
