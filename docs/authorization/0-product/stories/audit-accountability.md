# User stories — audit & accountability (`AUD`)

> Recording who did what, on whose behalf, and being able to answer it later.
> Rule: [B3, B12, B14](../behaviors.md), invariant [I7](../../1-spec/invariants.md).

### AUD-1 · Every sensitive action is logged
**As an** auditor, **I want** an immutable record of security-relevant actions, **so that** we can investigate and comply.
- Acceptance: audited events for auth, grants, shares, deletes, admin/operator actions.
- ❓ What's the event catalog? ❓ Immutable/append-only store? Retention?

### AUD-2 · Record the acting principal
**As an** auditor, **I want** each action tagged with who did it, **so that** it's attributable.
- Acceptance: actor id + type (user/api_key/service/agent) on every event (exists).
- ❓ Consistent actor model across all surfaces?

### AUD-3 · Record on-behalf-of (the second party)
**As an** auditor, **I want** to see when A acted for B (agent for human, operator impersonating), **so that** delegated actions are accountable.
- Acceptance: events carry the on-behalf-of / authorizing principal, not just the actor (today: only impersonation is modeled — a gap, RFC-0003).
- ❓ Add a `grantor`/`subject_principal`/`delegation` field to the audit stream? ❓ Emit the actor-chain?

### AUD-4 · Reconstruct a full delegation chain after the fact
**As an** incident responder, **I want** to trace "agent X acted for Sarah under approval Z," **so that** I understand what happened, **even after** tokens expired.
- Acceptance: durable, linked record joining approval + capability hop + action (RFC-0003).
- ❓ Where does the durable chain live (accounts vs product)?

### AUD-5 · Access review / recertification
**As a** compliance owner, **I want** periodic "who has access to what" reviews, **so that** access stays least-privilege.
- Acceptance: exportable effective-access report per user/team/resource; attest & revoke.
- ❓ In scope for the starter, or product-owned? ❓ Cadence, format?

### AUD-6 · Tamper-evident logs
**As a** security lead, **I want** audit logs I can trust weren't altered, **so that** they hold up in review.
- Acceptance: hash-chained / append-only; export to external SIEM.
- ❓ Hash-chaining in v1? ❓ SIEM/export targets?

### AUD-7 · Query "who can access this record?"
**As an** Org Admin, **I want** to ask who currently has access to a given record and why, **so that** I can spot over-sharing.
- Acceptance: reverse lookup: record → subjects + the grant/share/scope path granting it.
- ❓ Real-time query? ❓ Include inherited/hierarchical paths?

### AUD-8 · Query "what can this person access?"
**As an** Org Admin, **I want** to ask what a given person can reach, **so that** I can review before/after a role change.
- Acceptance: forward lookup: subject → resources + effective role.
- ❓ Bounded/paginated for large orgs? ❓ Simulate "if I grant X, what changes?"
