# Primitive — record share

**What it is.** A per-instance ACL overlay: "subject may act on **this specific
record** with this role," independent of the scope hierarchy. The durable,
standing sharing primitive that `delegation_grants` (ephemeral JIT elevation)
deliberately is not.

**Contract**
- Additive only: a share grants access, never removes anyone else's (B7, B8).
- Effective role combines shares + scope grants, highest-wins (B10).
- May carry an expiry; access ends automatically (B9).
- Subject may be a principal or a team (team shares follow membership).
- Capability resolved through the same `role_permissions` as everything else (I6).
- v1 has **no per-record denial** (no override of an inherited allow).

**Serves stories:** [record-sharing](../../0-product/stories/record-sharing.md) S1–S4.
**Realized by:** RFC-0002 → implementation spike
[`ltree-record-shares`](../../3-implementation/spikes/ltree-record-shares.md).

**Decisions (RFC-0002, #177):** who may share = a **granted `share` capability**
(built-in editor has it; not admin-only, not ambient); **intra-org only in v1**
(cross-org guest shares deferred); **no re-share**; additive-only, no per-record
denial.
