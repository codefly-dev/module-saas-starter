# saas-starter

The multi-tenant SaaS host: identity, authority, and the shared operational
spine that other modules compose onto. It ships as an immutable Codefly module
package — downstream workspaces **compose** it, they never fork it.

## Functional contract

The organization handbook carries this module's functional page,
`modules/saas-starter.md`: the capabilities in user-facing terms, and the
`HOST-*` user stories that define what "working" means. **The handbook owns the
*what*; this README owns the *how*.**

Neither side is free to drift from the other. The handbook renders its
Interface block from this repository's own generated catalog
(`module/services/accounts/generated/service-catalog.json`) at a pinned
release, and CI here fails when a story on that page has no acceptance test
(`TestStory_HOST_…`) or a test names a story the page does not carry.

## What it owns

Every capability below is an operation in the generated catalog; the catalog,
not this list, is the enumeration.

- **Identity** — sign-in through an external identity provider, sessions and
  refresh, multi-factor authentication (TOTP and WebAuthn), per-organization
  SSO, API keys, and a person's own profile, linked identities, and
  preferences.
- **Tenancy** — organizations, memberships, teams, invitations, onboarding
  progress, and the waitlist that gates signup.
- **Authorization** — roles and permissions with wildcards and team
  inheritance, scope grants and record shares, delegation between principals,
  non-human (agent and installation) principals, signed Work Contexts that
  carry who is calling for which tenant, and the installation of a solution
  into an organization.
- **Jobs** — a leased, at-least-once queue: modules enqueue, claim, heartbeat,
  acknowledge, or reject work; operators read queue health and replay what was
  dead-lettered.
- **Audit** — one append-only spine per tenant. Every event carries its actor,
  resource, and solution scope, so a tenant-wide and a solution-wide question
  are both queries over the same rows; the log can be searched, aggregated, and
  exported.
- **Approvals** — a module can gate its work on a human decision and resume
  when the decision is made.
- **Notifications** — delivery to a subject through the categories that subject
  has configured, plus the reading, counting, and clearing of their inbox.
- **Data sources** — connect an external source, pull its contents, and hand
  the resulting changes to the consuming module.
- **Dashboards** — user-owned dashboards built from a validated spec, kept
  private or shared with the organization.
- **Billing** — the public plan catalog, an organization's invoices, and the
  provider's billing portal.
- **Entitlements and usage** — plan-derived entitlements with per-organization
  overrides, metered usage, and quota consumption.
- **Webhooks** — outbound subscriptions with delivery history, replay, secret
  rotation, and a test delivery.
- **Platform administration** — platform roles, user search and suspension,
  session revocation, impersonation, and feature-flag inventory.
- **Introspection** — the service describes its own operations, permissions,
  protected tables, and scopes at runtime.

Beyond the API, the module ships the authenticated product frontend (whose
routes and navigation modules contribute to), a separately deployable public
marketing site, and the cache, store, vault, telemetry, and gateway services
the above runs on.

## What it does not own

No domain content. Documents, agents, and computation belong to the modules
composed on top; this repository carries no build-time knowledge of any of
them.

## How it is built and run

- Orientation, local runs, agent version pins, and the release tracks:
  [AGENTS.md](./AGENTS.md)
- Architecture, service graph, and capability ownership:
  [MODULE.md](./MODULE.md)
- Everything CI enforces: `codefly ci run` — see
  [RELEASE_GATES.md](./RELEASE_GATES.md)

```bash
codefly run service --fixture dev-admin
```
