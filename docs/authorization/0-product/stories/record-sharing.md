# User stories — per-record sharing (`S`)

> **Strawman for review.** Grant a specific person or team access to one record,
> across the ownership boundary. Rules: [B7, B8, B9, B10](../behaviors.md).
> Personas: [Member, External Partner, Org Admin](../personas.md).

### S1 · Share one record with a colleague
**As a** Member, **I want** to share a single record with another member, **so that** we collaborate on it without my granting access to everything in that scope.
- Acceptance: sharing record `123` with user Y as "editor" lets Y edit `123` and grants no other access (B7); Y's effective role is strongest across scope grants + this share (B10); nobody else's access changes (B7).
- 🟡 **Proposed (#177 — to review):** sharing is a **granted capability**, not ambient. `share` would be a first-class action in the role vocabulary, resolved through `role_permissions` like any other (I6). Note there is **no `share` action today** — this proposes adding it; the built-in **editor** role would then carry it by default, so a Member who can edit a record could share it — but it stays grantable/revocable like any permission and can be withheld. Not admin-only (S1 is deliberately a Member story); not implicitly-any-editor (that can't be audited or tightened as a distinct grant). → RFC-0002.

### S2 · Share with a team
**As an** Org Admin, **I want** to share a record with a whole team, **so that** every current and future member can access it without re-sharing.
- Acceptance: sharing `123` with team `eng` reaches all members; someone added to `eng` later gains it, removed loses it.
- 🟡 **Proposed (#177 — to review):** yes — effective access is the **union** of personal + team shares + scope grants, **highest-role-wins** (B10). No source subtracts (B8).

### S3 · Time-boxed external share
**As an** Org Admin, **I want** to share a record subtree with an external partner for a fixed window, **so that** an auditor can review and then automatically lose access.
- Acceptance: share carries an expiry — before it the partner has the role, after it access is gone with no manual revoke (B9); the partner sees only what was shared (B1, B6), nothing else in the org (B2).
- 🟡 **Proposed (#177 — to review):** **intra-org only in v1.** Cross-org / External-Partner shares defer to a later phase, gated on a sanctioned guest-identity path (see [`external-and-guest.md`](external-and-guest.md)). Sharing across the tenant boundary must be a narrow, audited path — never a hole in the RLS floor (I1/B2). → RFC-0002.

### S4 · See and revoke who has access
**As a** Member (record owner), **I want** to see everyone a record is shared with and revoke any share, **so that** access stays intentional.
- Acceptance: list all shares on a record (users, teams, roles, expiries); revoking one removes that access immediately, leaving inherited/scope access untouched (B7).
- 🟡 **Proposed — defer (#177 — to review):** no in-app notification in v1; a `record_share.created` audit event is emitted ([interfaces](../../1-spec/interfaces.md)). Recipient notification is a product surface, revisited when the Share UI lands — it does not block the primitive.

### Explicitly not in this set (v1)
- **Per-record denials.** Shares only *add* access (B8); a share that *removes* inherited access is a separate, conflict-heavy feature — out of scope v1.
- Re-sharing ("can Y share `123` onward?") — out of scope v1; default no.
