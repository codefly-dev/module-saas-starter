# Implementation roadmap

> Downstream of accepted proposals. Phasing is sequenced so each phase ships value
> and nothing forces a later rewrite. The through-line: **extend the SOTA-grade
> foundations (RLS floor, capability chain), don't replace them.** Full narrative +
> swimlane diagram are in the published reference artifact.

## Phases

### P0 — Harden what exists (now, no new features)
- Verify `SET LOCAL` + **transaction-mode** connection pooling (top RLS footgun).
- Confirm `org_id`-leading indexes on tenant tables.
- Rotate the internal service token; give each service its own signing `kid`;
  ensure the minted JWT carries the end-user identity end-to-end.

### P1 — Foundations (build in-repo)
- **Hierarchical scope** (RFC-0001, in review): `scope_nodes` + `scope_grants` +
  `record_scopes` (id→scope binding) + `CheckAccess` ancestor-match, resolved
  most-specific-wins; scope proven from the record's id; RLS stays the floor.
- **Chain durability** (RFC-0003, part 1): content-address + hash-chain journal;
  link `delegation_grants` ↔ `actor_chain`.
- **On-behalf-of interop** (RFC-0003): map `actor_chain` → RFC 8693 `act`.
- **ABAC** (if needed): closed enum of attribute predicates in Go.

### P2 — Features (build in-repo)
- **Per-record sharing** (RFC-0002): `record_shares` overlay; inherited-or-explicit,
  highest-wins.
- **Typed scope registry** hardening (RFC-0001): validate paths on write.
- **Chain revocation** (RFC-0003, part 2): per-hop revocation IDs on the epoch
  counter.
- **Field-level** — **proposed cut from v1** (#177, to review): no real field needs
  record-read-but-field-hidden today; split RPCs by visibility tier instead. Reopens
  as a redaction interceptor + annotations only if a concrete field appears.
- **ABAC** predicate→SQL compiler over RLS (if the Go enum proves useful).

### P3 — At scale / only at a real inflection (adopt / evaluate)
- **ReBAC PDP** (SpiceDB/OpenFGA/WorkOS FGA) if nested groups/inheritance outgrow
  SQL — RLS still the floor, consistency via stored tokens.
- **cedar-go** for ABAC past ~15–20 rules, with Cedar Analysis in CI.
- **SPIFFE/SPIRE** mTLS transport identity *under* the JWT, at multi-cluster scale.
- **Third-party caveats / DID portability** for the chain, only if cross-service /
  cross-org delegation demands it.

## Invariant across all phases
The RLS `org_id` floor stays fail-closed; every new layer sits above it
([ADR-0001](../9-reference/decisions/0001-rls-stays-the-floor.md)).

## Spikes
- [`spikes/ltree-record-shares.md`](spikes/ltree-record-shares.md) — concrete
  migration + `CheckAccess` sketch for RFC-0001 / RFC-0002.
- _(planned)_ durable actor-chain journal schema for RFC-0003.
