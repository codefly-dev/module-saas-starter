# Unified plugin manifest schema (`plugin.codefly.yaml`)

Status: schema defined (`P3-PLUGIN-002`)
Package: `services/frontend/code/packages/saas-plugin-manifest`
Canonical schema: `plugin.codefly.schema.json`

`plugin.codefly.yaml` is the single manifest for an installable Codefly plugin.
It spans the whole plugin — backend and frontend — so a registry
(`P3-RUNTIME-001+`) can read one file to know everything it must register,
gate, and verify. Contract v2 (`P3-PLUGIN-001`) modeled only the frontend; this
manifest is the superset that contains it.

## Why one manifest, aligned with the consuming platform

A consuming v2 platform models the same facts as its `SolutionSpec`: identity,
`services`, `api` exposes/consumes, `events`, `ui` extensions, `needs`,
`permissions`, `lifecycle`. Two manifests describing the same plugin would drift
the moment either side adds a field. Rather than fork, the starter manifest is a
**superset** of `SolutionSpec` with a total, side-effect-free projection
(`toSolutionSpec`) that maps the shared sections one-to-one and carries the
starter-only sections through a documented `extensions['x-codefly']` namespace.

The `ui` block is not re-modeled here: it is exactly the frontend contract's
presentation facts (`@codefly-dev/saas-plugin-contract`) and is validated by that
package, so the frontend plugin contract and the unified manifest cannot fork
either.

## Sections

Every section is optional except `apiVersion`, `kind`, and `metadata`. The
manifest is JSON-safe: it declares stable ids and compatibility metadata only —
never deployment addresses, credentials, or resolved bindings.

| Section | Purpose | Identifier rule |
| --- | --- | --- |
| `metadata` | Identity: name, semantic version, publisher. | logical id, semver |
| `services` | Backend services and endpoint protocols. | logical id |
| `api.exposes` | Contracts this plugin provides. | logical id + major |
| `api.consumes` | Contracts this plugin depends on. | logical id + major |
| `events.publishes` | Domain events emitted. | `name.space.vN` |
| `events.subscribes` | Domain events consumed, with handler. | `name.space.vN` |
| `ui` | Frontend navigation, routes, widgets, BFF services. | frontend contract |
| `dashboard` | Data graph: audit events, metrics, dashboards. | logical id |
| `needs` | Platform capabilities required to run. | namespaced id |
| `permissions` | Permissions defined and enforced. | `resource:action` |
| `entitlements` | Plan/grant gates on features. | namespaced id |
| `config` | Typed configuration keys. | upper-case env key |
| `migrations` | Ordered, scoped database migrations. | `0001_name` |
| `egress` | Allowed outbound destinations. | hostname |
| `lifecycle` | Install/upgrade/uninstall steps. | logical job id |
| `integrity` | Detached signature and pinned artifact hashes. | sha256 hex |

Duplicate ids **within** a manifest — service names, contracts, event handlers,
permission ids, config keys, migration ids, egress hosts, artifact paths — fail
validation. Cross-plugin duplicate detection across an installed set is a
separate concern (`P3-PLUGIN-005`) and is not performed here.

## Projection onto `SolutionSpec`

`toSolutionSpec(manifest)` produces a `SolutionSpec`:

| `plugin.codefly.yaml` | `SolutionSpec` |
| --- | --- |
| `metadata` | `metadata` |
| `services` | `services` |
| `api.exposes` / `api.consumes` | `api.exposes` / `api.consumes` |
| `events` | `events` |
| `ui` | `ui` |
| `needs` | `needs` |
| `permissions` | `permissions` |
| `lifecycle` | `lifecycle` |
| `dashboard` | `extensions['x-codefly'].dashboard` |
| `entitlements` | `extensions['x-codefly'].entitlements` |
| `config` | `extensions['x-codefly'].config` |
| `migrations` | `extensions['x-codefly'].migrations` |
| `egress` | `extensions['x-codefly'].egress` |
| `integrity` | `extensions['x-codefly'].integrity` |

The six starter-only sections are the deliberate convergence points: a consumer can
adopt any of them into `SolutionSpec` proper, at which point the projection
moves that section from `extensions` to a first-class field with no change to
`plugin.codefly.yaml` authors.

## The `dashboard` data graph

`dashboard` declares a data graph over the audit RPC, so a consumer adds a
dashboard in a few lines and drops `<Layout><Dashboard data={…}/></Layout>`. It
has three node kinds:

- **`events`** — a named audit event bound to an audit `event_type` (e.g.
  `guardrail.triggered.v1`). Metrics reference it by `name`.
- **`metrics`** — a `source` metric filters one event and compiles to an
  `AuditService.AggregateAuditLog` query (its `groupBy`/`bucket`/`aggregation`
  are exactly that RPC's dimensions); a `derived` metric combines other metrics
  (`sum`, `ratio`, `difference`).
- **`dashboards`** — a `layout` of `widgets`, each binding a `visualization` to
  one metric. These metric-bound widgets are distinct from `ui.widgets`, which
  are presentation slots contributed to host surfaces; the two never mix.

### Dashboard layout

Layout fields are **additive**. Existing declarations need no edits. A grid
packs consecutive `number` widgets into four-column rows and series into
two-column rows, collapsing to one column on narrow screens. Runs retain widget
order; a scalar is never moved ahead of a preceding chart. Cards align at their
natural height, so a short or empty card does not stretch to its neighbour.
`stack` keeps one widget per row and ignores column/span hints.

- `Dashboard.columns?: 1 | 2 | 3 | 4` overrides the default for both kinds of row.
- `MetricWidget.span?: 1 | 2 | 3 | 4` applies to scalars and series, clamped to
  the containing row's columns and the current responsive breakpoint.
- `Dashboard.sections?: { id, title, description?, columns? }[]` declares named
  bands in section order. Each widget may name one with `section`; the id must
  exist. A section inherits the dashboard column count unless it overrides it.
  Widgets without a section appear first; within each section, widget order is
  preserved. Empty sections draw nothing.

For example, an existing dashboard can add:

```json
{
  "id": "overview",
  "layout": "grid",
  "columns": 2,
  "sections": [
    { "id": "headline", "title": "Headline numbers", "columns": 4 },
    { "id": "trends", "title": "Over time" }
  ],
  "widgets": [
    { "id": "total", "metric": "total_events", "visualization": "number", "section": "headline" },
    { "id": "trend", "metric": "events_per_day", "visualization": "line", "section": "trends", "span": 2 }
  ]
}
```

`runDashboard` and `fromDashboardData` carry these fields to the kit unchanged.
The host's sortable view uses the same geometry. Dragging swaps widget IDs;
keyboard arrows move one place within the widget's declared section. A viewer's
preference does not change declared section membership.

**Independent rollout:** hosts with this validator ignore unknown presentation
fields on dashboards, sections and widgets. Unknown metric/query fields still
fail validation. Previously released hosts used exact-key validation and will
reject the new fields: upgrade those hosts before adding layout fields, or keep
sending the unchanged declaration to them. Additive types cannot retrofit an
already-deployed validator.

On a registered solution's page the host renders every declared dashboard in a
**Dashboard** tab beside the solution's own **App** tab, never stacked above
the solution; the solution stays mounted while the dashboard is open. A
solution that declares no dashboard gets no tab bar. Each viewer can arrange a
dashboard for themselves: reorder its declared widgets, remove
them, and re-add the ones they removed. The declared widgets, in declared order,
are where every viewer starts, and a widget declared later reaches viewers who
already rearranged. Saved layouts use widget IDs, not positions, and record the
IDs known at save time. An unchanged saved default follows a new declaration;
a customized order keeps surviving IDs, appends newly declared IDs once, and
keeps intentionally removed widgets hidden. A viewer's arrangement is a preference (ADR 0007): it never
adds a metric the dashboard does not draw, so a metric declared only as a
derived metric's input stays off the page.

The schema owns the per-node field formats — including the two shape rules that
cross fields: a metric carries a `bucket` exactly when it groups by `time`, and
a `ratio`/`difference` takes exactly two inputs (`sum` takes two or more). The
host validator adds only what a JSON Schema cannot express: referential
integrity — every metric filter names a declared event, every derived-metric
input and every widget names a declared metric — and that the derived-metric
reference graph is acyclic.

What neither layer checks is **dimensional coherence** — whether a metric's
`groupBy` is meaningful for its data, or whether a derived metric's inputs have
compatible shapes. That is deliberately the compiler's job, not the schema's,
and the schema stays permissive on purpose: legitimate combinations must not be
rejected here. A `ratio` of a per-day series and a scalar total (`groupBy:
event_type`, which yields a single bucket) is a valid rate; a rule that forced
matching `groupBy`/`bucket` across inputs would wrongly reject it. The compiler
that lowers a metric to an `AggregateAuditLog` query is the single place that
knows each metric's resolved shape, so it owns coherence.

This section gates the SDK that compiles metrics to audit queries and the
`<Dashboard>` component that renders them.

## Scope

This is `P3-PLUGIN-002` — the manifest and its schema. Generating
frontend/backend/gateway/networking registration (`P3-PLUGIN-004`), cross-plugin
duplicate detection (`P3-PLUGIN-005`), and driving the admin shell from the
registry (`P3-PLUGIN-006`) build on this schema and are tracked separately.
