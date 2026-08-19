# User stories — hierarchical / layered access

> **Strawman for review.** Capability: grant access at a level of a tree and have
> it flow down. Rules cited: [B4, B5, B6](../behaviors.md). Personas:
> [Org Admin, Member, Agent](../personas.md).

## Story H1 — Grant an analyst access to one branch

**As an** Org Admin, **I want** to grant an analyst the "viewer" role on a single
solution, **so that** they can see everything under that solution but nothing in
sibling solutions.

**Acceptance criteria**
- Given the scope tree `foundation → solution_x → customer_* → individual_*`,
  when I grant `viewer @ foundation.solution_x`, the analyst can read every
  record under `solution_x` (B4), and **cannot** see any record under
  `solution_y` (B2, B1).
- The analyst never sees that `solution_y` records exist (B6).
- Revoking the grant removes all that access in one action.

## Story H2 — More specific access overrides broader

**As an** Org Admin, **I want** to give someone broad read but elevated rights on
one customer, **so that** they can edit just that customer's records while reading
the rest.

**Acceptance criteria**
- Given `viewer @ foundation.solution_x` and `editor @ foundation.solution_x.customer_7`,
  when the user opens a record under `customer_7`, their effective role is
  **editor** (B5, B10); elsewhere under `solution_x` it is **viewer**.
- The system resolves this without the user choosing a "mode."

## Story H3 — An agent inherits only its slice

**As an** Org Owner, **I want** an agent I authorize for one solution to be unable
to touch another, **so that** automating one area can't leak into others.

**Acceptance criteria**
- When I authorize an agent to act for me scoped to `foundation.solution_x`, the
  agent can act under `solution_x` (bounded by my own rights, B11) and is denied
  everywhere else (B2), even though *I* can see more.

## Story H4 — Model the tree without free-text scopes

**As an** Org Admin, **I want** the scope levels to be real, validated things,
**so that** I can't fat-finger a grant onto a scope that doesn't exist.

**Acceptance criteria**
- Granting at a scope that isn't a registered node fails with a clear error
  (typed scope registry) rather than silently never matching.
- I can see the scope tree and pick a node; I don't type a path string.

## Explicitly not in this story set
- Per-record exceptions (that's `record-sharing.md`).
- Reducing access below the inherited level at a leaf (B8 — out of scope v1).

## Open questions
- Who can **edit the scope tree** — only Owner, or delegated Admins per branch?
- Can a grant target a **team** at a scope node (team-scoped inheritance), or
  only an individual principal? (Spec assumes both; confirm the product need.)
- Depth: is `foundation → solution → customer → individual` the real shape, or an
  example? The primitive is depth-agnostic, but the UI/registry should know.
