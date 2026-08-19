# RFC-0002 — Per-record sharing (record_shares overlay)

- **Status:** Accepted (2026-08-19, #177) — ADR [0003](../9-reference/decisions/0003-per-record-sharing.md)
- **Created:** 2026-08-19
- **Serves:** [record-sharing](../0-product/stories/record-sharing.md) S1–S4; behaviors B7–B10.
- **Depends on:** RFC-0001 (shares the `CheckAccess` resolver + capability vocabulary).

## Context
RLS proves a row belongs to the caller's org; there is no primitive to grant a
specific person/team access to a specific record across the ownership boundary
(gap 2). `delegation_grants` is ephemeral JIT elevation, not a durable ACL. Need
additive, expirable, team-aware sharing (B7–B10), highest-grant-wins (B10).

## Proposal (summary)
- **`record_shares`**: (resource_type, resource_id, subject, role, expires_at?),
  tenant-scoped, forced RLS.
- `CheckAccess` gains an overlay branch: allow if a direct share grants a role
  permitting the action — unioned with the scope-grant branch (RFC-0001).
- Effective role = highest across scope grants + shares (B10).
- Management RPCs: `ShareRecord`, `RevokeShare`, `ListShares` (audited).
- v1: **additive only, no per-record denial** (B8); no re-share.

## Alternatives considered
- **Model sharing as scope grants** on a per-record pseudo-node — conflates two
  concepts; rejected for clarity, may unify later.
- **External ReBAC PDP** — native fit (a tuple = a share) but two-sources-of-truth
  cost now; graduation target, deferred.
- **Overlay table** — chosen: single source of truth, atomic with the row, reuses
  the resolver.

## Consequences
- Ships the user-visible "Share" feature that justifies the hierarchy work.
- Adds a second grant source to resolve; kept cheap via indexes.
- Group/userset shares of shares are the graduation signal toward a PDP.

## Resolved questions (#177)
- **Who may share → a granted capability.** `share` is a first-class action in the
  role vocabulary (resolved through `role_permissions`, I6); the built-in **editor**
  role carries it by default, so it's not admin-only, but it's grantable/revocable
  like any permission — not an ambient right of every editor. (record-sharing S1.)
- **Cross-org shares → later.** v1 is **intra-org only**; External-Partner shares
  defer to a phase gated on a guest-identity surface (external-and-guest, personas).
  The primitive is built subject-kind-agnostic so guests slot in without a reshape.
- **Notification → deferred (product/UX).** No in-app notification in v1; a
  `record_share.created` audit event is emitted. Notification is a UI surface added
  with the Share feature, not a blocker for the primitive.

## Decision
**Accepted (2026-08-19).** Build the `record_shares` overlay + a `CheckAccess` share
branch, **additive only** (no per-record denial, B8), **no re-share** (default no),
**intra-org v1**, gated by the `share` capability. Effective role = highest across
scope grants + shares (B10). Recorded as ADR-[0003](../9-reference/decisions/0003-per-record-sharing.md);
phased at roadmap P2 (after RFC-0001's resolver lands in P1).
