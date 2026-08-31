# @codefly/saas-ui

Reusable SaaS-domain frontend components the portal **and** solutions import — so
there is one `<DatasourcesPanel>`, not a per-consumer copy. Components are built on
`@codefly/ui` primitives, styled with the shared token classes so they render
identically in the host app and in a Module-Federation remote.

The components own no transport. They drive an injected `DatasourceClient` — the
consumer adapts its generated `DatasourceService` Connect client (or, once it lands,
`@codefly/saas-sdk`) to that contract. This keeps the package free of generated
protobuf and lets any app wire its own auth/transport.

## Datasources

- `<DatasourcesPanel client={…} orgId={…} />` — lists an org's connected sources
  (repo · paths · branch · webhook · last sync) with per-row **Sync**/**Delete** and
  a **Connect GitHub** action. Loading/error/empty are first-class.
- `<ConnectGitHubForm onSubmit={…} … />` — the connect form (repo, paths, branch,
  target collection, access token, webhook secret).
- Hooks over the injected client: `useListSources`, `useAddGitHubSource`,
  `useSyncSource`, `useDeleteSource`.

The consumer provides a `@tanstack/react-query` `QueryClientProvider`.

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
