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
