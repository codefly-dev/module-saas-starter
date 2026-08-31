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

## Next: productization (epic [#369])

#321 delivered the *primitives* (spec, validation, authoring API, renderer, a
stub driver). Turning them into a product — a user who **defines, saves, and
shares** dashboards, at runtime — is epic [#369]. A current-state audit found the
primitives are **wired to no UI**, persistence is **localStorage-only**, and there
is **no user-dashboard model, no dashboard authz, and no external-driver channel**.

Two **design decisions come first** and gate the build:

- [#375] — **Storage & ownership model.** A dashboard is *configuration* (the
  definition — shareable, org-wide) vs *preferences* (per-user view state — never
  shared). Scopes: solution/template · org · user · user-prefs-on-any. Lean:
  definitions in a dedicated store, preferences in `user_settings`.
- [#382] — **Execution authority (invoker vs definer).** Default runs under the
  *viewer's* authority; an opt-in *definer's-rights* dashboard runs under its
  *author's* (or a bound service principal's) authority so a restricted viewer sees
  owner-computed aggregates — **new** to the platform, gated by an **aggregate-only**
  guardrail (only `AggregateAuditLog`, bounded group-by cardinality). The execution
  half of the object-authz in #367.

Then the build children: #364 (wire the loop into a Dashboards surface) · #365
(server-side persistence) · #366 (user-owned model + CRUD) · #367 (object authz) ·
#368 (external-driver channel). See
[DASHBOARD_AUTHORING_DESIGN.md](./DASHBOARD_AUTHORING_DESIGN.md) §4.

## Boundary

Persistence is `localStorage` **today**; the durable home is decided in [#375]
(a dedicated store vs settings-composed), not assumed. The *authoring
intelligence* (natural language → spec) is **not** in this module — it knows
nothing about it beyond the tool contract (#320) and the external-driver channel
(#368). The chat/NL agent already exists as a self-contained prototype in
`obin-ai/module-robin` (epic #16, complete); the goal is **convergence** — robin's
chat drives *this* host's real dashboards through #368's channel, retiring robin's
private store — and that end-to-end roadmap lives in **lodestar** (obin-ai), not
here. Dependency direction stays obin→codefly.

[#289]: https://github.com/codefly-dev/module-saas-starter/issues/289
[#317]: https://github.com/codefly-dev/module-saas-starter/issues/317
[#318]: https://github.com/codefly-dev/module-saas-starter/issues/318
[#319]: https://github.com/codefly-dev/module-saas-starter/issues/319
[#320]: https://github.com/codefly-dev/module-saas-starter/issues/320
[#321]: https://github.com/codefly-dev/module-saas-starter/issues/321
[#364]: https://github.com/codefly-dev/module-saas-starter/issues/364
[#365]: https://github.com/codefly-dev/module-saas-starter/issues/365
[#366]: https://github.com/codefly-dev/module-saas-starter/issues/366
[#367]: https://github.com/codefly-dev/module-saas-starter/issues/367
[#368]: https://github.com/codefly-dev/module-saas-starter/issues/368
[#369]: https://github.com/codefly-dev/module-saas-starter/issues/369
[#375]: https://github.com/codefly-dev/module-saas-starter/issues/375
[#382]: https://github.com/codefly-dev/module-saas-starter/issues/382
