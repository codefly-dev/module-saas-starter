# User stories — external & guest access (`GUEST`)

> People outside the org: clients, auditors, contractors, partners. They see
> nothing by default and exist only through explicit, usually time-boxed access.
> Tension to watch: tenant isolation ([I1, B2](../../1-spec/invariants.md)) is absolute — external
> access must be a sanctioned, narrow path, never a hole in the floor.

### GUEST-1 · Invite an external person to one record/subtree
**As a** Member, **I want** to give an outside client access to a specific record or area, **so that** we collaborate without adding them to my org.
- Acceptance: guest gets exactly the shared scope, nothing else (B1); usually read-only + expiring.
- ❓ Is cross-org guest access in v1, or intra-org first? ❓ Read-only only, or writes too?

### GUEST-2 · Guest identity & sign-in
**As an** external guest, **I want** a simple, secure way to access what's shared, **so that** I don't need a full account.
- Acceptance: guest auth (magic link / their own SSO / lightweight account).
- ❓ Magic link, guest SSO, or full account? ❓ MFA for guests?

### GUEST-3 · Time-boxed by default
**As an** Org Admin, **I want** external access to expire automatically, **so that** stale external access can't linger.
- Acceptance: guest shares carry an expiry (B9); default short.
- ❓ Max external-access lifetime? ❓ Renewal flow?

### GUEST-4 · Guests never see internal fields/metadata
**As an** Org Admin, **I want** internal-only fields hidden from guests, **so that** sharing a record doesn't leak internal notes.
- Acceptance: guests see a reduced projection (ties to `FIELD`).
- ❓ Which fields are "internal"? ❓ Is field-hiding required for guests specifically?

### GUEST-5 · Guests can't re-share
**As an** Org Admin, **I want** guests unable to pass their access to others, **so that** access doesn't spread uncontrolled.
- Acceptance: guests have no share capability by default.
- ❓ Ever allow guest re-share (with limits)?

### GUEST-6 · Revoke a guest instantly
**As a** Member, **I want** to cut off an external partner immediately, **so that** I control external exposure.
- Acceptance: revoke ends access at once (B13).
- ❓ Notify the guest? ❓ Bulk-revoke all guests on a record?

### GUEST-7 · Audit all external access
**As a** compliance owner, **I want** external access tightly audited, **so that** we know exactly what left the building.
- Acceptance: every guest view/action logged; exports flagged (`AUD`).
- ❓ Extra scrutiny/alerts for external access vs internal?
