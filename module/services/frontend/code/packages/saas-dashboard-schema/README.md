# @codefly/saas-dashboard-schema

The data-graph spine of the Template Dashboard capability: React-free types for
declaring **events → metrics → dashboards**, and the compiler that turns a
declarative metric into an audit `AggregateAuditLog` query.

A **metric is a producer over event data**. A source metric compiles to one
audit query; a derived metric is a pure reducer over the resolved series of
other metrics. A dashboard binds metrics to widgets. `defineDataGraph`
validates the whole declaration up front — dangling references, duplicate ids,
cycles, a derived metric missing its reducer, and malformed time buckets fail at
authoring time, not at render.

Events, source metrics, and dashboards are plain data and JSON-serializable; a
derived metric additionally carries a `compute` reducer, which is code and does
not serialize.

```ts
import { defineDataGraph } from "@codefly/saas-dashboard-schema";

const graph = defineDataGraph({
  events: [{ name: "user.login", category: "auth" }],
  metrics: [
    {
      id: "logins-over-time",
      kind: "source",
      filter: { event: "user.login" },
      groupBy: "time",
      bucket: "day",
    },
    {
      id: "total-logins",
      kind: "derived",
      from: ["logins-over-time"],
      compute: ([series]) => [
        { key: "total", value: series.reduce((sum, p) => sum + p.value, 0) },
      ],
    },
  ],
  dashboards: [
    {
      id: "activity",
      title: "Activity",
      widgets: [
        { id: "w1", metric: "logins-over-time", type: "line" },
        { id: "w2", metric: "total-logins", type: "stat" },
      ],
    },
  ],
});

// Compile a source metric to an audit query, or resolve any metric to its
// series by handing source queries to a fetcher (e.g. the audit RPC client).
graph.compile("logins-over-time");
const series = await graph.resolve("total-logins", (query) =>
  auditClient.aggregate(query),
);
```

Metrics aggregate by COUNT — the only reduction the audit RPC supports today.
#280 lifts that ceiling; when it lands it adds an aggregation field to
`SourceMetric`, wired through the compiled query at the same time so a declared
aggregation can never be silently dropped.

This package is the spine of epic #289. It deliberately holds no rendering
code: the `@codefly/ui` kit (#286) and the `<Dashboard>` component (#287)
consume the graph a consumer declares here.
