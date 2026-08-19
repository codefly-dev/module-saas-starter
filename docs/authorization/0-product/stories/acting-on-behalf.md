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
- 🟡 **Proposed (#177 — to review):** v1 = **single human owner** per agent. An agent is owned and authorized by exactly one human; team-owned agents and agents-owning-agents are deferred. This keeps the attenuation anchor immutable (owner/tenant/task, I3) and every autonomous action traceable to one accountable human (B3/I7). Multi-owner reopens only when a real flow needs it. → RFC-0003.

### A2 · The agent can't escalate down a chain
**As an** Org Owner, **I want** any sub-agent or child task to do at most what the agent could, **so that** delegation can't accumulate privilege.
- Acceptance: each hop only narrows authority, rejected at issue *and* use (B11); owner, tenant, and task are immutable down the chain.
- 🟡 **Proposed (#177 — to review):** real flows need **2–3 hops** (human → orchestrator agent → tool sub-agent); the existing **limit of 16 stays** as ample headroom — no change. Depth is not the constraint; durability + revocation are (A4, RFC-0003).

### A3 · Human-approved elevation (break-glass)
**As a** Member, **I want** an agent to request a one-time elevation I approve, **so that** it can do something sensitive with a human in the loop, once.
- Acceptance: agent requests with a justification; human approves; a single-use, short-lived authority is issued recording who approved and why (B12); it can't be reused or extended.
- 🟡 **Proposed (#177 — to review):** **no standing auto-approve** for break-glass / high-risk elevation in v1 — each elevation is single-use with a human in the loop (that is the whole point of B12). A *time-boxed scoped grant* (a narrow standing authority the owner sets deliberately) is a different, allowed mechanism; it is not "auto-approve the sensitive thing."

### A4 · Prove who acted for whom, later
**As a** Platform Operator / auditor, **I want** to reconstruct after the fact that "agent X did Y for Sarah under grant Z," **so that** every autonomous action is accountable.
- Acceptance: the acting principal **and** the on-behalf-of principal (and the approving grant) are recoverable from a durable record, not only while a token is live (B14); the record links approval + capability hop + action (today three disconnected primitives — the gap RFC-0003 addresses).
- 🟡 **Proposed (#177 — to review):** the durable actor-chain lives in **Accounts.** It is the authorization/audit record-of-truth — attenuation, issuance, and `audit_events` are already Accounts-owned, and accountability must not depend on a product being installed. The journal is **self-contained**: each hop is a content-addressed, immutable record holding its own copy of the chain facts (that is what durability, B14, means), so a product task id on a hop is a **soft pointer**, not a cross-service FK. Deleting or archiving a product task therefore never orphans accountability — the journal still answers "who acted for whom under grant Z," it just can't re-hydrate the live task. Retention is the journal's own policy, independent of the product's task lifecycle. → RFC-0003.

### A5 · Service calls carry both identities
**As a** platform engineer, **I want** a service calling another on a user's behalf to carry both "which service" and "which user," **so that** the callee authorizes and audits fully.
- Acceptance: the downstream service sees the end user *and* the calling service (B12); only the current actor's authority drives the decision, prior hops are provenance (RFC 8693 `act`).
- 🟡 **Proposed (#177 — to review):** the callee **re-checks** — it authorizes against the *current* actor's attenuated authority, never trusting the caller's decision. Prior hops are provenance only (RFC 8693 `act`); being internal grants no ambient trust (I5), and the decision fails closed (I2).
