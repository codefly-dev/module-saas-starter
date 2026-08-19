# User stories — platform operator & admin (`OPS`)

> Cross-tenant operators (support, billing, super-admin) acting *above* tenants,
> by design, always audited. Platform authority is separate from org membership.

### OPS-1 · Support access to help a customer
**As a** Support Operator, **I want** scoped access to a customer's org to diagnose issues, **so that** I can help, **without** standing access to all data.
- Acceptance: platform role grants bounded cross-tenant access; every action audited.
- ❓ Standing access vs per-incident grant? ❓ What data is off-limits even to support?

### OPS-2 · Impersonate a user (see also AUTH-11)
**As a** Support Operator, **I want** to act as a user to reproduce their view, **so that** I see what they see.
- Acceptance: impersonation carries `acting-as`; time-boxed; fully audited; some ops blocked.
- ❓ Consent: user opt-in, org policy, or platform right? ❓ Blocked-while-impersonating actions (change security, delete, export)?

### OPS-3 · Tiered platform roles
**As a** platform owner, **I want** distinct operator tiers (support / billing / super-admin), **so that** operators get only what their job needs.
- Acceptance: platform roles are tiered (exists: SUPPORT/BILLING/SUPER_ADMIN).
- ❓ Exact tier capabilities? ❓ Who grants platform roles (super-admin only)?

### OPS-4 · Break-glass emergency access
**As a** platform SRE, **I want** a heavily-audited emergency access path for incidents, **so that** I can act in a crisis, **with** alarms and review.
- Acceptance: break-glass requires justification + step-up; loud audit + alert; time-boxed.
- ❓ Who can break glass? ❓ Auto-notify whom? ❓ Post-hoc review process?

### OPS-5 · Sensitive platform ops need MFA
**As a** platform owner, **I want** dangerous cross-tenant ops (GDPR delete, entitlement override) to require recent MFA, **so that** they can't happen on a stale session.
- Acceptance: step-up required (exists for some ops).
- ❓ Full list of step-up-required platform ops?

### OPS-6 · Cross-tenant actions are always attributable
**As an** auditor, **I want** every cross-tenant action tied to a named operator, **so that** platform power is accountable.
- Acceptance: operator identity + reason on every cross-tenant write (I7).
- ❓ Reason/ticket required per action? ❓ Retention of operator audit?

### OPS-7 · Operator can't silently widen their own power
**As a** platform owner, **I want** operators unable to grant themselves more platform authority, **so that** insider risk is contained.
- Acceptance: platform-role grants are super-admin-gated + audited; no self-escalation.
- ❓ Two-person rule for granting super-admin?

### OPS-8 · Customer visibility into operator access
**As an** Org Owner, **I want** to see when platform operators accessed my org, **so that** I trust the vendor.
- Acceptance: customer-facing access log of operator actions.
- ❓ Do we expose operator access to customers (transparency) in v1? ❓ Real-time notify?

### OPS-9 · Read-only auditor persona
**As a** compliance reviewer, **I want** wide read access with zero write, ever, **so that** I can audit without risk.
- Acceptance: a strictly read-only role that no path can escalate.
- ❓ Is this a distinct persona/role? Scope (one org / platform-wide)?
