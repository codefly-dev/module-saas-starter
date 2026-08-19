# User stories — acting on behalf of (agents & services)

> **Strawman for review.** Capability: an agent or service acts for a human (or a
> user), provably and within bounds. Rules cited:
> [B11, B12, B13, B14](../behaviors.md). Personas:
> [Agent, Service, Platform Operator, Owner](../personas.md).
>
> **Note:** the hard part here already exists — the capability chain enforces
> attenuation at sign *and* verify (see `1-spec/primitives/capability-chain.md`).
> These stories are mostly about **authoring, durability, and audit**, not the
> core mechanism.

## Story A1 — Authorize an agent for a task

**As an** Org Owner, **I want** to authorize an agent to act for me on a specific
task with a specific slice of my authority, **so that** it can automate work
without becoming a standing super-user.

**Acceptance criteria**
- I authorize an agent with a subset of my scopes; the agent can do exactly that
  subset (B11) and no more, even where I could do more.
- The authorization is short-lived / task-bound, not permanent.
- I can see and revoke it.

## Story A2 — The agent can't escalate down a chain

**As an** Org Owner, **I want** any sub-agent or child task the agent spawns to
be able to do at most what the agent could, **so that** delegation can't
accumulate privilege.

**Acceptance criteria**
- Each hop in the delegation chain only narrows authority (B11); an attempt to
  widen is rejected at issue *and* at use.
- Owner, tenant, and task can't be changed anywhere down the chain.

## Story A3 — Human-approved elevation (break-glass)

**As a** Member, **I want** an agent to be able to request a one-time elevation
that I approve, **so that** it can do something sensitive with a human in the
loop, once.

**Acceptance criteria**
- The agent requests authority with a justification; a human approves; a
  single-use, short-lived authority is issued that records *who approved and why*
  (B12).
- The elevation cannot be reused or extended.

## Story A4 — Prove who acted for whom, later

**As a** Platform Operator (or auditor), **I want** to reconstruct, after the
fact, that "agent X did Y on behalf of Sarah under grant Z," **so that** every
autonomous action is accountable.

**Acceptance criteria**
- For any action, the acting principal **and** the on-behalf-of principal (and
  the approving grant, if any) are recoverable from a durable record (B14) — not
  only while a token is live.
- The record links the approval, the capability hop, and the resulting action
  (today these are three disconnected primitives — this is the gap RFC-0003
  addresses).

## Story A5 — Service calls carry both identities

**As a** platform engineer, **I want** a service calling another service on a
user's behalf to carry both "which service" and "which user," **so that** the
callee can authorize correctly and audit fully.

**Acceptance criteria**
- A downstream service sees the end user *and* the calling service (B12).
- Only the current actor's authority is used for the decision; prior hops are
  provenance (RFC 8693 `act` semantics).

## Open questions
- **Durability home:** does the durable actor-chain record live in Accounts, or
  in the product (the roadmap says task/session lifecycle is product-owned)?
  RFC-0003 must answer this.
- Chain depth in practice: A1–A2 allow deep chains (limit 16 today). What depth
  do real product flows actually need?
- Does an agent ever act for a **team** rather than an individual?
