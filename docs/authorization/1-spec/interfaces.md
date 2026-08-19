# Interfaces — the API surface

> **Seed.** Vendor-neutral request/response shapes for the new primitives. These
> firm up as the RFCs are accepted; treat signatures as illustrative, not final.
> Concrete proto lives with the implementation, not here.

## Decision

### `CheckAccess` — hierarchical + per-record decision
The companion to the existing `CheckPermission` (flat/org-wide RBAC). Answers
"may this subject perform `(resource_type, action)` on this specific record."

```
CheckAccess(
  subject:        { id, kind: principal | team },
  resource_type:  string,        // same vocabulary as role_permissions.resource
  resource_id:    string,        // the product's record id
  action:         string,
  org:            id,
) -> { allowed: bool, via: "scope" | "share" | "none" }
```
Contract: internal/PDP exposure (like `CheckPermission`); org-scoped; fail-closed.
The record's scope is **not a caller field** — `CheckAccess` resolves it from the
`record_scopes` binding by `resource_id`, so a caller can't forge a path it isn't
entitled to (resolved review follow-up, [spike](../3-implementation/spikes/ltree-record-shares.md)
open question 2).

### List visibility
"Which records may this subject see," with most-specific-wins — returns ids +
effective role, run under the tenant floor. Shape TBD (likely a filter the
product applies to its own query rather than an RPC that returns all ids).

## Grants & shares (management)

```
GrantScope(subject, scope_path, role, expires_at?)      -> grant_id
RevokeScopeGrant(grant_id)
ShareRecord(resource_type, resource_id, subject, role, expires_at?) -> share_id
RevokeShare(share_id)
ListShares(resource_type, resource_id) -> [ { subject, role, expires_at, granted_by } ]
```
Each is org-scoped, audited (`scope_grant.created` / `record_share.created` /
`.revoked`), and gated — see the open question on who may share
([record-sharing story](../0-product/stories/record-sharing.md)).

## Scope registry (management)

```
CreateScopeNode(parent_scope_path, kind, label) -> scope_path   // validates parent exists
ListScopeTree(root?) -> tree
```

## On-behalf-of / capability (mostly exists)
The capability chain (`StartTask`, `StartChildSession`, `ExchangeAudience`) and
`delegation_grants` (request/approve) already exist. The new surface is about
**durability + linkage + interop** (RFC-0003), e.g. resolving the durable chain
for an action, and an RFC 8693 `act`-claim mapping for external interop. Shapes
TBD in that RFC.

## Cross-cutting
- Every decision RPC is **internal exposure** and **fail-closed** (I2).
- Capability resolution is single-sourced through `role_permissions` (I6).
- None of these replace RLS; they compose above it (I1).
