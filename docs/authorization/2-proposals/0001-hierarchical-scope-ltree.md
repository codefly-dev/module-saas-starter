# RFC-0001 — Hierarchical scope via ltree + typed registry

- **Status:** Review (#177) — proposed decisions below, pending sign-off; draft ADR [0002](../9-reference/decisions/0002-hierarchical-scope.md)
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
- Path labels are the node UUID with hyphens stripped (resolved below); raw UUIDs
  can't be labels.
- Optional RLS-side composition needs a new `app.current_principal_id` GUC.

## Proposed resolutions (#177 — to review)
1. **Path label encoding → hyphen-stripped node UUID hex.** `scope_nodes.id` stays a
   real UUID; its `scope_path` **label** is that UUID with hyphens removed — 32 chars
   of `[0-9a-f]`, a universally valid `ltree` label on every supported Postgres. The
   human-readable name lives in `label`, so the path never needs to be readable and
   never has to be re-encoded on rename. Rejected: **slugs** (rename/collision breaks
   materialized paths) and **raw UUIDs** (hyphens are not valid labels). Tradeoff:
   longer paths, acceptable at shallow depth; a compact per-node synthetic key is a
   valid later optimization if paths grow.
2. **Handler-side `CheckAccess` first; no RLS-side composition and no new principal
   GUC in v1.** The subject is a query parameter, so no `app.current_principal_id` is
   needed (verified: only `app.current_org_id` / `app.current_user_id` exist today).
   RLS-side composition is deferred to if/when it's measured to be worth it.
3. **Who edits the tree → Owner + branch-delegated Admins** (an `admin` grant at a
   node lets its holder create children and grant within that subtree, bounded by
   their own authority). **Team-scoped grants are in v1** (`scope_grants.subject_kind
   = 'team'`); a human inherits via literal membership (no sub-team cascade — see
   [teams](../0-product/stories/teams.md) TEAM-3).

## Decision
**Proposed — pending review (#177).** The recommendation: build `scope_nodes` (typed
ltree registry) + `scope_grants` + `CheckAccess` ancestor-match, most-specific-wins,
strictly additive above the RLS floor (I1). Encoding and edit-authority as proposed
above. The record's scope is resolved *from* its id, never a caller field — see the
spike's load-bearing proposal
([open question 2](../3-implementation/spikes/ltree-record-shares.md)). On sign-off,
this becomes Accepted and draft ADR-[0002](../9-reference/decisions/0002-hierarchical-scope.md)
is finalized; phased at roadmap P1.
