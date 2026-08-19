# ADR-0003 — Per-record sharing (record_shares overlay)

- **Status:** Accepted
- **Date:** 2026-08-19
- **From:** RFC-[0002](../../2-proposals/0002-per-record-sharing.md) (#177)
- **Context:** RLS proves a row belongs to the caller's org, but nothing grants a
  specific person or team access to a specific record across the ownership boundary.
  `delegation_grants` is ephemeral JIT elevation, not a durable ACL. Product needs
  additive, expirable, team-aware sharing (B7–B10).

## Decision
Add a **`record_shares`** overlay (resource_type, resource_id, subject, role,
`expires_at?`), tenant-scoped and forced-RLS. `CheckAccess` (ADR-0002) gains a share
branch, unioned with the scope branch; effective role = highest across scope grants +
shares (B10). Management RPCs `ShareRecord` / `RevokeShare` / `ListShares` are
audited.

Sub-decisions:
- **Who may share:** a **granted `share` capability**, resolved through
  `role_permissions` like any action (I6). The built-in editor role carries it, so
  it isn't admin-only, but it's grantable/revocable — not an ambient right.
- **Reach:** **intra-org only in v1.** Cross-org / External-Partner shares defer to a
  phase gated on a guest-identity surface; the primitive is built subject-kind-
  agnostic so guests slot in without a reshape.
- **Additive only:** no per-record denial (B8); **no re-share** (default no).
- **Notification:** deferred (product/UX); a `record_share.created` audit event is
  emitted regardless.

## Consequences
- Ships the user-visible "Share" feature that justifies the hierarchy work, reusing
  ADR-0002's resolver and the record-scope binding (so a share decision can't be
  spoofed by a forged path either).
- A second grant source to resolve, kept cheap with indexes.
- Group/userset "shares of shares" are the graduation signal toward a ReBAC PDP.

## Why (not) alternatives
Modeling a share as a scope grant on a per-record pseudo-node conflates two concepts;
an external ReBAC PDP is a natural fit (a tuple = a share) but pays the
two-sources-of-truth cost now. A dedicated overlay table is a single source of truth,
atomic with the row, and reuses the resolver.
