# Dynamic dashboards — epic overview

The **Dynamic Dashboards** epic ([#321]) makes an audit-driven dashboard
*editable at runtime* — by a user, and (the real goal) by conversation, via an
external driver that composes this module. The [Template Dashboard epic][#289]
made a dashboard *a few lines of code*; this epic makes it *a piece of data you
can mutate live in the browser*.

The work is cheap because the dashboard was already pure data end-to-end:
`DashboardDef`/`MetricDef` are JSON-safe, `<Dashboard data={…}>` re-renders on
any new object, and the backend `AuditService.AggregateAuditLog` engine already
supports far more than the app DSL exposed. The epic *surfaces* and *persists*
what was already there — no new server endpoint.

All code lives under
`module/services/frontend/code/src/features/dashboard/` (plus the `@codefly/saas-sdk`
and `@codefly/saas-plugin-manifest` packages for the graph-level widening).

## Workstreams

| # | Workstream | Delivered by |
|---|------------|--------------|
| [#317] | Serializable, versioned dashboard spec + local draft store (`model/validate.ts` throwing `assertDashboardSpec`/`parseDashboardSpec`, `service/use-dashboard-draft.ts` — validate-on-load/on-set, cross-tab sync, persist-failure surfaced) | PR #328 — **merged** |
| [#318] | Close the DSL↔RPC gap (payload fields, `count_distinct`, percentile, numeric ops over a field, multi-dim group-by, derived ratios); lift the SDK compiler ceiling — no Go change | PR #326 |
| [#319] | Layout & style in the spec (`LayoutDef` grid/stack + columns, per-widget `span`, `ThemeDef` accent read by `<Dashboard>`) | PR #327 — **merged** |
| [#320] | Dashboard authoring API — `listEventTypes` / `previewMetric` / `setDashboard`, guard-railed by validation and read-only against audit | PR #325 — **merged** |

The authoring API's three follow-up decisions (ownership, result contract,
caching) are recorded in [DASHBOARD_AUTHORING_DESIGN.md](./DASHBOARD_AUTHORING_DESIGN.md).

## Boundary

Persistence is `localStorage` for now; the eventual home is the settings
service (`@codefly/saas-settings`). The *authoring intelligence* (natural
language → spec) is **not** in this module — this module knows nothing about it
beyond the tool contract in #320. This epic delivers the canvas plus the
guard-railed authoring seam; a conversational brain is delivered separately by
whatever composes the module.

[#289]: https://github.com/codefly-dev/module-saas-starter/issues/289
[#317]: https://github.com/codefly-dev/module-saas-starter/issues/317
[#318]: https://github.com/codefly-dev/module-saas-starter/issues/318
[#319]: https://github.com/codefly-dev/module-saas-starter/issues/319
[#320]: https://github.com/codefly-dev/module-saas-starter/issues/320
[#321]: https://github.com/codefly-dev/module-saas-starter/issues/321
