# User stories — lifecycle & revocation (`LIFE`)

> Access ending cleanly: offboarding, revocation, rotation, orphaned data.
> Rule: [B13](../behaviors.md) (authority reflects current state).

### LIFE-1 · Offboard a person completely
**As an** Org Admin, **I want** removing someone to strip *all* their access at once, **so that** nothing is missed.
- Acceptance: membership, roles, scope grants, shares, keys, sessions all revoked in one action.
- ❓ Single "offboard" action across all primitives? ❓ What happens to their agents?

### LIFE-2 · Handle records an offboarded person owned
**As an** Org Admin, **I want** to decide what happens to a departing user's records, **so that** work isn't lost or orphaned.
- Acceptance: reassign, transfer to manager, or archive.
- ❓ Default policy (reassign vs orphan-to-admin)? ❓ Bulk transfer tooling?

### LIFE-3 · Revoke a single grant/share instantly
**As a** Member, **I want** to revoke access I granted, **so that** I can correct mistakes.
- Acceptance: revoke a specific grant/share; takes effect fast (B13).
- ❓ Instant vs next-refresh? ❓ Notify the person losing access?

### LIFE-4 · Cascade revocation down a delegation chain
**As an** Org Owner, **I want** revoking an agent's authority to also kill anything it delegated onward, **so that** revocation is complete.
- Acceptance: revoke an ancestor → descendants die (Biscuit-style per-hop revocation, RFC-0003).
- ❓ Cascade automatically, or revoke leaf-by-leaf? ❓ SLA?

### LIFE-5 · Rotate credentials/keys
**As a** developer, **I want** to rotate keys/secrets with a grace window, **so that** rotation is safe.
- Acceptance: overlapping validity; old credential retired.
- ❓ Forced rotation cadence? ❓ Auto-rotate service creds?

### LIFE-6 · Expire stale access automatically
**As a** security lead, **I want** unused access to expire, **so that** least-privilege holds over time.
- Acceptance: TTLs; optional "revoke if unused N days."
- ❓ Auto-expire unused grants? ❓ Warn owners first?

### LIFE-7 · Suspend vs delete
**As an** Org Admin, **I want** to temporarily suspend access without deleting, **so that** I can pause someone (leave, investigation) reversibly.
- Acceptance: suspend blocks all access but preserves grants for restore.
- ❓ Is suspend a first-class state? ❓ Difference from remove?

### LIFE-8 · Delete an org (tenant teardown)
**As an** Org Owner, **I want** to delete my org and its data, **so that** I can leave cleanly (and for GDPR).
- Acceptance: full teardown; GDPR-compliant; audited; irreversible with confirmation.
- ❓ Grace/soft-delete window? ❓ Export-before-delete? ❓ Who can trigger?

### LIFE-9 · Deactivate an agent
**As an** Org Owner, **I want** to retire an agent principal, **so that** its credentials and authority stop working.
- Acceptance: agent deactivated; credentials void; in-flight capability revoked.
- ❓ What happens to actions in progress? ❓ Keep the agent's audit history (yes)?
