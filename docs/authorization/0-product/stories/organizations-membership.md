# User stories — organizations & membership (`ORG`)

> Tenancy, joining, leaving, belonging to more than one org. Rule: [B2](../behaviors.md) (tenant isolation is absolute).

### ORG-1 · Create an organization
**As a** new user, **I want** to create an org, **so that** I have a workspace and become its owner.
- Acceptance: creator becomes Owner; org is an isolated tenant from creation.
- ❓ Can one user own many orgs? ❓ Any limits (plan-gated)?

### ORG-2 · Invite someone by email
**As an** Org Admin, **I want** to invite a person by email with a role, **so that** they can join with the right access.
- Acceptance: invite carries a role (and maybe scope); accepting creates membership; unverified emails can't accept.
- ❓ Can an invite pre-assign a scope/branch, not just a role? ❓ Invite expiry? ❓ Re-invite/resend?

### ORG-3 · Accept / decline an invite
**As an** invited person, **I want** to accept or decline, **so that** I control what I join.
- Acceptance: accept → membership with the invited role; decline → nothing.
- ❓ Does accepting into org B affect my session in org A (multi-org)?

### ORG-4 · Belong to multiple orgs
**As a** consultant, **I want** to be a member of several orgs, **so that** I serve multiple clients with one login.
- Acceptance: one identity, many memberships; each org's data isolated (B2).
- ❓ Is multi-org in v1? ❓ Same email across orgs — one user or per-org identity?

### ORG-5 · Switch the active organization
**As a** multi-org user, **I want** to switch which org I'm working in, **so that** all my views/queries scope to it.
- Acceptance: switch issues a fresh token for the target membership; refresh/session preserved; only current-membership orgs selectable.
- ❓ One global switcher, or per-tab context? ❓ Default org on login?

### ORG-6 · Change a member's role
**As an** Org Admin, **I want** to change a member's role, **so that** access matches their job.
- Acceptance: takes effect promptly (B13); audited.
- ❓ Can an admin grant a role higher than their own? ❓ Owner-only for certain roles?

### ORG-7 · Remove a member (offboard)
**As an** Org Admin, **I want** to remove someone, **so that** they lose all access immediately.
- Acceptance: membership removed; sessions revoked; their grants/shares handled (see `LIFE`).
- ❓ What happens to records they owned/shared? ❓ Reassign vs orphan vs delete? (see `LIFE-*`)

### ORG-8 · Leave an organization
**As a** Member, **I want** to leave an org I no longer work with, **so that** I stop seeing its data.
- Acceptance: self-remove membership; last-owner can't leave without transfer.
- ❓ Last-owner guard rules? ❓ Cooling-off / re-join?

### ORG-9 · Transfer ownership
**As an** Org Owner, **I want** to hand ownership to someone else, **so that** the org survives my departure.
- Acceptance: explicit transfer; requires the new owner's acceptance; audited, maybe MFA.
- ❓ One owner or multiple co-owners? ❓ Step-up required?

### ORG-10 · Org-level security policy
**As an** Org Owner, **I want** to set org-wide rules (enforce MFA, allowed domains, session limits), **so that** my company's policy is enforced for everyone.
- Acceptance: policy applies to all members; violations blocked.
- ❓ Which policies are configurable per org vs platform-fixed? ❓ Who can change them?

### ORG-11 · See who's in my org and what they can do
**As an** Org Admin, **I want** a members-and-access view, **so that** I can audit who can do what.
- Acceptance: list members, roles, scopes, last active.
- ❓ How deep — show effective permissions per member, or just roles? (ties to `AUD`)
