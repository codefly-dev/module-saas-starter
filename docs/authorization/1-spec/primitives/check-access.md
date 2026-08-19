# Primitive — CheckAccess (the resolver)

**What it is.** The decision function for hierarchical + per-record authorization.
The companion to the existing `CheckPermission` (flat/org-wide RBAC). In one line:

> **`CheckAccess` = `CheckPermission` with an ltree ancestor-match on scope
> instead of flat equality, plus a per-record-share overlay.**

**Contract**
- Answers `(subject, resource_type, resource_id, record_scope, action) → allow/deny`.
- Allow if **either** an ancestor **scope grant** or a direct **record share**
  grants a role that permits `(resource_type, action)` — resolved through the
  same `role_permissions` rows as RBAC (I6).
- Same subject model as `CheckPermission`: principal, or team via membership.
- Internal/PDP exposure, org-scoped, fail-closed (I2).
- Never a substitute for the RLS floor — composes above it (I1).

**Interaction with existing RBAC.** Handlers check the cheap org-wide capability
via `CheckPermission` first, then fall through to `CheckAccess` for the
hierarchical/shared case. `role_assignments` and `CheckPermission` are untouched —
this is strictly additive.

**Serves:** the decision half of every story in `0-product/`.
**Realized by:** RFC-0001 + RFC-0002; sketch in
[`ltree-record-shares`](../../3-implementation/spikes/ltree-record-shares.md).
