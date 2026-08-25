# User stories — permission hierarchies (`PERM`)

> How permissions and roles relate to each other: inheritance, seniority,
> precedence. Distinct from *scope* hierarchy (`H`, the data tree) — this is
> about the *permission model* itself.

### PERM-1 · Senior roles include junior ones
**As an** Org Admin, **I want** "editor" to automatically include everything "viewer" can do, **so that** I don't re-list read permissions on every role.
- Acceptance: role inheritance (NIST RBAC1) — senior ⊇ junior.
- 🟡 **Proposed (#177 — to review):** keep roles **flat + explicit** in v1 — no role-to-role inheritance graph. "Senior ⊇ junior" is expressed by the built-in roles' *permission sets* plus the action ladder (PERM-2), not a role-inheritance edge. This stays single-sourced (I6) and avoids a multi-inheritance resolver. Arbitrary multi-inheritance is deferred.

### PERM-2 · Action implies weaker action
**As a** product designer, **I want** "write" to imply "read," "admin" to imply "write," **so that** capabilities nest naturally.
- Acceptance: an action ladder (read < write < admin) resolves implications.
- 🟡 **Proposed (#177 — to review):** a **global** ladder `admin ⊇ write ⊇ read` (not per-resource), **realized at role-definition time** — a senior role's permission set explicitly includes the junior rows, so there is no new runtime implication and `CheckPermission` stays exact-match (I6; no such logic exists today). `delete`, `list`, `share`, `export`, `create` are **orthogonal** — *not* on the ladder, granted explicitly (see the canonical action set in [`resources-and-actions.md`](resources-and-actions.md)).

### PERM-3 · Most-specific grant wins
**As an** Org Admin, **I want** a specific grant to override a broad one, **so that** I can make exceptions (broad viewer, editor on one branch).
- Acceptance: most-specific-wins across scope (B5); highest-privilege-wins across paths (B10).
- 🟡 **Proposed (#177 — to review):** both confirmed, they cannot conflict (different axes — see [`hierarchical-access.md`](hierarchical-access.md) H2). A specific grant **never reduces** below a broad one — no per-record denial in v1 (B8, PERM-7).

### PERM-4 · Combine grants from many sources
**As a** Member, **I want** my effective access to be the sum of my personal grants, team grants, and shares, **so that** access is predictable.
- Acceptance: effective = union, highest-wins (B10).
- 🟡 **Proposed (#177 — to review):** **pure union, highest-wins** — no source subtracts (B8, B10). The winning grant is what PERM-6 reports as the "why."

### PERM-5 · Scope-limited roles
**As an** Org Admin, **I want** to say "analyst, but only for solution X," **so that** a role applies to a branch, not the whole org.
- Acceptance: a role assignment can carry a scope (ties to `H`); NULL scope = org-wide.
- 🟡 **Proposed (#177 — to review):** both coexist — flat `role_assignments.scope` (today; NULL = org-wide) via `CheckPermission`, and hierarchical `scope_grants` (RFC-0001) via `CheckAccess`. One assignment/grant targets **one** scope node; spanning several is several grants.

### PERM-6 · Explain why access was granted or denied
**As a** Member confused by a denial, **I want** to understand why, **so that** I can request the right access.
- Acceptance: a decision can report its reason (which grant / which missing permission).
- 🟡 **Proposed (#177 — to review):** admins get the full reason (which grant / missing permission); end users get a generic "you don't have access, request it" — never the specifics, to avoid an info-leak oracle (B1).

### PERM-7 · Deny beats allow (when we ever add deny)
**As a** security lead, **I want** an explicit deny to always win over an allow, **so that** prohibitions are absolute.
- Acceptance: *if* explicit denies exist, deny overrides allow.
- 🟡 **Proposed (#177 — to review):** **no explicit denies in v1** (B8). The rule stays latent: *if* denies are ever added, deny beats allow — but the complexity isn't justified now.

### PERM-8 · Permission catalog is discoverable
**As a** product team, **I want** the full set of resources and actions to be a known, typed catalog, **so that** grants can't reference nonsense.
- Acceptance: resources/actions are a validated vocabulary (like MethodPolicy today).
- 🟡 **Proposed (#177 — to review):** the **product registers** its resources/actions into the typed catalog (as `MethodPolicy` declares today); the catalog is validated, and unknown vocabulary is **fail-closed** (yes) — a grant referencing an unregistered resource/action never matches (I2, PERM-8).
