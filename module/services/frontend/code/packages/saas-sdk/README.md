# @codefly-dev/saas-sdk

The TypeScript SDK a saas-starter consumer imports to reach the saas public API
— the TS twin of the Go `saas-sdk`. Two layers:

1. **Generated Connect client** — `connect-es` clients generated from the public
   accounts/connect API contract (`AuditService`, `DatasourceService`,
   `WebhookService`).
2. **Generated gateway-bound facade** — `accounts.New(gw)` binds the generated
   clients to a transport (the gateway seam) and exposes one accessor per
   service, mirroring the Go facade's `accounts.New(gw).datasource().method(...)`.

```ts
import { accounts } from "@codefly-dev/saas-sdk";

// gw is a connect-es Transport pointed at the running gateway.
const { datasource: source } = await accounts.New(gw).datasource().addGitHubSource({
  orgId,
  repo: "acme/widgets",
});
```

`accounts.New(gw)` exposes `.audit()`, `.datasource()`, and `.webhook()`, each
returning the bound Connect client for that service. The generated service
descriptors (`AuditService`, `DatasourceService`, `WebhookService`) are
re-exported for consumers that build their own clients.

## The data graph

On top of the audit facade, the SDK ships **data-graph tooling** that turns a
metric declaration into a bound audit query and produces the `data={…}` a
`<Dashboard>` renders.

The declaration is the contract-first data graph — named audit **events**,
**metrics** computed over them, and **dashboards** that lay metrics out as
widgets. A source metric filters one event and compiles to an
`AggregateAuditLog` query; a derived metric combines other metrics
(`sum` / `ratio` / `difference`). This SDK is the runtime half: it executes that
declaration against the audit RPC.

> The declaration shapes mirror the schema owned by
> `@codefly/saas-plugin-manifest` (the `dashboard` manifest slot). They are
> re-declared in `src/schema.ts` so the SDK builds independently; once that
> schema lands they collapse to a re-export.

```ts
import {
  createSaasClient,
  defineDataGraph,
  runDashboard,
} from "@codefly-dev/saas-sdk";

const sdk = createSaasClient(transport);

const graph = defineDataGraph({
  events: [{ name: "signed_in", type: "user.signed_in.v1" }],
  metrics: [
    {
      id: "logins_over_time",
      kind: "source",
      filter: { event: "signed_in" },
      groupBy: "time",
      bucket: "day",
      aggregation: "count",
    },
    {
      id: "logins",
      kind: "source",
      filter: { event: "signed_in" },
      groupBy: "event_type",
      aggregation: "count",
    },
  ],
  dashboards: [
    {
      id: "activity",
      title: "Activity",
      layout: "grid",
      widgets: [
        { id: "trend", metric: "logins_over_time", visualization: "line" },
        { id: "total", metric: "logins", visualization: "number" },
      ],
    },
  ],
});

const data = await runDashboard(sdk.audit, graph, "activity", { orgId });
// data.byMetric.logins_over_time is typed by the declared metric ids.
```

`runDashboard` resolves every metric once (source metrics concurrently, derived
metrics after their inputs) and binds each widget to its metric's series.
`runDataGraph`, `runMetric`, and `compileMetric` are exported for finer-grained
use.

## Chat streaming

The `@codefly-dev/saas-sdk/chat` subpath ships `useChatStream`, the streaming twin
of `runDashboard`: it owns the SSE/WS transport and produces the `messages`/`onSend`
that `@codefly-dev/ui/chat`'s pure `<Chat>` renders. The subpath is split out so
the SDK's main entry stays React-free; `react` is an optional peer.

The hook takes a `ChatStreamSource` — any object that, given the conversation so
far, streams the assistant reply as content deltas. An SSE reader and a
WebSocket client both satisfy it structurally, exactly as `runDashboard` takes
any `AuditAggregateClient`:

```tsx
import { useChatStream } from "@codefly-dev/saas-sdk/chat";
import { Chat } from "@codefly-dev/ui/chat";

const source = {
  async *send(messages, { signal }) {
    const res = await fetch("/api/chat", {
      method: "POST",
      body: JSON.stringify({ messages }),
      signal,
    });
    for await (const delta of readSse(res.body)) yield { delta };
  },
};

function Assistant() {
  const { messages, send, isStreaming } = useChatStream(source);
  return <Chat messages={messages} onSend={send} busy={isStreaming} />;
}
```

## Scope

The metric model targets the audit RPC as it exists today: `group_by ∈
{event_type, category, actor, time}`, `bucket ∈ {day, week, month}`, and a
`count` aggregation. `count_distinct` and richer server-side aggregation track
the audit RPC extension; compiling a `count_distinct` metric throws until that
lands.

## Regenerating the client

The Connect client and facade under `generated/typescript` are generated from
the published accounts/connect API contract via codefly — the same contract
exported into the module package (`module/contracts/api`):

```bash
npm run generate   # codefly generate client, reproducible from the API contract
```

This runs `codefly generate client --from contracts:… --endpoint accounts/connect
--services AuditService,DatasourceService,WebhookService`, writing the bindings
(`generated/typescript/src/gen`), the `accounts` facade
(`generated/typescript/src/accounts_facade.ts`), and the resolved
`library.codefly.yaml` recording the contract digest.

The `--services` flag scopes only the generated **facade** to the three public
services. The accounts/connect contract is the full `saas.accounts.v1` package
descriptor, so the generator emits `_pb` bindings for the *entire* message graph
under `generated/typescript/src/gen` — well beyond those three services. The
published tarball must not carry that whole graph (it includes internal
admin/authz/mfa/sso message shapes), so the build restricts what ships: `build`
compiles from `src` only (see `tsconfig.json`), and tsc emits just the generated
files the facade and its three services actually reach. The `published-surface`
test asserts the shipped `dist` gen tree equals that reachable closure and never
contains an internal surface.

## Building and testing

```bash
npm run build
npm test
```

## Publishing and versioning

The package is versioned (`version` in `package.json`) and consumed today as an
**npm workspace** package — the frontend app and any in-repo solution import
`@codefly-dev/saas-sdk` directly. It is published to GitHub Packages on release
alongside `@codefly-dev/ui` (see `scripts/publish-frontend-kit.mjs`); as a
Module-Federation singleton, the bytes a solution installs from the registry are
the bytes the host serves.

A consumer outside this repo needs the org scope routed to GitHub Packages with
a read token before `npm i @codefly-dev/saas-sdk` can resolve:

```
@codefly-dev:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${GITHUB_PACKAGES_TOKEN}
```

The frontend app pins this package at an **exact** version in
`module/services/frontend/code/package.json`, and `npm ci` refuses to install if
the workspace version no longer satisfies that pin — it stops treating the
dependency as local and goes to the public registry, where this package's older
versions do not exist. (No version literal is quoted here on purpose: this
paragraph previously named one and went stale the moment the package was bumped,
which is exactly the failure it is warning about. `package.json` is the authority.)
So a `version` bump is not self-contained: bump it only together with the matching
pin bump in the app's `package.json`, the `@codefly-dev/saas-sdk` peer/dev ranges in
`packages/saas-ui/package.json`, and a regenerated `package-lock.json`, in the same
change — otherwise `npm ci` fails. The `workspaceLinkSatisfaction` half of
`module/tools/base-integrity.mjs` gates exactly this, so a missed pin fails in
seconds at PR time rather than several minutes into `npm ci`. Additive, backward-compatible surface changes therefore stay on the current
version until a release actually needs to move it.
