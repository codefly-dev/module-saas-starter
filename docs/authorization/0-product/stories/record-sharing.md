# User stories — per-record sharing

> **Strawman for review.** Capability: grant a specific person or team access to
> one record, across the normal ownership boundary. Rules cited:
> [B7, B8, B9, B10](../behaviors.md). Personas:
> [Member, External Partner, Org Admin](../personas.md).

## Story S1 — Share one record with a colleague

**As a** Member, **I want** to share a single record with another member,
**so that** they can collaborate on it without my giving them access to
everything in that scope.

**Acceptance criteria**
- When I share record `123` with user Y as "editor," Y can open and edit `123`
  (B7) and gains **no** other access.
- Y reaching `123` sees the effective role that is strongest across their scope
  grants and this share (B10).
- Nobody else's access changes (B7).

## Story S2 — Share with a team

**As an** Org Admin, **I want** to share a record with a whole team,
**so that** every current and future member of that team can access it without
re-sharing.

**Acceptance criteria**
- Sharing `123` with team `eng` grants access to all members of `eng`.
- A person added to `eng` later automatically gains the shared access; removed,
  they lose it.

## Story S3 — Time-boxed external share

**As an** Org Admin, **I want** to share a record subtree with an external
partner for a fixed window, **so that** an auditor can review and then
automatically lose access.

**Acceptance criteria**
- I share with an expiry; before it, the partner has the granted role; after it,
  access is gone with no manual revoke (B9).
- The partner sees only what was shared (B1, B6), nothing else in the org (B2).

## Story S4 — See and revoke who has access

**As a** Member (owner of a record), **I want** to see everyone a record is
shared with and revoke any share, **so that** access stays intentional.

**Acceptance criteria**
- I can list all shares on a record (users, teams, roles, expiries).
- Revoking a share removes that access immediately; inherited/scope access is
  unaffected (B7).

## Explicitly not in this story set (v1)
- **Per-record denials.** Shares only ever *add* access (B8). A share that
  *removes* inherited access is a separate, conflict-heavy feature — deferred.
- Re-sharing rights ("can Y share `123` onward?") — deferred; default no.

## Open questions
- Can a **Member** share, or only an **Admin**? (i.e. is "share" a capability
  every editor has, or a granted one?)
- Cross-org shares (External Partner) — first cut, or after intra-org sharing
  proves out? (Ties to the External Partner persona question.)
- Notifications: does sharing notify the recipient? (Product/UX, but affects the
  interface.)
