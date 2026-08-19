# User stories — API keys & programmatic access (`KEY`)

> Non-interactive credentials for scripts, integrations, and machine callers.
> API keys exist today (`cfly_sk_…`, Vault-hashed, scoped).

### KEY-1 · Create a scoped API key
**As a** developer, **I want** to create an API key limited to specific capabilities, **so that** a script only does what it needs.
- Acceptance: key carries scopes (`resource:action`, wildcards); shown once; hashed at rest.
- ❓ Who can create keys (any member, or admin)? ❓ Scope ceiling = creator's own rights?

### KEY-2 · Key inherits ≤ creator's access
**As a** security lead, **I want** a key to never exceed its creator's permissions, **so that** keys can't escalate.
- Acceptance: a key's effective access is the intersection of its scopes and the owner's current rights.
- ❓ Bound to owner's *current* rights (shrinks if they're demoted) or frozen at creation? (today: key dies if owner leaves org)

### KEY-3 · Key lifecycle: expire & rotate
**As a** developer, **I want** keys to expire and be rotatable, **so that** long-lived secrets don't linger.
- Acceptance: optional expiry; rotate = new key + grace window; revoke instantly.
- ❓ Force max lifetime? ❓ Rotation grace period?

### KEY-4 · Revoke a key immediately
**As an** Org Admin, **I want** to revoke any key instantly, **so that** a leaked secret is contained.
- Acceptance: revocation blocks the key at once.
- ❓ Admin can revoke others' keys? ❓ Auto-revoke on owner offboarding (yes)?

### KEY-5 · Org-owned vs user-owned keys
**As an** Org Admin, **I want** some keys tied to the org (service accounts) rather than a person, **so that** integrations survive staff changes.
- Acceptance: a key may belong to a service principal, not a human.
- ❓ Do we have org/service-account keys, or only user keys? ❓ Who manages service-account keys?

### KEY-6 · Scope enforcement everywhere
**As a** platform team, **I want** key scopes enforced on all endpoints, **so that** there are no unchecked backdoors.
- Acceptance: `requireScope` on every relevant RPC (today: a starter subset — a known gap).
- ❓ Commit to full-surface scope enforcement? Priority order?

### KEY-7 · Per-key rate limits
**As a** platform team, **I want** each key rate-limited, **so that** one integration can't exhaust the API.
- Acceptance: per-key budgets (exists); 429 with retry info.
- ❓ Default limits? Per-plan? Configurable per key?

### KEY-8 · Audit key usage
**As an** auditor, **I want** to see what each key did, **so that** machine access is accountable.
- Acceptance: actions attributable to the key (actor_type = api_key today).
- ❓ Usage analytics per key? Last-used surfaced?

### KEY-9 · Restrict a key by network/origin
**As a** security lead, **I want** to bind a key to an IP range or origin, **so that** a stolen key is useless elsewhere.
- Acceptance: optional network/origin constraints (ABAC-ish, `COND`).
- ❓ In scope? ❓ IP allowlist, mTLS, or both?
