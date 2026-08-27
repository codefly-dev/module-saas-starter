# @codefly/saas-sdk

The TypeScript SDK a saas-starter consumer imports to build dashboards: a typed
Connect client for the accounts audit surface plus the **data-graph tooling**
that turns a metric declaration into a bound audit query and produces the
`data={…}` a `<Dashboard>` renders.

## The data graph

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

The Connect client under `src/gen` is generated from the accounts audit proto:

```bash
npm run generate   # buf generate, audit.proto + its transitive imports only
```

## Building and testing

```bash
npm run build
npm test
```
