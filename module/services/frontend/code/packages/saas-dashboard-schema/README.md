# @codefly/saas-dashboard-schema

The data-graph spine of the Template Dashboard capability: React-free,
serializable types for declaring **events → metrics → dashboards**, and the
compiler that turns a declarative metric into an audit `AggregateAuditLog`
query.

A **metric is a producer over event data**. A source metric compiles to one
audit query; a derived metric is a pure reducer over the resolved series of
other metrics. A dashboard binds metrics to widgets. `defineDataGraph`
validates the whole declaration up front — dangling references, duplicate ids,
cycles, and malformed time buckets fail at authoring time, not at render.

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

`aggregation` is `"count"` only today — the audit RPC is COUNT-only (#280 lifts
that ceiling). The union is kept open so a graph can be authored ahead of the
RPC.

This package is the spine of epic #289. It deliberately holds no rendering
code: the `@codefly/ui` kit (#286) and the `<Dashboard>` component (#287)
consume the graph a consumer declares here.
