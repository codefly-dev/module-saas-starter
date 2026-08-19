# User stories — tools (`TOOL`)

> Authorizing agents/integrations to **use tools** (actions with side effects:
> call an API, run a job, send an email, deploy). A "tool" is a capability an
> agent invokes; tool access is authorization at the action level, often on
> behalf of a human. Ties tightly to `acting-on-behalf.md`.

### TOOL-1 · Grant an agent a specific set of tools
**As an** Org Owner, **I want** to authorize an agent to use only certain tools, **so that** it can do its job and nothing more.
- Acceptance: an agent's authority is an explicit allow-list of tools; unlisted tools are denied (B1).
- ❓ Are tools their own resource type (`tool:invoke`), or just `resource:action` pairs? ❓ Allow-list per agent, per task, or per grant?

### TOOL-2 · A tool inherits the user's own limits
**As an** Org Owner, **I want** an agent using a tool on my behalf to be unable to do more than I could, **so that** delegation can't escalate.
- Acceptance: tool invocation is bounded by the on-behalf-of chain's attenuated authority (B11).
- ❓ Does every tool call resolve against the *delegated* authority, not the agent's own?

### TOOL-3 · Dangerous tools require approval (human-in-the-loop)
**As a** Member, **I want** high-risk tools (delete prod, move money, email customers) to need my one-time approval, **so that** an agent can't do them unattended.
- Acceptance: risk-tiered tools trigger a break-glass approval; single-use, justified, audited (ties to `delegation_grants`).
- ❓ Who defines tool risk tiers? ❓ Approver = owner, or a designated role? ❓ Standing pre-approval ("auto-approve X for 1h") allowed?

### TOOL-4 · Per-tool scope/parameters
**As an** Org Admin, **I want** to authorize a tool only for certain targets (deploy tool, but only to staging), **so that** a broad tool is safely narrowed.
- Acceptance: a tool grant can carry parameter constraints (env=staging, repo=X).
- ❓ Do we support parameter-level constraints (this is ABAC-ish, `COND`)? ❓ Or is tool access all-or-nothing per tool?

### TOOL-5 · Rate/quantity limits on tool use
**As an** Org Owner, **I want** to cap how often/much an agent can use a tool, **so that** a runaway agent is bounded.
- Acceptance: max-uses / rate limits per grant (delegation grants already have max_uses).
- ❓ Per-grant caps, per-tool caps, or both? ❓ What happens at the cap (block, re-approve)?

### TOOL-6 · Time-boxed tool access
**As an** Org Owner, **I want** to grant an agent a tool for a task/window only, **so that** access ends automatically.
- Acceptance: tool grants expire (B9); short-lived by default.
- ❓ Default TTL? ❓ Renewable without re-approval?

### TOOL-7 · Audit every tool invocation
**As an** auditor, **I want** every tool call recorded with who/for-whom/what-args, **so that** autonomous actions are accountable.
- Acceptance: each invocation logs acting agent, on-behalf-of principal, tool, args (redacted as needed), outcome (B14, `AUD`).
- ❓ Log args (sensitive)? Redaction policy? ❓ Retention?

### TOOL-8 · Revoke a tool grant instantly
**As an** Org Owner, **I want** to pull an agent's access to a tool immediately, **so that** I can stop misbehavior.
- Acceptance: revocation takes effect fast (B13); in-flight capability stops.
- ❓ Instant vs next-token? (ties to chain revocation, RFC-0003)

### TOOL-9 · Tools that call other services on the user's behalf
**As a** platform engineer, **I want** a tool that calls a downstream service to carry both the agent and the end-user identity, **so that** the service authorizes correctly.
- Acceptance: on-behalf-of propagation (RFC 8693 `act`) through the tool call (`SVC`).
- ❓ Which tools cross service boundaries? ❓ Does the callee re-check, or trust the caller?

### TOOL-10 · Discover which tools an agent may use
**As an** Org Admin, **I want** to see an agent's current tool authority, **so that** I can review and prune it.
- Acceptance: list an agent's granted tools, scopes, limits, expiries.
- ❓ Surface in an agent-management UI? ❓ Include inherited/delegated tools down a chain?
