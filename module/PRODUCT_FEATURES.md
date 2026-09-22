# Optional product screens and entitlement enforcement

Subscription management, Single Sign-On, and Entitlements are hidden by default. Enable each
independently in the frontend's build/development environment:

```dotenv
NEXT_PUBLIC_ENABLE_SUBSCRIPTIONS=true
NEXT_PUBLIC_ENABLE_SSO=true
NEXT_PUBLIC_ENABLE_ENTITLEMENTS=true
```

Restart development after changing these values. Production Next.js public
environment values are bundled at build time: rebuild/redeploy to change them.
Only the literal `true` enables a feature. These switches contain no secrets.

Disabled screens are removed from the shared navigation selector (sidebar,
command palette, and plugin registry). Direct visits to `/admin/billing`,
`/admin/sso`, and `/admin/entitlements` show an explanatory disabled state without mounting their API-backed
components. Enabling a screen does not configure Stripe or an identity provider;
those integrations still require their provider configuration.

These are presentation switches, not backend authorization controls. They do not
turn off existing API endpoints, disable existing SSO connections, or waive quotas.
Entitlements stays hidden while enforcement is incomplete; enabling its screen
does not add the missing backend checks described below.

## Enforcement verified in the September 2026 local audit

- **Seats:** direct organization-member admission and pending invitation
  reservations enforce cardinality quotas inside a tenant transaction.
- **API keys:** creation checks active-key capacity in the same transaction as
  insertion. Concurrent admissions cannot both claim the final slot.
- **Metered usage:** `ConsumeUsage` enforces the configured meter limit with
  idempotency and concurrent quota serialization. This is not evidence that
  every HTTP request consumes usage: the audited gateway does not automatically
  call this operation for every API request.
- **SSO and audit-log feature flags:** the audited SSO setup and audit read paths
  do not consult the corresponding boolean entitlement. A displayed disabled
  limit therefore must not be represented as enforced feature denial.

Regression evidence: `pkg/business/cardinality_quota_test.go` and
`pkg/infra/postgres_usage_test.go` in the accounts service. Hiding optional screens
does not resolve the missing feature-entitlement gates. Do not claim universal
plan enforcement until those gates and their allow/deny tests exist.

## Teams

Team names open `/admin/teams/{teamId}`. Organization members can view the roster;
team admins/owners, organization admins/owners, and platform super admins can
manage it. The details screen distinguishes actual team membership from elevated
organization/platform access. Team roles are separate from organization roles.
Backend authorization remains authoritative; failed mutations leave a visible
error and preserve the existing roster.
