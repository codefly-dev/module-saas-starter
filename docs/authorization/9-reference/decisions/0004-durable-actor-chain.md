# ADR-0004 — Durable, linked, revocable actor chain

> **Draft — proposed in #177, not yet accepted.** The decision below is a
> recommendation pending review; RFC-0003 is in **Review**. This becomes the
> immutable Accepted record only when the RFC is signed off.

- **Status:** Draft (proposed #177)
- **Date:** 2026-08-19
- **From:** RFC-[0003](../../2-proposals/0003-durable-actor-chain.md) (#177)
- **Context:** The capability chain already enforces attenuation correctly at sign
  and verify (I3) — the hard part is done. What's missing is record-keeping: the
  chain is ephemeral (no record after token expiry, B14), `delegation_grants` is
  single-hop and unlinked, unified audit has no on-behalf-of field beyond
  impersonation, and the `created_by` owner edge isn't checked at issuance.

## Decision
Make the existing chain **durable, linked, revocable, and interoperable** without
replacing its owner/tenant/task guarantees:
- **Durable:** content-address each hop into an append-only hash-chained journal so
  who-acted-for-whom survives token expiry (B14).
- **Linked:** the Work Context `delegation_id` references a real
  `delegation_grants.id`, and `audit_events` carries a chain reference — approval +
  capability hop + action become one provable graph (I7).
- **Revocable:** the `authorization_revision` epoch handles standing/bulk revocation;
  per-hop revocation IDs handle surgical single-chain revocation; short TTL is the
  backstop.
- **Interoperable:** map `actor_chain` → RFC 8693 nested `act` (prior hops are
  provenance; only the current actor authorizes).

Sub-decisions:
- **Home:** the durable chain lives in **Accounts** (the authorization/audit
  record-of-truth); it references product task ids but task/session *lifecycle* stays
  product-owned.
- **Owner model:** exactly **one human owner** per agent, enforced at issuance
  (owner must equal the agent's registered `created_by`). Deep chains stay allowed
  (limit 16). Team/agent-owned agents and DID portability are deferred.

## Consequences
- Full after-the-fact accountability for autonomous/agent actions (A4, I7).
- A new durable store + revocation list to operate; TTL remains the backstop.
- Enforcing the `created_by` edge can reject previously-accepted issuance paths —
  a deliberate tightening.

## Why (not) alternatives
Adopting Biscuit or UCAN wholesale would lose the owner/tenant/task structural
guarantees already built; keeping the chain ephemeral and logging only at use-time
gives no durable audit and no transitive revocation. Borrowing their mechanisms
(content-addressing, per-hop revocation) onto the existing chain keeps the strong
guarantees and closes the record-keeping gap.
