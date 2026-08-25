# Invariants — the non-negotiables

> Rules that hold regardless of feature, phase, or engine choice. Every proposal
> must preserve all of them; a proposal that can't is rejected or must change an
> invariant explicitly (with its own RFC). These back the [behaviors](../0-product/behaviors.md).

## I1 — The RLS tenant floor is sacred
Postgres row-level security enforces `org_id` isolation physically, fail-closed,
on a downgraded role. **No feature ever delegates tenant isolation to a layer
above the database.** Hierarchical scope, per-record sharing, a future ReBAC PDP,
caching — all sit *above* RLS and can only *narrow*, never widen past it. A bug or
outage in any higher layer must be incapable of crossing a tenant boundary.
*(Backs B2.)*

## I2 — Fail closed
Missing context, an unset GUC, an unrecognized scope, a stale revision, an
unreachable PDP — every one resolves to **deny**, never allow. Absence of a
grant is denial (B1).

## I3 — Attenuation is monotonic and dual-enforced
Every delegation hop can only reduce authority. Enforced at **both** issue and
verify, so neither a malicious issuer nor a malicious holder can widen. Owner,
tenant, and task are structurally immutable across a chain. *(Backs B11.)*

## I4 — Authority reflects current state (with a bounded token-staleness window)
**DB-backed** decisions read live state: `CheckPermission` and refresh-time
re-resolution use current membership/roles/revisions, and role/membership changes
revoke affected sessions in the same transaction; capability issuance fails closed
on a stale authorization revision. **Token-claim-backed** decisions are *not*
instantaneous: L1 gates reading `X-Org-Role` and the `sr` scoped-roles claim
reflect the token, so a just-revoked grant can persist until the access token is
refreshed — bounded by the access-token TTL (~15 min), never longer. Gate anything
that must be revoked instantly on the DB-backed path, not the claim. *(Backs B13;
see [B13's freshness question](../0-product/behaviors.md).)*

## I5 — Identity headers are earned, not trusted
Identity forwarded between services is trusted only when stamped by the gateway
credential only the trusted sidecar holds; otherwise the bearer is
cryptographically re-verified. A tenant caller can never forge identity or become
an oracle about other principals.

## I6 — Capability vocabulary is single-sourced
"What a role can do" has exactly one definition (`roles` → `role_permissions`).
Flat RBAC, hierarchical grants, and record shares all resolve capability through
the same rows — never a parallel permission model.

## I7 — Every allow is attributable
For any granted action we can name the acting principal, the grant it flowed
from, and the on-behalf-of principal if any. *(Backs B3, B12.)*

## Operational invariant to verify continuously
- **`SET LOCAL` + transaction-mode pooling.** The org GUC must be transaction-
  scoped so it can't leak across pooled connections. This is the top multi-tenant
  RLS breach footgun; it is asserted, not assumed.
