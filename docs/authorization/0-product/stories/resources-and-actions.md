# User stories — resources & actions (`RES`)

> The granularity of *what* you can act on and *how*: resource types,
> read/write/delete/create/list, ownership. This is the heart of "read vs write."

### RES-1 · Read vs write are separate
**As an** Org Admin, **I want** to grant read without write, **so that** viewers can't change data.
- Acceptance: `read` and `write` are distinct actions; read never implies write.
- ❓ Confirm the base action set: read, write, create, delete, list, share, admin? ❓ Which are separate vs bundled?

### RES-2 · Create vs edit are separate
**As an** Org Admin, **I want** to let someone edit existing records but not create new ones (or vice versa), **so that** I control data growth.
- Acceptance: `create` distinct from `write`/`update`.
- ❓ Is create its own action, or part of write? ❓ Does create imply ownership of the created record?

### RES-3 · Delete needs a stronger right than edit
**As a** data owner, **I want** deletion to require more than edit, **so that** routine editors can't destroy data.
- Acceptance: `delete` is its own action; irreversible deletes may need confirm/step-up.
- ❓ Is delete separate from write? ❓ Soft-delete vs hard-delete — who can hard-delete? ❓ Bulk-delete gating?

### RES-4 · List/discover vs read-detail
**As a** product designer, **I want** "see that it exists in a list" to be separable from "open and read it," **so that** search results don't leak content.
- Acceptance: `list`/`discover` distinct from `read`; default-deny hides existence (B1, B6).
- ❓ Do we need list-vs-read separation, or is visibility binary? ❓ Metadata-visible-but-content-locked a real case?

### RES-5 · Per-resource-type permissions
**As an** Org Admin, **I want** permissions per type of thing (reports, dashboards, connections…), **so that** access matches the domain.
- Acceptance: resource type is first-class in `resource:action`.
- ❓ Who defines the product's resource types? ❓ Fixed catalog vs per-tenant custom types?

### RES-6 · Ownership grants baseline rights
**As a** Member, **I want** to fully control things I create, **so that** I can manage my own work.
- Acceptance: creator/owner gets an owner-level right on their record by default.
- ❓ Is there a per-record "owner" concept? ❓ Can ownership transfer? ❓ Does owner always win, even over admin?

### RES-7 · Admin override
**As an** Org Admin, **I want** to access/manage any record in my scope even if I don't own it, **so that** I can administer.
- Acceptance: admin role covers records within its scope.
- ❓ Does admin see *content* or just *manage* (metadata/permissions) — "manage without read"? ❓ Break-glass + audit for admin content access?

### RES-8 · Action on many records at once (bulk)
**As a** power user, **I want** to act on many records at once, **so that** I'm efficient — **but** only where I'm allowed.
- Acceptance: bulk ops authorize per-record; partial success reports what was denied.
- ❓ All-or-nothing vs partial? ❓ Extra gating for bulk destructive ops?

### RES-9 · Export / download as a distinct right
**As a** compliance lead, **I want** "export data" to be its own permission, **so that** reading in-app ≠ bulk exfiltration.
- Acceptance: `export` distinct from `read`; audited.
- ❓ Is export a first-class action? ❓ Watermark/limit exports?

### RES-10 · Act on a sub-part of a record (nested)
**As a** product designer, **I want** to authorize part of a structured record (a section/subtree), **so that** access can be finer than whole-record.
- Acceptance: either decompose into rows (recommended) or subtree authz (gap 5).
- ❓ Is sub-record authz a real need, or do we model sub-parts as their own records? (gap analysis leans: model as rows)

### RES-11 · Cross-resource actions
**As a** user, **I want** an action that touches several resources (e.g. "publish" reads a report + writes a channel) to check all of them, **so that** it can't bypass a missing right.
- Acceptance: composite actions authorize every resource they touch.
- ❓ How do we model multi-resource operations — enumerate in policy, or per-step checks in code?

### RES-12 · Read vs write on relationships/links
**As a** user, **I want** linking two records (assigning, tagging, relating) to be governed, **so that** relationships aren't a backdoor.
- Acceptance: creating/removing a link checks rights on both ends.
- ❓ Is "relate" its own action? ❓ Which side's permission governs?
