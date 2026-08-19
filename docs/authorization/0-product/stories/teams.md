# User stories — teams (`TEAM`)

> Groups of principals that carry grants collectively. Teams already form a
> materialized-path tree (`teams.path`) today, resolved *literally* by authz.

### TEAM-1 · Create a team and add members
**As an** Org Admin, **I want** to group people into a team, **so that** I grant access once to the group.
- Acceptance: teams have members; grants to a team reach its members.
- ❓ Who can create teams / manage membership (admin, or team lead)?

### TEAM-2 · Grant access to a team
**As an** Org Admin, **I want** to grant a role/scope/share to a team, **so that** all members get it without individual grants.
- Acceptance: team grants resolve to members (RBAC, scope, and shares all support team subjects).
- 🟡 **Proposed (#177 — to review):** yes — personal + team grants **stack as a union, highest-wins** (B10); no source subtracts (B8).

### TEAM-3 · Nested teams / sub-teams
**As an** Org Admin, **I want** sub-teams under a parent team, **so that** structure mirrors the org.
- Acceptance: teams form a tree (exists: `teams.path`).
- 🟡 **Proposed (#177 — to review):** membership stays **literal** — a grant to a parent team does **not** flow to sub-teams in v1. `teams.path` already forms the tree, but subtree-inheriting team grants add surprising blast radius (create a sub-team → its members silently gain every parent grant) for little proven need. What *does* combine is a person's own multiple memberships (TEAM-5, union). Subtree inheritance reopens later via the same `path`-prefix mechanism if a real case appears; until then it is out of scope.

### TEAM-4 · Automatic team membership by attribute
**As an** Org Admin, **I want** people to join a team automatically by attribute (department, title), **so that** I don't hand-manage membership.
- Acceptance: rule-based membership (e.g. from IdP groups/claims).
- ❓ Is auto-membership in scope? ❓ Source of truth — IdP groups, or manual?

### TEAM-5 · A person in many teams
**As a** Member, **I want** to belong to several teams, **so that** I get the combined access.
- Acceptance: access = union of all teams' grants (+ personal).
- 🟡 **Proposed (#177 — to review):** access = **union** of all the person's teams' grants + personal grants, **highest-wins** (B10).

### TEAM-6 · Team-scoped admins
**As an** Org Owner, **I want** a team lead who administers only their team, **so that** I delegate management without full org admin.
- Acceptance: a team-admin role bounded to that team's subtree.
- ❓ Can team admins add external people, or only existing members? ❓ Scope of their authority (members only, or grants too)?

### TEAM-7 · Remove from team = lose team access
**As an** Org Admin, **I want** removing someone from a team to remove exactly that team's access, **so that** offboarding is clean.
- Acceptance: removal drops team-derived grants only; personal grants untouched.
- ❓ Immediate (B13)? ❓ Effect on things shared *to* the team the person is now out of.

### TEAM-8 · See a team's effective access
**As an** Org Admin, **I want** to see everything a team can do, **so that** I can review it.
- Acceptance: expand team → roles, scopes, shares, members.
- 🟡 **Proposed (#177 — to review):** N/A in v1 — there is no parent-team inheritance to show (TEAM-3 literal). The view expands a team to its own roles, scopes, shares, and members.
