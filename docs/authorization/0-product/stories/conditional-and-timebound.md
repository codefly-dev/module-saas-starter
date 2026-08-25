# User stories — conditional & time-bound access (`COND`)

> Access that depends on *attributes* or *time*, not just role: record status,
> classification, business hours, expiry, step-up. This is the ABAC surface
> (gap 4) — pressure-test which conditions are real needs.

### COND-1 · Access depends on record status
**As a** product owner, **I want** access to depend on a record's state (draft vs published, open vs closed), **so that** rights change with lifecycle.
- Acceptance: a condition on a record attribute gates the action.
- ❓ Which statuses matter? ❓ Model in authz (ABAC) or in business logic (the current stance)?

### COND-2 · Classification / need-to-know
**As a** compliance owner, **I want** highly-classified records restricted to cleared people, **so that** sensitivity is enforced.
- Acceptance: a classification attribute + a clearance check gates access.
- ❓ Do we have data classification? ❓ Levels? ❓ Hierarchy of clearances?

### COND-3 · Time-of-day / business-hours access
**As a** security lead, **I want** some access only during business hours or maintenance windows, **so that** off-hours risk is reduced.
- Acceptance: a time-window condition on a grant.
- ❓ Real requirement, or over-engineering? ❓ Whose timezone?

### COND-4 · Expiring grants (already a theme)
**As an** Org Admin, **I want** any grant/share/role to optionally expire, **so that** access doesn't outlive its purpose.
- Acceptance: `expires_at` on grants/shares/keys; auto-revoke (B9).
- ❓ Universal expiry support, or per-primitive? ❓ Warn-before-expiry?

### COND-5 · Break-glass / just-in-time elevation
**As a** Member, **I want** to request elevated access for a task with justification and approval, **so that** I'm least-privilege by default but can escalate when needed.
- Acceptance: request → approve → single-use, short-lived, audited (delegation_grants exists).
- ❓ Which actions support JIT elevation? ❓ Auto-approve patterns allowed?

### COND-6 · Step-up for risky context
**As a** security lead, **I want** a risky context (new device, new location, sensitive action) to demand re-auth/MFA, **so that** risk triggers friction.
- Acceptance: contextual step-up (device/location/action risk).
- ❓ Which signals (device, IP, geovelocity)? ❓ Adaptive or fixed rules?

### COND-7 · Attribute conditions on tools (params)
**As an** Org Owner, **I want** a tool grant constrained by parameters (deploy→staging only, refund≤$100), **so that** powerful tools are safely narrowed.
- Acceptance: parameter-level conditions on tool invocation (ties to `TOOL-4`).
- ❓ How expressive — enumerated constraints, or a predicate language? (research: bounded predicates in Go, not a general engine)

### COND-8 · Ownership-team condition
**As a** product owner, **I want** "you can edit records your team owns," **so that** access follows team ownership without per-record grants.
- Acceptance: a condition matching the caller's team to the record's owning team.
- ❓ Is "owning team" a first-class record attribute? ❓ Common enough to be a primitive?
