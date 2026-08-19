# User stories — acting on behalf of (`A`)

> **Strawman for review.** An agent or service acts for a human/user, provably and
> within bounds. Rules: [B11, B12, B13, B14](../behaviors.md). Personas:
> [Agent, Service, Platform Operator, Owner](../personas.md).
>
> **Note:** the hard part exists — the capability chain enforces attenuation at
> sign *and* verify ([primitive](../../1-spec/primitives/capability-chain.md)).
> These stories are mostly about **authoring, durability, and audit**.

### A1 · Authorize an agent for a task
**As an** Org Owner, **I want** to authorize an agent to act for me on a task with a slice of my authority, **so that** it automates work without becoming a standing super-user.
- Acceptance: I authorize a subset of my scopes; the agent can do exactly that subset and no more, even where I could do more (B11); the authorization is short-lived / task-bound; I can see and revoke it.
- ❓ Does an agent ever act for a **team** rather than one individual?

### A2 · The agent can't escalate down a chain
**As an** Org Owner, **I want** any sub-agent or child task to do at most what the agent could, **so that** delegation can't accumulate privilege.
- Acceptance: each hop only narrows authority, rejected at issue *and* use (B11); owner, tenant, and task are immutable down the chain.
- ❓ Chain depth in practice — deep chains are allowed (limit 16); what depth do real flows need?

### A3 · Human-approved elevation (break-glass)
**As a** Member, **I want** an agent to request a one-time elevation I approve, **so that** it can do something sensitive with a human in the loop, once.
- Acceptance: agent requests with a justification; human approves; a single-use, short-lived authority is issued recording who approved and why (B12); it can't be reused or extended.
- ❓ Are standing auto-approve patterns ("auto-approve X for 1h") allowed?

### A4 · Prove who acted for whom, later
**As a** Platform Operator / auditor, **I want** to reconstruct after the fact that "agent X did Y for Sarah under grant Z," **so that** every autonomous action is accountable.
- Acceptance: the acting principal **and** the on-behalf-of principal (and the approving grant) are recoverable from a durable record, not only while a token is live (B14); the record links approval + capability hop + action (today three disconnected primitives — the gap RFC-0003 addresses).
- ❓ **Durability home:** does the durable actor-chain live in Accounts or the product (roadmap says task/session lifecycle is product-owned)?

### A5 · Service calls carry both identities
**As a** platform engineer, **I want** a service calling another on a user's behalf to carry both "which service" and "which user," **so that** the callee authorizes and audits fully.
- Acceptance: the downstream service sees the end user *and* the calling service (B12); only the current actor's authority drives the decision, prior hops are provenance (RFC 8693 `act`).
- ❓ Does the callee re-check the user's rights, or trust the caller's decision? (see [`service-to-service.md`](service-to-service.md))
