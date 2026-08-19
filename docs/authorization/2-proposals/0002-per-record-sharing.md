# RFC-0002 — Per-record sharing (record_shares overlay)

- **Status:** Draft
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

## Open questions
- Who may share — every editor, or admin-only (is "share" a capability)?
- Cross-org (External Partner) shares in v1 or later?
- Notification on share (product/UX).

## Decision
_Pending review._
