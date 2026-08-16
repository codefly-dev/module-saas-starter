# Unified plugin manifest schema (`plugin.codefly.yaml`)

Status: schema defined (`P3-PLUGIN-002`)
Package: `services/frontend/code/packages/saas-plugin-manifest`
Canonical schema: `plugin.codefly.schema.json`

`plugin.codefly.yaml` is the single manifest for an installable Codefly plugin.
It spans the whole plugin — backend and frontend — so a registry
(`P3-RUNTIME-001+`) can read one file to know everything it must register,
gate, and verify. Contract v2 (`P3-PLUGIN-001`) modeled only the frontend; this
manifest is the superset that contains it.

## Why one manifest, aligned with lodestar

obin's v2 platform models the same facts as `SolutionSpec` (lodestar
`docs/design/solution-anatomy.md`, obin-ai/lodestar#13, #15): identity,
`services`, `api` exposes/consumes, `events`, `ui` extensions, `needs`,
`permissions`, `lifecycle`. Two manifests describing the same plugin would drift
the moment either side adds a field. Rather than fork, the starter manifest is a
**superset** of `SolutionSpec` with a total, side-effect-free projection
(`toSolutionSpec`) that maps the shared sections one-to-one and carries the
starter-only sections through a documented `extensions['x-codefly']` namespace.

The `ui` block is not re-modeled here: it is exactly the frontend contract's
presentation facts (`@codefly/saas-plugin-contract`) and is validated by that
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
| `entitlements` | `extensions['x-codefly'].entitlements` |
| `config` | `extensions['x-codefly'].config` |
| `migrations` | `extensions['x-codefly'].migrations` |
| `egress` | `extensions['x-codefly'].egress` |
| `integrity` | `extensions['x-codefly'].integrity` |

The five starter-only sections are the deliberate convergence points: obin can
adopt any of them into `SolutionSpec` proper, at which point the projection
moves that section from `extensions` to a first-class field with no change to
`plugin.codefly.yaml` authors.

## Scope

This is `P3-PLUGIN-002` — the manifest and its schema. Generating
frontend/backend/gateway/networking registration (`P3-PLUGIN-004`), cross-plugin
duplicate detection (`P3-PLUGIN-005`), and driving the admin shell from the
registry (`P3-PLUGIN-006`) build on this schema and are tracked separately.
