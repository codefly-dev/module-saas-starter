# User stories — hierarchical / layered access (`H`)

> **Strawman for review.** Grant access at a level of a tree and have it flow
> down. Rules: [B4, B5, B6](../behaviors.md). Personas: [Org Admin, Member, Agent](../personas.md).

### H1 · Grant an analyst access to one branch
**As an** Org Admin, **I want** to grant an analyst "viewer" on a single solution, **so that** they see everything under it but nothing in sibling solutions.
- Acceptance: `viewer @ foundation.solution_x` reads every record under `solution_x` (B4); cannot see or even know of `solution_y` records (B1, B2, B6); revoking removes it all in one action.
- 🟡 **Proposed (#177 — to review):** **Owner + branch-delegated Admins.** An Admin holding `admin` at a scope node may create child nodes under it and grant within that subtree, bounded by their own authority (can't grant above self, ROLE-7). Reshaping the root / top-level tree stays Owner-level. → RFC-0001.

### H2 · More specific access overrides broader
**As an** Org Admin, **I want** to give broad read but elevated rights on one customer, **so that** they can edit that customer while reading the rest.
- Acceptance: with `viewer @ …solution_x` + `editor @ …solution_x.customer_7`, effective role under `customer_7` is **editor** (B5, B10), **viewer** elsewhere — resolved without the user choosing a "mode."
- 🟡 **Proposed (#177 — to review):** confirmed — they are **different axes and cannot conflict.** B5 (most-specific-*visible*-wins) picks *which single node* you resolve a record at; B10 (highest-grant-wins) picks *how strong a role* you hold among all grants that reach it. Resolution order: find the record's most-specific applicable node (B5), then take the strongest role among every grant — scope + team + share — reaching that node (B10).

### H3 · An agent inherits only its slice
**As an** Org Owner, **I want** an agent I authorize for one solution unable to touch another, **so that** automating one area can't leak into others.
- Acceptance: an agent scoped to `foundation.solution_x` acts only there (bounded by my rights, B11) and is denied elsewhere (B2), even though I can see more.
- 🟡 **Proposed (#177 — to review):** yes — a scope grant may target a **team** (`scope_grants.subject_kind = 'team'`). A human inherits it through team membership, resolved **literally** (a parent-team grant does not cascade to sub-teams — see [`teams.md`](teams.md) TEAM-3). → RFC-0001.

### H4 · Model the tree without free-text scopes
**As an** Org Admin, **I want** scope levels to be real, validated things, **so that** I can't fat-finger a grant onto a scope that doesn't exist.
- Acceptance: granting at an unregistered node fails with a clear error (typed registry), not a silent never-match; I pick a node from the tree, not type a path string.
- 🟡 **Proposed (#177 — to review):** it is an **example, not a fixed schema.** The primitive is depth-agnostic; each node carries a `kind` that types its level *per tenant/product* (`scope_nodes.kind`), and the registry/UI reads the tenant's actual tree rather than assuming four fixed levels. The starter ships no hard-coded level names. → RFC-0001.

### Explicitly not in this set
- Per-record exceptions → [`record-sharing.md`](record-sharing.md).
- Reducing access below the inherited level at a leaf (B8 — out of scope v1).
