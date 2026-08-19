# Personas

> **Strawman for review.** These are the actors authorization exists to serve.
> Every user story and behavior rule names one of these. React, correct, add
> the ones we're missing.

Authorization has two kinds of actor: **who is asking** (the subject) and, when
it's an agent or service, **on whose behalf**. That second axis is why the
persona list includes non-humans as first-class.

## Human personas

### Org Owner
The person who created (or owns) a tenant. Ultimate authority within their org;
can grant any role, configure the scope tree, and authorize agents. Trust: full,
within their own tenant. Never crosses tenant boundaries.
*Example:* the founder of a customer company using the product.

### Org Admin
Delegated administrator. Manages members, teams, roles, and grants within a
scope they've been given — possibly the whole org, possibly one branch of the
scope tree. Trust: high, bounded by their own scope.
*Example:* a regional lead who administers `foundation.solution_x` but not
`solution_y`.

### Member / Analyst
An ordinary user who does the actual work. Reads and writes the records their
role and scope allow. Cannot grant access to others (unless explicitly given a
sharing capability). Trust: scoped to their grants.
*Example:* an analyst who can read reports under one customer, edit none.

### External Partner / Guest
Someone outside the org who has been **shared** a specific record or subtree —
a client, an auditor, a contractor. They exist only through explicit shares,
see nothing by default, and their access is typically time-bounded. Trust:
minimal, per-share.
*Example:* an external accountant shared one quarter's records, read-only, for
30 days.

## Machine personas

### Agent Principal
A non-human principal (already first-class in the system: `principals.kind =
'agent'`, `publisher/name:version`) that a human **owns** and **authorizes** to
act. An agent never has authority of its own — it acts **on behalf of** a human,
and can never exceed what that human granted it (attenuation). Trust: derived,
always ≤ its authorizer, always attributable.
*Example:* a deployment agent Sarah authorizes to `deploy:module-a` on her
behalf, for this task only.

### Service
An internal backend service making service-to-service calls. Authenticated by
the internal credential today. When it calls on behalf of an end user, the
request must carry **both** identities (which service, and for which user).
Trust: platform-internal, but on-behalf-of must be explicit and auditable.
*Example:* the billing service reconciling a subscription for org X.

### Platform Operator
A cross-tenant human operator (support / billing / super-admin) acting through
the audited control-plane role. Can cross tenant boundaries **by design**, every
action logged, MFA-gated for sensitive ops. Distinct from Org roles: platform
authority is separate from tenant membership. Trust: high but heavily audited;
impersonation is the modeled on-behalf-of hop today.
*Example:* a support engineer impersonating a user to reproduce a bug.

## The two questions each persona raises

| Persona | "Who are you" (authN) | "On whose behalf" (delegation) |
|---|---|---|
| Owner / Admin / Member | SSO (WorkOS/OIDC) → our JWT | themselves |
| External Partner | SSO or invite/magic-link | themselves, via a share |
| Agent | agent principal credential | **a human owner** (attenuated) |
| Service | internal credential + per-service key | **an end user** (RFC 8693 `act`) |
| Platform Operator | SSO + platform role | sometimes **a user** (impersonation) |

## Proposed resolutions (#177 — to review)

- **External Partner / Guest is deferred** to a post-v1 phase. v1 per-record
  sharing is **intra-org only** (RFC-0002); cross-org guest access is gated on a
  guest-identity surface that doesn't exist yet. The persona stays in the model so
  the primitives are built subject-kind-agnostic, but no story in it is a v1
  commitment. (See [`stories/external-and-guest.md`](stories/external-and-guest.md).)
- **Read-only auditor is a distinct persona/role** — a strictly read-only role no
  path can escalate, scoped either to one org or platform-wide (OPS-9). Kept
  separate from Guest: an auditor reads a *wide* scope by policy; a guest reads a
  *narrow* shared record. Distinct trust models, distinct roles.
- **Agent has exactly one human owner** in v1 (immutable owner anchors attenuation,
  I3). Chain *depth* > 1 is fine (deep chains allowed, limit 16); what's deferred
  is an agent owned by a *team* or by *another agent*. (See
  [`stories/acting-on-behalf.md`](stories/acting-on-behalf.md) A1–A2.)
