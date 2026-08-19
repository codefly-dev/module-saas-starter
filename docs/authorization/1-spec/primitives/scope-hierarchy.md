# Primitive — scope hierarchy

**What it is.** A per-tenant tree of **scope nodes**, each identified by a
materialized **scope path** from the root (e.g. `foundation.solution_x.customer_7`).
A **scope grant** binds a principal/team + role to a node; the grant inherits to
the node's entire subtree.

**Contract**
- Granting at an ancestor implies access to all descendants (B4).
- Resolution is most-specific-wins (B5): the deepest matching grant governs.
- A scope must be a **registered node** (typed registry) — granting on an
  unregistered scope fails (B?/H4), which closes the untyped-scope gap.
- Sits above the RLS floor (I1); tenant isolation is never expressed through
  scope.

**Serves stories:** [hierarchical-access](../../0-product/stories/hierarchical-access.md) H1–H4.
**Realized by:** RFC-0001 → implementation spike
[`ltree-record-shares`](../../3-implementation/spikes/ltree-record-shares.md).

**Proposed (RFC-0001, #177 — to review):** path labels = the node UUID with hyphens stripped
(ltree-safe, stable); the registry validates each path's parent on write (typed,
not a hard FK); the tree is edited by the Owner + branch-delegated Admins. A grant
may target a principal **or a team**. Depth/shape is per-tenant (`kind`-typed), not
a fixed four-level schema.
