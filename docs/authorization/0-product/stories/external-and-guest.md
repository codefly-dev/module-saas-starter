# User stories — external & guest access (`GUEST`)

> People outside the org: clients, auditors, contractors, partners. They see
> nothing by default and exist only through explicit, usually time-boxed access.
> Tension to watch: tenant isolation ([I1, B2](../../1-spec/invariants.md)) is absolute — external
> access must be a sanctioned, narrow path, never a hole in the floor.

> ✅ **Aspect decision (#177): the whole `GUEST` set defers to a post-v1 phase.**
> v1 per-record sharing is **intra-org only** ([`record-sharing.md`](record-sharing.md) S3,
> RFC-0002). Cross-org guest access needs a guest **identity** surface (auth, MFA,
> lifetime) that does not exist yet, and it is where tenant isolation (I1/B2) is
> most exposed — so it is a deliberate second phase, not a v1 cut corner. The
> `record_shares` primitive is built subject-kind-agnostic so guests slot in later
> without reshaping it. The shape decisions below are recorded now to steer that
> phase; they are not v1 commitments.

### GUEST-1 · Invite an external person to one record/subtree
**As a** Member, **I want** to give an outside client access to a specific record or area, **so that** we collaborate without adding them to my org.
- Acceptance: guest gets exactly the shared scope, nothing else (B1); usually read-only + expiring.
- ✅ **Decided (#177):** **intra-org first;** cross-org guest access is the deferred phase above. When it lands, guest shares default **read-only** (write is an explicit, separately-granted role on the share, never the default).

### GUEST-2 · Guest identity & sign-in
**As an** external guest, **I want** a simple, secure way to access what's shared, **so that** I don't need a full account.
- Acceptance: guest auth (magic link / their own SSO / lightweight account).
- 🔷 **Deferred (#177):** guest identity is the gating unknown for the whole aspect — resolved when the cross-org phase is scoped. Leaning: magic-link *or* the guest's own SSO (no forced full account); MFA follows the sharing org's policy.

### GUEST-3 · Time-boxed by default
**As an** Org Admin, **I want** external access to expire automatically, **so that** stale external access can't linger.
- Acceptance: guest shares carry an expiry (B9); default short.
- ✅ **Decided (#177):** external shares are **time-boxed with a mandatory expiry** (no standing external access); the `expires_at` on `record_shares` is required for cross-org subjects. Exact max lifetime + renewal flow are set with the cross-org phase.

### GUEST-4 · Guests never see internal fields/metadata
**As an** Org Admin, **I want** internal-only fields hidden from guests, **so that** sharing a record doesn't leak internal notes.
- Acceptance: guests see a reduced projection (ties to `FIELD`).
- ✅ **Decided (#177):** field-hiding is **not** built as a role-based redaction framework (see [`field-visibility.md`](field-visibility.md) — cut from v1). Guests instead receive a **narrower projection RPC / message** than internal callers; "internal vs shared" is a message-shape decision, not a per-field policy. Reassess only if the cross-org phase surfaces a concrete leaking field.

### GUEST-5 · Guests can't re-share
**As an** Org Admin, **I want** guests unable to pass their access to others, **so that** access doesn't spread uncontrolled.
- Acceptance: guests have no share capability by default.
- ✅ **Decided (#177):** guests **cannot re-share** — re-share is out of scope v1 for everyone ([`record-sharing.md`](record-sharing.md)), and permanently default-off for guests.

### GUEST-6 · Revoke a guest instantly
**As a** Member, **I want** to cut off an external partner immediately, **so that** I control external exposure.
- Acceptance: revoke ends access at once (B13).
- ✅ **Decided (#177):** revoke ends access immediately (B13); `ListShares`/`RevokeShare` cover per-share revoke, and revoking every external share on a record is a `ListShares`-then-revoke loop, not a new primitive. Guest notification follows the same deferral as S4.

### GUEST-7 · Audit all external access
**As a** compliance owner, **I want** external access tightly audited, **so that** we know exactly what left the building.
- Acceptance: every guest view/action logged; exports flagged (`AUD`).
- ✅ **Decided (#177):** yes — external access is audited with the same event model as internal (`actor_type`, on-behalf-of) plus an **external flag** on the event so exports/alerts can single it out. Alerting thresholds are a product/compliance config, set with the cross-org phase.
