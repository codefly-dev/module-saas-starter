# User stories — permission hierarchies (`PERM`)

> How permissions and roles relate to each other: inheritance, seniority,
> precedence. Distinct from *scope* hierarchy (`H`, the data tree) — this is
> about the *permission model* itself.

### PERM-1 · Senior roles include junior ones
**As an** Org Admin, **I want** "editor" to automatically include everything "viewer" can do, **so that** I don't re-list read permissions on every role.
- Acceptance: role inheritance (NIST RBAC1) — senior ⊇ junior.
- ❓ Do we model role inheritance, or keep roles flat + explicit? ❓ Tree (limited) or arbitrary multi-inheritance?

### PERM-2 · Action implies weaker action
**As a** product designer, **I want** "write" to imply "read," "admin" to imply "write," **so that** capabilities nest naturally.
- Acceptance: an action ladder (read < write < admin) resolves implications.
- ❓ Is there a global action ladder, or per-resource? ❓ Is `delete` on the ladder or separate? (see `RES`)

### PERM-3 · Most-specific grant wins
**As an** Org Admin, **I want** a specific grant to override a broad one, **so that** I can make exceptions (broad viewer, editor on one branch).
- Acceptance: most-specific-wins across scope (B5); highest-privilege-wins across paths (B10).
- ❓ Confirm both rules; do they ever conflict? ❓ Can a specific grant ever *reduce* below a broad one (deny)? (default no, B8)

### PERM-4 · Combine grants from many sources
**As a** Member, **I want** my effective access to be the sum of my personal grants, team grants, and shares, **so that** access is predictable.
- Acceptance: effective = union, highest-wins (B10).
- ❓ Pure union, or can any source subtract? ❓ How is a conflict surfaced/explained?

### PERM-5 · Scope-limited roles
**As an** Org Admin, **I want** to say "analyst, but only for solution X," **so that** a role applies to a branch, not the whole org.
- Acceptance: a role assignment can carry a scope (ties to `H`); NULL scope = org-wide.
- ❓ Flat scope labels (today) vs hierarchical scope (RFC-0001)? ❓ Can one assignment span multiple scopes?

### PERM-6 · Explain why access was granted or denied
**As a** Member confused by a denial, **I want** to understand why, **so that** I can request the right access.
- Acceptance: a decision can report its reason (which grant / which missing permission).
- ❓ Expose "why denied" to end users, or admins only (info-leak risk)?

### PERM-7 · Deny beats allow (when we ever add deny)
**As a** security lead, **I want** an explicit deny to always win over an allow, **so that** prohibitions are absolute.
- Acceptance: *if* explicit denies exist, deny overrides allow.
- ❓ Do we ever want explicit denies? (adds big complexity — default: no in v1)

### PERM-8 · Permission catalog is discoverable
**As a** product team, **I want** the full set of resources and actions to be a known, typed catalog, **so that** grants can't reference nonsense.
- Acceptance: resources/actions are a validated vocabulary (like MethodPolicy today).
- ❓ Who registers product resources/actions? ❓ Fail-closed on unknown vocabulary (yes)?
