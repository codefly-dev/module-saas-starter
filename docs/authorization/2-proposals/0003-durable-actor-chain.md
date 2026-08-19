# RFC-0003 — Durable, linked, revocable actor chain

- **Status:** Draft
- **Created:** 2026-08-19
- **Serves:** [acting-on-behalf](../0-product/stories/acting-on-behalf.md) A2–A5; behaviors B12–B14.
- **Relates to:** the existing capability chain + `delegation_grants` (the mechanism already exists).

## Context
The capability chain already enforces attenuation correctly (I3) — the strong,
hard part is done. What's missing is record-keeping: the chain is **ephemeral**
(no record after token expiry, B14), `delegation_grants` is **single-hop** and
**unlinked** to the chain, unified audit has **no on-behalf-of field** beyond
impersonation, and "owner authorized this agent" (`created_by`) isn't checked at
issuance. This RFC makes the chain durable, linked, revocable, and interoperable
without replacing the working mechanism.

## Proposal (summary — borrows from SOTA, doesn't rip-and-replace)
1. **Durability:** content-address each hop and persist an append-only,
   hash-chained journal (UCAN-style), so who-acted-for-whom survives token expiry
   (B14). *(Home — Accounts vs. product — is an open question; roadmap says
   task/session lifecycle is product-owned.)*
2. **Linkage:** make the Work Context `delegation_id` reference a real
   `delegation_grants.id`; add a chain reference to `audit_events`. The three
   primitives (approval, capability hop, action) become one provable graph.
3. **Revocation:** Biscuit-style per-hop revocation IDs (revoke an ancestor,
   descendants die) layered on the existing `authorization_revision` epoch counter;
   short TTL bounds worst case.
4. **Interop:** map `actor_chain` → RFC 8693 nested `act` claim (nested actors are
   provenance; only the current actor authorizes) for OAuth/OIDC-facing calls.
5. **Enforce the edge:** optionally require the owner to be the agent's registered
   `created_by` at issuance (currently only stored, not checked).

## Alternatives considered
- **Adopt Biscuit or UCAN wholesale** — would lose the owner/tenant/task
  structural guarantees already built; rejected. Borrow their mechanisms instead
  (`9-reference/sota-research.md`).
- **Keep ephemeral, log at use-time only** — insufficient for after-the-fact audit
  (A4) and gives no transitive revocation.

## Consequences
- Full accountability for autonomous/agent actions (I7).
- New durable store + revocation list to operate; TTL stays the backstop.

## Open questions
- Durable-chain home: Accounts or product?
- Revision-epoch vs. per-hop revocation list boundaries (which handles what).
- DID-based principals only if cross-org portability is needed (deferred).

## Decision
_Pending review._
