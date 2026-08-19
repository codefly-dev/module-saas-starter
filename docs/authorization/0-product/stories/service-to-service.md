# User stories — service-to-service (`SVC`)

> One backend service calling another, sometimes on an end user's behalf.
> Today: shared internal token + gateway token. Rule: [I5](../../1-spec/invariants.md).

### SVC-1 · Authenticate a service call
**As a** platform engineer, **I want** internal calls to be authenticated, **so that** only trusted services reach internal APIs.
- Acceptance: internal credential required for `INTERNAL` RPCs; tenant transport refuses them.
- ❓ Keep shared internal token, or per-service identity (`kid`/SPIFFE)? (research: rotate + per-service `kid` now)

### SVC-2 · Carry the end user through a service hop
**As a** platform engineer, **I want** a service calling another on a user's behalf to carry both identities, **so that** the callee authorizes for the user, not the service.
- Acceptance: on-behalf-of propagation (RFC 8693 `act`) — end user + calling service both visible.
- ❓ Adopt `act` claim? ❓ Does the callee re-check the user's rights, or trust the caller's decision?

### SVC-3 · Least-privilege per service
**As a** security lead, **I want** each service to have only the internal capabilities it needs, **so that** a compromised service has a small blast radius.
- Acceptance: per-service authority, not "any service can call anything."
- ❓ Do we scope internal RPCs per calling service? ❓ How declared?

### SVC-4 · Services can't impersonate users freely
**As a** security lead, **I want** a service unable to act as an arbitrary user without authorization, **so that** internal access isn't an oracle.
- Acceptance: acting-as requires explicit authority (`may_act`); not implicit from being internal (I5).
- ❓ Which services may act for users, for which actions?

### SVC-5 · Worker/background jobs authority
**As a** platform engineer, **I want** background workers (billing, webhooks, exports) to have exactly their needed data authority, **so that** cross-tenant workers are bounded.
- Acceptance: workers use dedicated DB roles (exists: `app_billing_worker`, etc.); bypass is explicit + audited.
- ❓ Any new worker types? ❓ How is a worker's on-behalf-of (if any) recorded?

### SVC-6 · Rotate service credentials without downtime
**As a** platform engineer, **I want** to rotate internal credentials with overlapping validity, **so that** rotation doesn't cause outages.
- Acceptance: multiple valid keys during rotation (`kid`).
- ❓ Rotation cadence? Automated?

### SVC-7 · Product/plugin services get scoped internal access
**As a** product team, **I want** an installed product service to reach only the accounts capabilities it's granted, **so that** plugins can't overreach.
- Acceptance: plugin/product services get bounded internal capability (deployment topology already restricts this).
- ❓ How does a product declare needed internal capabilities? ❓ Review/approval of that grant?
