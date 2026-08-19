# User stories — authentication (`AUTH`)

> Signing in, staying signed in, proving who you are. Personas: all humans, plus
> machines. Rules: [B2, B3](../behaviors.md), invariant [I5](../../1-spec/invariants.md).

### AUTH-1 · Sign in with company SSO
**As a** Member, **I want** to sign in with my company's identity provider (WorkOS/Okta/Google/etc.), **so that** I use one corporate login and IT keeps control.
- Acceptance: OAuth code + PKCE; provider token verified via JWKS; we mint our own token; provider token never travels past the front door.
- ❓ Which providers must ship day one? ❓ Do we support multiple IdPs per org (different departments)? ❓ Is email/password ever allowed, or SSO-only?

### AUTH-2 · Just-in-time provisioning on first SSO login
**As an** Org Owner, **I want** a new employee's account to be created automatically the first time they sign in via our SSO, **so that** I don't pre-provision everyone.
- Acceptance: first login creates the user + default membership/role per policy; unverified email may sign in but can't accept invitations.
- ❓ What default role does a JIT user get (none / viewer / configurable)? ❓ Does JIT auto-join them to an org, or land them "org-less"? ❓ Domain-based auto-join (anyone @acme.com joins Acme)?

### AUTH-3 · Multi-factor for sensitive actions
**As a** security-conscious Org, **I want** certain actions to require a recent second factor, **so that** a stolen session can't perform them.
- Acceptance: sensitive ops require recent step-up MFA; freshness window configurable; lack of enrollment isn't a bypass for the strict variant.
- ❓ Which actions require step-up (billing, deletes, sharing, role grants…)? ❓ Org-enforced MFA vs opt-in? ❓ Freshness window length?

### AUTH-4 · Stay signed in safely
**As a** Member, **I want** to stay signed in across visits without re-entering credentials, **so that** it's convenient, **but** have sessions expire sensibly.
- Acceptance: refresh-token rotation; absolute + idle expiry; reuse of a rotated token revokes the family.
- ❓ Session absolute lifetime? Idle timeout? ❓ Per-device session cap? ❓ "Remember this device"?

### AUTH-5 · See and revoke my sessions/devices
**As a** Member, **I want** to see where I'm logged in and sign out a device, **so that** I can cut off a lost laptop.
- Acceptance: list active sessions (device, last active); revoke one or all.
- ❓ Do we expose this in v1? ❓ Can an admin revoke a member's sessions?

### AUTH-6 · Immediate effect of a role/membership change
**As an** Org Admin, **I want** removing someone's access to take effect quickly, **so that** offboarding is real.
- Acceptance: role/membership change revokes affected sessions in the same transaction; in-flight tokens stop working within one refresh at most. (B13)
- ❓ Is "within one refresh cycle" acceptable, or must it be instant (token deny-list)? ❓ What's the promised SLA?

### AUTH-7 · Sign out everywhere
**As a** Member who suspects compromise, **I want** a "sign out of all devices" button, **so that** I can reset access instantly.
- Acceptance: revokes all refresh families + deny-lists live access tokens.
- ❓ Self-serve only, or admin-triggerable for a member?

### AUTH-8 · Programmatic sign-in for scripts (machine)
**As a** developer, **I want** a non-interactive credential for scripts/CI, **so that** automation authenticates without a human.
- Acceptance: API keys (see `KEY` stories) or client-credentials; scoped, revocable.
- ❓ API keys, OAuth client-credentials, or both? (see `api-keys-programmatic.md`)

### AUTH-9 · Agent sign-in (machine)
**As an** Org Owner, **I want** an autonomous agent to authenticate as its own identity, **so that** its actions are attributable to the agent, not a human's shared login.
- Acceptance: agent principals authenticate as themselves; every action attributable (B3).
- ❓ How does an agent get its credential (issued by owner, rotated how)? ❓ Ties to `acting-on-behalf.md`.

### AUTH-10 · Account recovery / lockout
**As a** Member locked out, **I want** a safe recovery path, **so that** I regain access without a support ticket, **but** attackers can't abuse it.
- Acceptance: recovery respects the IdP (SSO orgs recover via IdP); MFA backup codes exist.
- ❓ Is recovery entirely the IdP's job (SSO-only), or do we own any path? ❓ Backup-code policy?

### AUTH-11 · Impersonation for support (see also OPS)
**As a** Support Operator, **I want** to act as a user to reproduce an issue, **so that** I can help them, **with** full audit and their-or-policy consent.
- Acceptance: impersonation carries `acting-as`; every action audited as impersonated; time-boxed.
- ❓ Consent model (user opt-in vs policy)? ❓ What can't be done while impersonating (e.g. change security settings)? (see `OPS`)
