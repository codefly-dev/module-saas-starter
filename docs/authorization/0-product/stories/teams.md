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
- ❓ Do personal + team grants stack (union, highest-wins)?

### TEAM-3 · Nested teams / sub-teams
**As an** Org Admin, **I want** sub-teams under a parent team, **so that** structure mirrors the org.
- Acceptance: teams form a tree (exists: `teams.path`).
- ❓ **Key:** does a grant to a parent team flow to sub-teams (subtree inheritance), or is membership literal (today's behavior)? This is an open gap.

### TEAM-4 · Automatic team membership by attribute
**As an** Org Admin, **I want** people to join a team automatically by attribute (department, title), **so that** I don't hand-manage membership.
- Acceptance: rule-based membership (e.g. from IdP groups/claims).
- ❓ Is auto-membership in scope? ❓ Source of truth — IdP groups, or manual?

### TEAM-5 · A person in many teams
**As a** Member, **I want** to belong to several teams, **so that** I get the combined access.
- Acceptance: access = union of all teams' grants (+ personal).
- ❓ Conflict resolution (highest-wins confirmed)?

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
- ❓ Show inherited-from-parent-team access (if subtree inheritance exists)?
