# @codefly/saas-sdk

The TypeScript SDK a saas-starter consumer imports to reach the saas public API
— the TS twin of the Go `saas-sdk`. Two layers:

1. **Generated Connect client** — `connect-es` clients generated from the public
   accounts proto (`AuditService`, `DatasourceService`, `WebhookService`).
2. **Gateway-bound facade** — a thin `svc.New(gw)` per service that binds the
   generated client to a transport (the gateway seam), mirroring the Go facade's
   `svc.New(gw).method(...)`.

```ts
import { datasource } from "@codefly/saas-sdk";

// gw is a connect-es Transport pointed at the running gateway.
const { datasource: source } = await datasource.New(gw).addGitHubSource({
  orgId,
  repo: "acme/widgets",
});
```

`audit`, `datasource`, and `webhooks` each export `New(gw)`. The generated
service descriptors (`AuditService`, `DatasourceService`, `WebhookService`) are
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
} from "@codefly/saas-sdk";

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

## Scope

The metric model targets the audit RPC as it exists today: `group_by ∈
{event_type, category, actor, time}`, `bucket ∈ {day, week, month}`, and a
`count` aggregation. `count_distinct` and richer server-side aggregation track
the audit RPC extension; compiling a `count_distinct` metric throws until that
lands.

## Regenerating the client

The Connect client under `src/gen` is generated from the public accounts proto —
`audit`, `datasource`, and `webhooks`, plus their transitive imports:

```bash
npm run generate   # buf generate, reproducible from the accounts proto
```

## Building and testing

```bash
npm run build
npm test
```

## Publishing and versioning

The package is versioned (`version` in `package.json`) and consumed today as an
**npm workspace** package — the frontend app and any in-repo solution import
`@codefly/saas-sdk` directly. It is npm-ready (`publishConfig.access: public`,
`files: ["dist"]`); publishing to a public registry is gated on registry
credentials and is not wired here. Until that lands, treat this workspace package
as the codefly-internal library.

The frontend app pins this package at an **exact** version
(`"@codefly/saas-sdk": "0.1.0"` in `module/services/frontend/code/package.json`),
and `npm ci` refuses to install if the workspace version no longer satisfies that
pin. So a `version` bump is not self-contained: bump it only together with the
matching pin bump in the app's `package.json` and a regenerated `package-lock.json`,
in the same change — otherwise `npm ci` (and the workspace-install-graph CI gate)
fails. Additive, backward-compatible surface changes therefore stay on the current
version until a release actually needs to move it.
