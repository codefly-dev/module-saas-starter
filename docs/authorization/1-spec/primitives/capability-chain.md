# Primitive — capability chain (on-behalf-of)

**What it is.** A short-lived, signed **capability** (Work Context) carrying an
**actor chain**: the ordered list of who is acting on whose behalf. Each hop
appends one actor with its own granted scopes; every hop **attenuates**.

**Status: largely built.** This is the strongest existing primitive. Enforced
today: monotonic attenuation at sign *and* verify (I3), structural immutability of
owner/tenant/task, revision-gated issuance that fails closed on stale state (I4),
and a single-hop human-approves-agent flow (`delegation_grants`) that mints a
caveated, single-use token.

**Contract**
- Delegated authority ≤ delegator, transitively (B11, I3).
- On-behalf-of is provable: the acting *and* authorizing principals are carried
  (B12, I7).
- Authority reflects current state (B13, I4).

**Serves stories:** [acting-on-behalf](../../0-product/stories/acting-on-behalf.md) A1–A5.

**The gaps (not the mechanism — the record-keeping):**
- The chain is **ephemeral** — no durable record after token expiry (B14).
- `delegation_grants` is **single-hop**; the two primitives aren't **linked**.
- Unified audit lacks an **on-behalf-of** field beyond impersonation.
- The `created_by` (owner authorized this agent) edge isn't checked at issuance.

**Realized by:** RFC-0003 (durable, linked, revocable, interoperable actor chain).
