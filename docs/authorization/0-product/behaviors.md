# Behaviors — the rules, in plain language

> **Strawman for review.** These are the observable rules the system must obey,
> written so a non-engineer can agree with them and an engineer can turn each
> into a test. They are the contract the spec and implementation must satisfy.
> Each rule has an ID so stories and tests can cite it.

## Foundational

- **B1 · Default deny.** If nothing explicitly grants access, the answer is no.
  A caller never learns that a record they can't see even exists.
- **B2 · Tenant isolation is absolute.** No grant, share, bug, or agent can ever
  expose one tenant's data to another. This is the floor everything else sits on
  and it is never negotiable. *(Enforced physically by RLS — see `1-spec/invariants.md`.)*
- **B3 · Every decision is attributable.** For any allow, we can say who acted,
  under what grant, and — if on behalf of someone — for whom.

## Hierarchical / layered access

- **B4 · Grant at a node, inherit down the subtree.** Access granted at a scope
  node (e.g. `foundation.solution_x`) applies to everything beneath it
  (`…solution_x.customer_7.individual_9`), without re-granting at each level.
- **B5 · Most-specific-visible-wins.** When the same logical thing is reachable
  at several scope levels, the caller sees the single most-specific one they're
  entitled to — not duplicates, not the broadest.
- **B6 · A visibility filter runs before content loads.** We decide what a caller
  may see over *metadata* first; we never load content and then hope to hide it.

## Sharing

- **B7 · Sharing is additive.** Sharing a record with someone *adds* their
  access; it never silently removes anyone else's.
- **B8 · Widen at the leaf, don't secretly narrow.** A per-record share can grant
  more than the hierarchy gives; reducing access is done deliberately at the
  scope level, never as a hidden per-record exception. *(Per-record denials are
  explicitly out of scope for v1 — see `stories/record-sharing.md`.)*
- **B9 · Shares can expire.** A share may carry an expiry; after it, access is
  gone with no action required.
- **B10 · Highest grant wins.** When a caller reaches a record by several paths
  (their scope + a direct share + a team share), they get the strongest role
  among them.

## Acting on behalf of

- **B11 · Delegated authority never exceeds the delegator.** An agent or service
  acting for someone can do *at most* what that someone could — every delegation
  hop can only narrow, never widen. *(This is already enforced by the capability
  chain's attenuation — see `1-spec/primitives/capability-chain.md`.)*
- **B12 · On-behalf-of is provable, not implied.** When A acts for B, the action
  carries both identities; "B's agent did X" is a fact we can show, not an
  inference.
- **B13 · Authority reflects current state.** Revoke a role or a membership and
  in-flight delegated authority stops working — stale authority fails closed.
- **B14 · The chain is auditable after the fact.** Who-acted-for-whom is
  recoverable later, not only while a token is live. *(This is the durability gap
  today — see RFC-0003.)*

## Field-level

- **B15 · A field may be more sensitive than its record.** Being able to read a
  record does not automatically mean reading every field of it; some fields are
  gated by an additional permission and are blanked (not errored) when denied.

## What "good" looks like (acceptance themes)

Every story's acceptance criteria should be expressible as one or more of these
rules firing. If a proposed behavior can't be traced to a rule here (or a new
rule we add), it's a signal the behavior isn't agreed yet.

## Resolved (#177)

- **B5 vs B10 — confirmed, cannot conflict.** They are different axes: B5
  (most-specific-*visible*-wins) picks *which node* a record resolves at; B10
  (highest-grant-wins) picks *how strong a role* holds among all grants reaching
  it. Resolution: most-specific node first (B5), then strongest role there (B10).
  (See [`stories/hierarchical-access.md`](stories/hierarchical-access.md) H2.)
- **B8 — no per-record revocation/deny in v1.** Shares and grants are additive
  only; the complexity of a deny-that-overrides-an-inherited-allow isn't justified.
  The "deny beats allow" rule stays latent (PERM-7) if we ever add denies.
- **B13 — "current" is dual-path.** DB-backed decisions are immediate (sessions
  revoked in the same transaction on role/membership change); token-claim-backed
  reads are bounded by the access-token TTL (~15 min), never longer. Gate anything
  that must revoke *instantly* on the DB path. This is invariant
  [I4](../1-spec/invariants.md) — that is the SLA we promise.
