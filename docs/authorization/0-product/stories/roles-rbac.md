# User stories — roles & RBAC (`ROLE`)

> Named bundles of permissions, assigned to principals/teams. Rule: capability is
> single-sourced ([I6](../../1-spec/invariants.md)).

### ROLE-1 · Built-in roles out of the box
**As an** Org Admin, **I want** sensible default roles (owner/admin/editor/viewer), **so that** I can grant access without designing a permission model first.
- Acceptance: built-ins exist with defensible permission sets.
- ❓ Exact set + what each can do? ❓ Are built-ins editable or fixed?

### ROLE-2 · Assign a role to a person
**As an** Org Admin, **I want** to assign a role to a member, **so that** they get its permissions.
- Acceptance: assignment grants the role's `resource:action` permissions org-wide (or scoped).
- 🟡 **Proposed (#177 — to review):** multiple roles per person are **additive** (union). A role assignment can be **scoped to a branch** — org-wide via `role_assignments` (NULL scope) or at a scope node via `scope_grants` (RFC-0001, `H`).

### ROLE-3 · Assign a role to a team
**As an** Org Admin, **I want** to grant a role to a whole team, **so that** members inherit it.
- Acceptance: team members inherit the team's role. (ties to `TEAM`)
- 🟡 **Proposed (#177 — to review):** yes — team + personal grants **stack as a union, highest-wins** (B10); no source subtracts (B8).

### ROLE-4 · Create a custom role
**As an** Org Admin, **I want** to define a custom role with specific permissions, **so that** I model our exact job functions.
- Acceptance: pick `resource:action` pairs (and wildcards); custom roles are org-owned.
- ❓ Is custom-role authoring in v1, or built-ins only? ❓ Any cap on custom roles? ❓ Can custom roles use wildcards (`reports:*`)?

### ROLE-5 · Wildcard permissions
**As an** Org Admin, **I want** to grant "all actions on reports" or "read everything," **so that** I don't enumerate every pair.
- Acceptance: `resource:*`, `*:action`, `*:*` supported (exists today).
- ❓ Should wildcards be admin-only (they're powerful)? ❓ Surface them in UI or keep for catalog import?

### ROLE-6 · Least-privilege defaults
**As a** security lead, **I want** new members to default to minimal access, **so that** people don't over-accumulate rights.
- Acceptance: default role is low-privilege; escalations explicit.
- ❓ Default = viewer, or no access until granted?

### ROLE-7 · Prevent privilege escalation via granting
**As a** security lead, **I want** an admin unable to grant permissions they don't themselves hold, **so that** they can't self-escalate.
- Acceptance: you can only grant ≤ your own authority.
- 🟡 **Proposed (#177 — to review):** yes — **enforce "can't grant above your own authority"** at grant time (attenuation for grants). The **Owner is the ceiling** within their tenant, so there is no "above self" for them to exceed; everyone else is bounded by what they hold.

### ROLE-8 · Separation of duties
**As a** compliance officer, **I want** certain role combinations forbidden (e.g. can't both create and approve payments), **so that** we meet controls.
- Acceptance: mutually-exclusive role sets enforced at assignment.
- ❓ Is SoD in scope? Which pairs? ❓ Enforce at grant time or detect after?

### ROLE-9 · Sync roles from an external catalog
**As a** platform team, **I want** to import our role/permission catalog from our own system, **so that** roles stay consistent across products.
- Acceptance: versioned catalog import (exists); diff-apply; provenance-audited.
- ❓ Who owns the catalog of record? ❓ Product-defined resources/actions registered how?

### ROLE-10 · Review effective permissions of a role
**As an** Org Admin, **I want** to see exactly what a role can do, **so that** I grant it confidently.
- Acceptance: expand a role to its `resource:action` set incl. inherited/wildcard.
- ❓ Show raw pairs, or a friendly capability description?
