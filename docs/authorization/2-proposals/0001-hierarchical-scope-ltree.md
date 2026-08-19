# RFC-0001 — Hierarchical scope via ltree + typed registry

- **Status:** Draft
- **Created:** 2026-08-19
- **Serves:** [hierarchical-access](../0-product/stories/hierarchical-access.md) H1–H4; behaviors B4–B6.
- **Relates to:** RFC-0002 (shares reuse the same resolver), gap analysis gaps 1, 5, 6.

## Context
`role_assignments.scope` is a flat string compared with equality — no hierarchy,
no precedence, no registry (gaps 1 & 6). Product needs "grant at a level, inherit
down" (B4) and most-specific-wins (B5), over a `foundation → solution → customer →
individual`-shaped tree. Must preserve the RLS floor (I1) and fail-closed (I2).

## Proposal (summary — full sketch in implementation)
- A **typed scope registry** (`scope_nodes`) storing a materialized path per node;
  parents validated on write (closes gap 6).
- **`scope_grants`**: principal/team + role at a scope node.
- **`CheckAccess`** resolves via an **ancestor match** on the path
  (`grant.path @> record.path`), most-specific-wins by depth — the flat-equality
  scope check becomes an ancestor check. Capability still resolves through
  `role_permissions` (I6). `CheckPermission` untouched (additive).
- Full schema + query + Go sketch:
  [`3-implementation/spikes/ltree-record-shares`](../3-implementation/spikes/ltree-record-shares.md).

## Alternatives considered
- **Adjacency list + recursive CTE** — recursion in the hot authz path; rejected
  for read-heavy auth-critical use.
- **Closure table** — powerful for DAGs/multi-parent; overkill for a strict tree.
- **External ReBAC PDP (SpiceDB/OpenFGA/WorkOS FGA)** — the eventual graduation
  target, but introduces two-sources-of-truth consistency cost now; deferred to
  the inflection point (see `9-reference/sota-research.md`).
- **Materialized path via `ltree`** — chosen: single indexed ancestor operator,
  single source of truth, composes with RLS.

## Consequences
- Adds hierarchy without touching flat RBAC. Enables per-record sharing (RFC-0002)
  to share the resolver.
- Path labels can't hold raw UUIDs (hyphens) — needs a label encoding decision.
- Optional RLS-side composition needs a new `app.current_principal_id` GUC.

## Open questions
1. Path label encoding (slug / hex / synthetic key).
2. Handler-side `CheckAccess` first vs. RLS-side policy (needs principal GUC).
3. Who edits the scope tree; team-scoped grants in v1?

## Decision
_Pending review._
