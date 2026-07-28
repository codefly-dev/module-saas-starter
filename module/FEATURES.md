# saas-starter — Features

> What this module ships with, what's stubbed, and how it stacks up against
> a world-class SaaS starter. Source-of-truth checklist for both end users
> picking a starter and contributors deciding what to build next.

Last updated: 2026-07-28

---

## Architecture

Two deployment shapes, same business logic:

```
PROD                                   DEV / TEST
─────────────────────────────────────  ─────────────────────────────────────
browser → auth-sidecar → api           browser → api
          (validates JWT,                       (in-process Connect+gRPC
           strips Authorization,                 auth interceptors validate
           injects X-User-Id,                    the bearer token directly)
           X-Org-Id headers)
```

Defense-in-depth: the api validates the bearer JWT itself even when the
sidecar is in front. A misconfigured sidecar / a direct port-hit cannot
bypass auth. Implemented as `connect_auth_interceptor.go` and
`grpc_auth_interceptor.go`.

CORS lives on the api (not the sidecar) because Connect-Web preflights
every POST. Permissive in dev (any localhost origin); production puts
the sidecar in front so this code path isn't reached.

### Stack

| Layer        | Choice                                               |
|--------------|------------------------------------------------------|
| Backend      | Go, Connect-RPC + gRPC + grpc-gateway REST (one impl)|
| Frontend     | Next.js 16 (App Router) + Connect-ES + TanStack Query|
| Auth tokens  | Ed25519 JWT, OWASP refresh-token rotation            |
| Identity     | WorkOS / Auth0 / Google OIDC (prod), fixture (dev)   |
| Database     | Postgres                                             |
| Cache        | Redis (org-membership, 30s TTL, invalidation hooks)  |
| Secrets      | Vault (signing key + integrations)                   |
| Email        | Resend (prod), log-only fake (dev)                   |
| Billing      | Stripe (checkout, portal, webhook)                   |
| Observability| `wool` structured logging everywhere                 |
| Test infra   | Playwright e2e against the real stack via `withDependencies` |

Everything is orchestrated by Codefly: `codefly run service --fixture
dev-admin` resolves the module's `auth-sidecar` service entry and brings up all
seven services—Postgres + Vault + Redis + object storage + accounts + frontend
+ the public auth gateway—with seed data in one command.

---

## Auth flows

### Fixture (dev / test)

`dev-admin.yaml` seeds 4 users: Sarah Chen (super_admin), Alice (admin),
Bob (member), Carol (member). The login page detects fixture mode (no
`NEXT_PUBLIC_*_AUTHORIZE_URL` configured) and renders a one-click user
picker. Explicit `AUTH_PROVIDER=dev` or `CODEFLY__FIXTURE=dev-admin` wires a
fixture allowlist. Click → POST `/v1/auth/authenticate` with `{provider:
"email", fixture: {token: "dev-bob"}}`; the backend obtains the subject and email
from the selected fixture rather than trusting request fields, then mints JWTs.
An explicit Codefly fixture takes precedence over local OAuth configuration.

Used by: every Playwright spec, every contributor running `codefly run`.

### WorkOS / Auth0 / Google (production)

Standard OAuth 2.0 authorization-code flow:

1. User clicks "Sign in with WorkOS" → frontend calls `BeginOAuth`; the backend
   validates the exact redirect allowlist and mints a signed CSRF state bound to
   the provider and redirect URI. The frontend stores it in `sessionStorage` and
   redirects to the provider with a PKCE challenge.
2. WorkOS authenticates the user, redirects back to `/auth/callback?code=…&state=…`.
3. Frontend verifies state matches the stored nonce.
4. Frontend POSTs `{provider: "workos", oauth_code: {code, redirect_uri,
   state, code_verifier}}` to `/v1/auth/authenticate`.
5. Backend `Exchanger.Exchange` calls WorkOS's token endpoint with
   `grant_type=authorization_code`, gets `id_token`.
6. Backend `Validator.Validate` verifies WorkOS's signature via JWKS.
7. `IdentityResolver.Resolve` upserts the user in Postgres, returns an
   internal Identity.
8. `JWTMinter.Mint` issues our own Ed25519 access + refresh pair.

Switching providers is one env var: `AUTH_PROVIDER=workos|auth0|google`.

### Token lifecycle

| Stage         | Mechanism                                                       |
|---------------|-----------------------------------------------------------------|
| Access token  | Ed25519 JWT, 15-min TTL, with identity, tenant, role, session, and assurance claims |
| Refresh token | Opaque 256-bit random token, SHA-256 hashed in `sessions`, fixed 7-day absolute family lifetime by default |
| Session idle  | Explicit 24-hour idle expiry by default; successful rotation advances idle expiry but never absolute expiry |
| Device policy | Bounded device metadata, stable family id, whole-device revocation, and configurable active-device cap (default 10) |
| Rotation      | One locked transaction consumes the family, resolves current authorization, and inserts one successor |
| Org exchange  | Authenticated target-only exchange; current membership/roles resolved under the active session lock; same device family and refresh credential |
| Reuse defense | Reuse of a token consumed by rotation revokes every active user session; administrative revocation is not replay |
| Logout        | Revokes the refresh family and deny-lists the current access-token JTI in Redis |
| Current state | Refresh requires current authorization; database triggers atomically revoke affected families on status, membership/role, platform-role, and verified-MFA changes |

---

## Authorization model

Two orthogonal axes:

- **Org role** (per-org): owner / admin / member.
- **Platform role** (global): super_admin / billing / support / "" (none).

Backend gates resources via helpers in `pkg/adapters/auth.go`:

| Helper                          | What it enforces                                       |
|---------------------------------|--------------------------------------------------------|
| `requireAuth(ctx)`              | A valid user id is in the wool context                 |
| `requireOrgMember(ctx, orgID)`  | Caller is a member of orgID (Redis-cached, 30s TTL)    |
| `requireOrgAdmin(ctx, orgID)`   | Caller is admin/owner of orgID, or super_admin globally|
| `requireTeamAdmin(ctx, teamID)` | Caller is admin/owner of the team's parent org         |
| `requirePlatformAdmin(ctx)`     | Caller has any non-empty platform role                 |
| `requireSelfOrPlatformAdmin(ctx, target)` | Caller acts on themselves OR is platform-admin |

Frontend mirrors with a `<RoleGate require="admin">` component and
`useCanAccess()` hook. UI gating prevents flash-of-admin-content for
non-admins; **server is still authoritative**.

### Public Connect/gRPC procedures (no auth required)

```
/saas.accounts.v1.AuthService/Authenticate
/saas.accounts.v1.AuthService/BeginOAuth
/saas.accounts.v1.AuthService/BeginWebAuthnMFAChallenge
/saas.accounts.v1.AuthService/CompleteMFAChallenge
/saas.accounts.v1.AuthService/CompleteWebAuthnMFAChallenge
/saas.accounts.v1.AuthService/RefreshToken
/saas.accounts.v1.AuthService/Logout
/saas.accounts.v1.AuthService/GetJWKS
/saas.accounts.v1.IntrospectionService/GetServiceInfo
/saas.accounts.v1.InvitationService/InspectInvitation
/saas.accounts.v1.UserService/RegisterUser
/saas.accounts.v1.UserService/Version
/saas.accounts.v1.WaitlistService/GetAcquisitionStatus
/saas.accounts.v1.WaitlistService/Join
/saas.accounts.v1.WaitlistService/Verify
```

This list is projected from protobuf `method_policy` exposure and pinned by
catalog tests. All other RPCs, including `AuthService/SwitchOrganization`,
require a valid bearer JWT. Public REST endpoints outside the Connect/gRPC mux
are explicit catalogued extensions rather than implicit bypasses.

---

## Feature inventory

Legend: ✅ production-ready · 🟡 partial / scoped · ❌ stubbed / not implemented

### Authentication & sessions

| Feature                             | Status | Notes                                                            |
|-------------------------------------|--------|------------------------------------------------------------------|
| OAuth code-grant (WorkOS/Auth0/Google) | ✅  | Generic OIDC exchanger; provider chosen via `AUTH_PROVIDER` env  |
| Magic link                          | 🟡    | Token gen/verify in `business/magic_links.go`; needs email-send wiring |
| Password login                       | ❌    | Intentional — provider-only; account recovery routed via OAuth    |
| Fixture login (dev)                 | ✅    | Click-to-login in dev mode; not exposed in prod build            |
| MFA (passkeys + TOTP + recovery)    | ✅    | WebAuthn with exact RP/origin policy and encrypted credential state; durable one-use login challenges and recent AAL2 step-up |
| Refresh-token rotation              | ✅    | OWASP family revocation on reuse                                 |
| Session list + revoke               | ✅    | Stable per-device family ids, device context, idle/absolute expiry, whole-family revoke |
| Session lifetime policy             | ✅    | Configurable fixed absolute TTL, idle TTL, and serialized active-device cap |
| Logout                              | ✅    | Revokes the presented device family                              |
| OAuth state / CSRF                  | 🟡    | Validated client-side in `sessionStorage`; no server-side double-check (gap) |
| OAuth PKCE                          | ❌    | Comments mention PKCE but exchanger uses `client_secret` (acceptable for confidential server-side; PKCE adds defense for SPA-driven flows) |
| Account lockout (failed attempts)   | ❌    | No counter on user table                                         |
| Email verification                  | 🟡    | `email_verified` flag stored; no flow that issues + checks       |
| Password reset                      | ❌    | No password ⇒ no reset                                           |
| Device fingerprinting               | ❌    | Deliberately not an auth signal; device description is display-only |

### Identity & users

| Feature                  | Status | Notes                                                                          |
|--------------------------|--------|--------------------------------------------------------------------------------|
| User registration        | ✅    | `RegisterUser` (provider-based; JIT during first login)                        |
| Profile (name, avatar)   | ✅    | Free-form `profile_data` JSON map                                              |
| Multiple linked identities | ✅  | Same user can have WorkOS + Google linked; lookup via `(provider, sub)`        |
| Add identity             | ✅    | `AddIdentity` — gated to self or platform-admin (security fix 2026-04-25)      |
| Find by identity         | ✅    | `FindUserByIdentity` — platform-admin only (security fix 2026-04-25)           |
| List identities          | ✅    | `ListUserIdentities` — gated to self or platform-admin (security fix 2026-04-25) |
| Account deletion         | ✅    | `DeleteUser` cascades through org memberships, audit, etc.                     |
| GDPR export              | ✅    | `ExportUserData` returns JSON / CSV bundle                                     |
| GDPR delete              | ✅    | `DeleteAllUserData` — full erasure with audit trail                            |

### Multi-tenancy (orgs / teams)

| Feature                      | Status | Notes                                                                  |
|------------------------------|--------|------------------------------------------------------------------------|
| Organizations                | ✅    | Create / get / list / update / delete                                  |
| Members + roles              | ✅    | owner / admin / member (built-in roles)                                |
| Teams within orgs            | ✅    | Create / list members; team admins                                     |
| Invitations                  | 🟡    | Hashed seven-day credential, member/admin roles, email/auth handoff, acceptance, resend cooldown, revoke, and queued delivery state; provider delivery projection and scheduled reminders are not included |
| Org branding                 | 🟡    | Logo + name stored; update RPC missing                                 |
| Transfer ownership           | ❌    | No explicit RPC; only role reassignment                                |
| Leave org                    | ❌    | Member must be removed by admin                                        |
| Org switcher (multi-org user)| ✅    | Generated `SwitchOrganization` exchange; signed session context drives one global FE selector and all tenant-scoped queries |
| Org-scoped subdomain         | ❌    | All routing is `app.example.com/admin/...` not `<org>.example.com`     |

### Acquisition, onboarding & activation

| Feature | Status | Notes |
|---------|--------|-------|
| Signup modes | ✅ | Server-enforced `open_signup` (default), `invite_only`, `approval_required`, and `closed` |
| Public waitlist | 🟡 | Enumeration-safe case-folded join, honeypot, public rate limit, optional verification, and source/campaign/referral capture; self-service suppression/deletion is not included |
| Waitlist administration | 🟡 | Platform-admin search/filter, notes/tags, individual approve/reject/invite, authorization, and audit events; batch actions, CSV, and retention automation are not included |
| Auth conversion | ✅ | Existing accounts continue; new accounts are gated by mode and approved/invited email, then attribution is converted |
| Onboarding contract | ✅ | Generated canonical step enum and explicit protobuf-to-UI transform; organization-scoped flow/version state |
| Required setup | ✅ | Organization configuration is server-authoritative and mounted as the required gate |
| Optional checklist | ✅ | Invitation, plan, and API-key steps deep-link to their real product surfaces and remain resumable/skippable |
| Product activation | ✅ | Versioned organization milestone separate from checklist completion |

Onboarding flow `starter_activation` version `1` preserves its own progress
instead of reinterpreting legacy user-global rows. A generated product records
its core value milestone with `RecordProductActivation`; additional milestone
names can use the same organization/flow/version record without changing the
starter checklist.

Invitation credentials are random, expiring, and SHA-256 hashed at rest. The
plaintext appears only in the transactional delivery command and the initial
email URL; it is never returned from `CreateInvitation`. The frontend captures
the query credential into a short-lived HttpOnly, SameSite cookie, immediately
redirects to a redacted URL, sets `no-referrer`, and clears the cookie after
acceptance. Acceptance always re-resolves the authenticated primary email and
performs the membership/status transition in one tenant transaction. Ordinary
invitations allow only `member` or `admin`; ownership transfer remains a
separate, unimplemented high-assurance operation.

### RBAC / permissions

| Feature                | Status | Notes                                                                                |
|------------------------|--------|--------------------------------------------------------------------------------------|
| Built-in roles         | ✅    | Org: owner/admin/member · Team: owner/admin/member · Platform: super_admin/billing/support |
| Custom roles           | ✅    | `CreateRole` / `AssignRole` with arbitrary permission strings                        |
| Permission matrix      | ✅    | `CheckPermission(subject, action, resource)`                                         |
| Bootstrap super_admin  | ✅    | First login matching `BOOTSTRAP_ADMIN_EMAIL` becomes super_admin; self-disarms forever |
| Fine-grained scopes    | 🟡    | API keys carry scope strings (`x-scopes` header); not enforced at handler level (gap) |
| ABAC / row-level rules | ❌    | Not modeled                                                                          |

### Background work

| Feature | Status | Notes |
|---------|--------|-------|
| Shared inbox/outbox contract | ✅ | Product-neutral `saas.jobs.v1` protobuf; Codefly-generated Go and TypeScript types |
| Durable message schema | ✅ | Exact-byte envelopes, tenant/subject/global scope, attempts, schedules, replay lineage, append-only transitions |
| Database state machine | ✅ | Finite transitions, immutable terminal history, fencing fields, attempt budgets, cross-layer parity tests |
| Transactional producers | ✅ | Generated enqueue contract, deterministic exact-retry fingerprint, collision-free ordering keys, and business-transaction outbox atomicity |
| Worker database isolation | ✅ | Function-only tenant enqueue plus grant-limited `app_job_worker`; no request payload reads or worker product-table access |
| Generic execution runtime | ✅ | Generated worker commands plus atomic claim, heartbeat, retry, recovery, fencing, ordering, and dead-letter operations |
| Worker lifecycle and telemetry | ✅ | Generic polling runtime, redacted typed failures, queue-only metrics, per-poll/job traces, and deadline-based graceful shutdown |
| Job operations | ✅ | Super-admin payload-free queue/job/history UI plus recent-MFA, idempotent, audited dead-letter replay copied inside PostgreSQL |
| Workload convergence | 🟡 | Stripe, outbound webhooks, and transactional email use generated generic job adapters; exports and approvals follow |

The persistence, producer, lifecycle, operations, Stripe, outbound-webhook, and
email adapter contracts are ready. Remaining workers migrate independently;
see `JOBS.md` for the exact boundary and sequencing.

### Billing (Stripe)

| Feature                      | Status | Notes                                                            |
|------------------------------|--------|------------------------------------------------------------------|
| Checkout sessions            | ✅    | Catalog-key-only request; stable idempotency key; server-owned redirects/trial/tax/currency |
| Customer portal              | ✅    | Server-owned return origin; stable idempotency key                |
| Plans (DB-modeled)           | ✅    | Price IDs, checkout availability, trial, currency, and tax policy are server-owned |
| Subscriptions                | ✅    | Track active/trialing/past_due/canceled                          |
| Webhook signature verify     | ✅    | Exact raw body HMAC-verified before durable acknowledgment        |
| Durable webhook inbox        | ✅    | Generated protobuf retains exact raw body + Stripe metadata before fast `2xx` |
| Webhook retries/idempotency  | ✅    | Generic atomic dedup, leased worker, fencing, backoff, dead letters, replay |
| Out-of-order convergence     | ✅    | Hydrates current Stripe state; org-serialized monotonic projection |
| Billing mutation auth        | ✅    | Owner/admin or delegated `billing:write`, plus mandatory recent AAL2 |
| Worker database isolation    | ✅    | `app_job_worker` owns lifecycle; grant-limited `app_billing_worker` owns product projection |
| Trial periods                | ✅    | Stripe-driven; status mirrored locally                           |
| Dunning emails               | 🟡    | `payment_failed` queues an exact rendered email on the generic retry/dead-letter runtime; in-app prompts remain |
| Usage metering               | ✅    | Internal `ConsumeUsage` RPC; idempotent event receipts, tenant RLS, atomic monthly hard caps |
| Usage-based invoicing        | 🟡    | Meter reconciliation and Stripe usage reporting are not wired yet |
| Invoices                     | ✅    | Connect invoice list plus hosted-detail/PDF links                 |
| Tax (sales tax / VAT)        | 🟡    | Stripe Tax can be enabled; no local tax config UI                |
| Proration on plan change     | ✅    | Handled by Stripe automatically                                  |
| Refunds                      | ❌    | Manual via Stripe dashboard only                                 |

### Notifications

| Feature                | Status | Notes                                                               |
|------------------------|--------|---------------------------------------------------------------------|
| In-app notifications   | ✅    | DB-backed, list / mark-read / unread-count                          |
| SSE notification stream| ✅    | `/api/notifications/stream` Server-Sent-Events                      |
| Email transactional    | ✅    | Invitation, magic-link, and billing emails use a generated transactional outbox and isolated worker |
| Email templates        | ✅    | Versioned DB catalog; strict variable resolution, HTML escaping, and immutable rendered job payloads |
| User notification prefs| ❌    | No opt-out per category                                             |
| Outbound webhooks      | ✅    | Generated transactional outbox, Vault keys, SSRF-safe exact-body signing, generic fenced retries/dead letters, replay |
| Push notifications     | ❌    | No web push or mobile push                                          |
| SMS notifications      | ❌    | No SMS provider                                                     |
| Slack / Teams hooks    | 🟡    | Internal Slack notifier (errors/health); not customer-facing        |

### Audit log

| Feature                | Status | Notes                                                              |
|------------------------|--------|--------------------------------------------------------------------|
| Durable event emit     | ✅    | Audit row + delivery history + generated webhook jobs commit atomically; no lossy process queue |
| Event types            | ✅    | auth.login, user.registered, org.created, role.assigned, etc.      |
| Multi-field filter     | ✅    | By org, actor, action, resource, time range                        |
| Cursor pagination      | ✅    | Stable across writes                                               |
| Retention policy       | ✅    | Goroutine purges > 90 days nightly (configurable)                  |
| Export (JSON/CSV)      | ✅    | `audit_export.go`                                                  |
| Impersonation tracking | ✅    | Records both real actor + viewed-as user                           |
| Replay / event sourcing| ❌    | Audit log is read-only history; not used to reconstruct state      |

### Admin panel

| Feature                | Status | Notes                                                              |
|------------------------|--------|--------------------------------------------------------------------|
| User search & CRUD     | ✅    | Platform-admin can suspend / unsuspend / delete                    |
| Impersonation          | ✅    | Mints session with `acting` claim; banner shown to impersonator    |
| Sessions list          | ✅    | Active device families with description, activity, and both expiries |
| Feature flags          | ✅    | DB-backed; per-org gates; `useFeatureFlag()` hook on FE            |
| Entitlements           | ✅    | Per-org plan → feature mapping with overrides                      |
| Webhooks (system view) | ✅    | List + delete from admin                                           |
| Time-bound impersonation | ✅  | Access tokens are capped at five minutes by default                 |
| Audit log viewer       | ✅    | Same UI as user-facing log, super-admin sees all orgs              |

### Compliance & legal

| Feature                  | Status | Notes                                                            |
|--------------------------|--------|------------------------------------------------------------------|
| GDPR data export         | ✅    | User-initiated; emailed when ready                               |
| GDPR data deletion       | ✅    | Cascade through all PII tables                                   |
| Audit retention          | ✅    | 90 days default, configurable                                    |
| Terms acceptance         | ✅    | Versioned authenticated evidence, separate from optional tracking choices |
| Purpose consent          | ✅    | Necessary, analytics, and marketing choices with policy version, context, timestamp, and withdrawal |
| Legal routes             | ✅    | `/legal/terms` and `/legal/privacy`; production operator content must be configured and legally reviewed |
| Optional SDK gating      | ✅    | Analytics and marketing default off; `consentchange` is emitted immediately on grant or withdrawal |
| SOC2 evidence collection | 🟡    | Audit log + access reviews would feed into SOC2; no auto-pack    |

The included legal text is a technical placeholder, not legal advice.
`NEXT_PUBLIC_LEGAL_ENTITY_NAME` and `NEXT_PUBLIC_LEGAL_CONTACT_EMAIL` must be
configured before Terms can be accepted, and adopters must replace/review the
content for their product and jurisdictions. Anonymous browser preferences are
used only to fail optional SDKs closed; they are not treated as authenticated
legal evidence.

### Frontend (Next.js)

The FE is intentionally MVC-style: most pages are thin shells around
library components in `src/features/<area>/ui/`, so consumers can swap
implementations without rewriting the route handlers.

| Surface                | Pages                                                                          |
|------------------------|--------------------------------------------------------------------------------|
| Auth & acquisition     | `/auth/login`, `/auth/callback`, `/auth/magic-link`, `/invitations/accept`, `/waitlist`, `/waitlist/verify` |
| Legal                  | `/legal/terms`, `/legal/privacy`                                                 |
| Dashboard (every user) | `/`, `/notifications`, `/onboarding`, `/settings/{mfa,notifications,data,privacy}` |
| Subscription           | `/admin/billing`, `/admin/billing/success`                                     |
| Org admin (`/admin/*`) | `users`, `organizations`, `teams`, `roles`, `invitations`, `api-keys`, `audit-log`, `webhooks`, `entitlements`, `billing` |
| Platform admin         | `/admin/platform/{admins,feature-flags,waitlist}`, `/admin/sessions`           |
| Docs                   | `/docs/sdks`, `/docs/compliance`                                               |

### Tests

| Layer       | Coverage                                                                |
|-------------|-------------------------------------------------------------------------|
| Unit (Go/TS) | Waitlist enumeration/modes and consent version checks; generated onboarding transforms, invitation roles, auth return paths, and secret redaction |
| Integration | Postgres organization/version onboarding scope, migration authority, sidecar↔backend gateway, audit retention, billing handler |
| e2e (Playwright) | Real-stack auth/admin suites plus `tests/e2e/acquisition-journey.spec.ts` for legal links, acquisition-mode UI, and browser token redaction |
| Coverage gates | None enforced today                                                  |

### Observability

| Feature        | Status | Notes                                                           |
|----------------|--------|-----------------------------------------------------------------|
| Structured logs| ✅    | `wool` everywhere; user/org/action context auto-attached         |
| Audit trail    | ✅    | Separate from app logs; queryable                                |
| Metrics        | ❌    | No Prometheus / StatsD                                           |
| Tracing        | ❌    | OpenTelemetry hooks exist in `wool` but no exporter wired        |
| Error tracking | ❌    | No Sentry / Rollbar / Honeybadger                                |
| Dashboards     | ❌    | No pre-built Grafana / Datadog                                   |

---

## Comparison: world-class SaaS starter

A reference set ("absolute must" features for a top-tier starter) and where
this module currently sits. Sources of comparison: Cal.com, Trigger.dev,
Supabase, Plain, Linear-style internal tools, the better Vercel/Next
starters, and large-scale enterprise SaaS expectations.

### Must-have (table stakes)

| Feature                                    | This module | Best-in-class      |
|--------------------------------------------|-------------|--------------------|
| OAuth login (multiple providers)           | ✅          | Same               |
| Email/password login                       | ❌          | ✅ (most starters keep both) |
| Magic-link login                           | 🟡          | ✅ (one-click)     |
| MFA enforced on sensitive ops              | ✅          | ✅ (2026-04-25 fix: `requireMFA` gates billing, GDPR delete, role grants, impersonation; opt-in per user) |
| Multi-org tenancy                          | ✅          | Same               |
| Org invitations                            | ✅          | Same               |
| RBAC (built-in roles)                      | ✅          | Same               |
| Audit log                                  | ✅          | Same — and ours has retention + export, which many starters skip |
| Stripe checkout + portal                   | ✅          | Same               |
| Webhook (inbound from Stripe, signed)      | ✅          | Same               |
| Outbound webhooks (customer endpoints)     | ✅          | ✅ — plus Vault rotation, SSRF-safe egress, generated generic multi-replica outbox |
| Email (transactional, templated, dev-mode) | ✅          | Same               |
| GDPR export + delete                       | ✅          | 🟡 (often skipped — we're ahead) |
| Admin impersonation                        | ✅          | 🟡 (Cal.com has it; many starters don't) |
| API keys with scopes                       | ✅          | ✅ (2026-04-25 fix: `requireScope` enforces `resource:action` patterns + wildcards on API-key callers; JWT callers bypass via RBAC) |
| OpenAPI / TS client autogen                | ✅          | ✅ (Connect-ES is more typesafe than fetch-based clients) |
| Real-stack e2e tests                       | ✅          | 🟡 (most starters mock — we're ahead) |

### Nice-to-have (differentiators)

| Feature                                        | This module | World-class       |
|------------------------------------------------|-------------|-------------------|
| Feature flags (per-org)                        | ✅          | Same — usually external (LaunchDarkly) |
| Entitlements (plan → feature gates)            | ✅          | 🟡 (many use Stripe metadata — ours is first-class) |
| In-app notifications                           | ✅          | Same              |
| SSE / real-time                                | ✅          | 🟡 (often WebSocket; SSE is simpler & sufficient) |
| Dark mode                                      | ✅          | Same              |
| Sidebar navigation + breadcrumbs               | ✅          | Same              |
| Onboarding checklist                           | ✅          | Same               |
| Org-scoped subdomains (`acme.example.com`)     | ❌          | ✅ (multi-tenant best practice for B2B) |
| Custom SSO (SAML / OIDC dynamic clients)       | ✅          | ✅ (2026-04-25: SSOAdminService + /admin/sso self-serve WorkOS Admin Portal flow with stub-mode for dev) |
| Webhooks UI (test event, replay, signing key)  | ✅          | ✅ (Stripe-style; v2 added 2026-04-25: replay, rotate-secret, deliveries inspector) |
| API rate limiting per org/key                  | ✅          | ✅ (2026-04-25: Redis-backed fixed-window limiter on Connect + gRPC; X-RateLimit-* headers exposed via CORS + low-budget banner on FE) |
| Usage metering + billing UI                    | ✅          | ✅ (idempotent tenant-RLS event ledger, atomic monthly quota consumption, GetUsage/GetOrgEntitlements, and billing/admin usage UI) |
| Status page / system health                    | ❌          | ✅                |
| Internationalization (i18n)                    | ❌          | 🟡                |
| Mobile-responsive admin                        | 🟡          | ✅                |
| Keyboard shortcuts (cmd-k command palette)     | ✅          | ✅ (2026-04-25 fix: cmd/ctrl+K opens; role-gated nav + super_admin user search; debounced async) |
| Activity feed (in-app)                         | 🟡          | ✅ (audit log is queryable but not user-facing UI yet) |

### "Best in the world" additions (beyond table stakes)

These are what separate a top-tier from a typical starter. Roughly
prioritized by leverage:

1. **Cmd-K command palette** — search users, orgs, audit log, navigate
   anywhere. Single biggest UX upgrade for admin-heavy products.
2. **Webhooks dashboard with replay** — Stripe-style "Last Delivery"
   with response body / status code / replay button. Customer-trust
   accelerator.
3. **API rate limiting + key scopes enforced** — prerequisite for any
   product that exposes its API.
4. **MFA enforcement policy** — `requireMFA(ctx)` decorator on billing,
   role grants, GDPR delete. We have the storage; we need the gate.
5. **Org-scoped subdomains** — biggest UX/multi-tenancy upgrade for
   B2B SaaS. Cookie scope, branding, white-label all flow from this.
6. **Sample data extension** — optionally let products add a demo-data action
   after required organization setup.
7. **System status page** — `/status` reading internal probes; protects
   incident response and shows up well during sales demos.
8. **Self-serve SSO admin** — paying enterprise plans should be able to
   wire SAML themselves. WorkOS Connections handle this if we expose it.
9. **Audit log streaming to customer S3 / Datadog** — compliance teams
   ask for this on day one of vendor review.
10. **Billing usage UI** — chart of usage vs plan limit, "you'll hit
    your limit on day X" projections. Big retention/expansion lever.

---

## Known security gaps

Severity: 🔴 critical · 🟠 important · 🟡 hardening

### Open

_All previously-open gaps closed 2026-04-25._

### Resolved 2026-04-26

- ✅ **GetOrgEntitlements had no authz** — any authenticated user could read any org's plan + usage. Now gated by `requireOrgMember` (platform admins implicitly satisfy via membership).
- ✅ **OverrideEntitlement had no authz** — implicit JWT-only. Now requires `platformAdmin`.
- ✅ **API key scopes enforced on more endpoints** — webhooks (Create/Delete/List/Test/GetDelivery/ReplayDelivery/RotateSecret) and api-keys (Create/List/Revoke). 5 new unit tests pin the wildcard semantics + the JWT pass-through.
- ✅ **Self-serve SSO admin (WorkOS Connections)** — proto SSOAdminService + business / handler / WorkOS HTTP client + migration 21 + /admin/sso FE. Stub-mode when no `WORKOS_API_KEY` so dev exercises the full flow.
- ✅ **Audit-export FE admin form** — was backend-only; /admin/audit-export now lets org admins configure their bucket through the UI. Pre-flight connection probe at Save time so bad creds fail fast.
- ✅ **s3 plugin now actually runs MinIO** — was a redis-template scaffold (port 6379, redis ping readiness); now real (port 9000, /minio/health/live, structured conn keys, agent v0.0.2).
- ✅ **User settings API** — JSONB-backed (`users.settings`) + UserSettingsService + /settings hub (theme / locale / timezone / date-time format / email opt-ins).
- ✅ **Theme toggle** — next-themes wired with system / light / dark, persists per user via the settings API, syncs across devices.
- ✅ **Stripe billing portal in /admin/billing** — Connect-RPC `BillingService.OpenPortal` works without sidecar.
- ✅ **Stripe invoices list** — last 12 invoices on /admin/billing with hosted-detail link + PDF download.
- ✅ **Rate-limit visibility** — X-RateLimit-* exposed via CORS; FE captures every response, banner appears at <10% remaining.

### Resolved 2026-04-25

- ✅ **User identity endpoints unauthenticated** — `AddIdentity`, `FindUserByIdentity`, `ListUserIdentities` would let any authenticated caller enumerate provider identities or attach attacker-controlled identities to any user. Now gated by `requireSelfOrPlatformAdmin` / `requirePlatformAdmin`.
- ✅ **gRPC server had no in-process auth interceptor** — handlers assumed sidecar presence; direct port hits bypassed auth. Added `grpcAuthInterceptor` mirroring the Connect interceptor (defense in depth: api validates the bearer regardless of upstream).
- ✅ **Connect server had no CORS** — browser preflight returned 405; every Connect-Web request from the FE failed in production-style architectures. Added `rs/cors` middleware.
- ✅ **MFA is enforced and refresh-safe** — enrolled users receive no normal session until a durable one-use challenge succeeds. JWT/session evidence carries `amr`, `auth_time`, `acr`, and `mfa_at`; refresh preserves rather than renews that evidence. Refresh re-resolves verified enrollment: newly enrolled MFA terminates AAL1 refresh families and requires login, while removed MFA strips factor methods and downgrades the successor to AAL1. General sensitive operations apply the configured recent-AAL2 policy to enrolled users; money-moving billing checkout/portal is stricter and always requires fresh AAL2, so lack of enrollment is not a bypass. Passkeys require WebAuthn user verification with exact Codefly-configured RP/origin policy; complete credentials and ceremony state use Vault Transit envelopes. TOTP seeds are encrypted and recovery codes are one-use bcrypt hashes.
- ✅ **API key scopes forwarded but not enforced** — sidecar set `X-Scopes`; handlers ignored. New `requireScope(ctx, "resource:action")` gate with wildcard support (`*`, `users:*`, `*:read`). Applied to `ListUsers`, `UpdateUser`, `DeleteUser` as a starter set; extend to other resources per business needs (JWT-authenticated callers bypass — RBAC handles them).
- ✅ **Access tokens not individually revocable** — Logout only killed the refresh chain; old access tokens stayed valid up to 15 min. New `auth.TokenRevoker` interface + `cache.NewTokenRevoker` Redis impl. `Logout(refresh, accessToken)` now calls `JWTMinter.RevokeAccess` which adds the jti to the revocation list with TTL = remaining `exp`. `VerifyAccess` consults the list. Falls back to `NoopTokenRevoker` (no Redis) → original behavior.
- ✅ **Impersonation had no time limit** — admin "view-as" sessions inherited the normal 15-min TTL. New `Config.ImpersonationTokenTTL` (default 5 min) auto-applied when minting tokens with `acting` claim set.
- ✅ **Cache invalidation on member-remove** — confirmed correct on a closer read. `CacheInvalidator.InvalidateMembership` calls `cache.Delete` against shared Redis, so all api instances see the change immediately. The 30s TTL is safety net, not staleness window.
- ✅ **OAuth `state` not validated server-side** — added `auth.OAuthStateSigner` (HMAC-SHA256, key derived from the JWT private key with a domain label, 10-min TTL). `BeginOAuth(provider, redirect_uri) → state` mints a server-signed token bound to provider + redirect URI. `Authenticate` requires and re-verifies it; mismatch returns the canonical `ErrInvalidOAuthState` without an oracle. The frontend fails closed if state cannot be minted. Exact redirects are enforced by `OAUTH_ALLOWED_REDIRECT_URIS` during both initiation and exchange.
- ✅ **PKCE for OAuth code flow** — FE now generates a 64-byte `code_verifier` per sign-in, computes SHA-256 `code_challenge`, and includes both in the authorize URL. `code_verifier` rides through `Authenticate` to `Exchanger.Exchange`, which forwards it as the standard `code_verifier` form parameter to the provider's token endpoint. Belt-and-suspenders alongside the existing `client_secret` flow — recommended even for confidential clients per OAuth 2.1.
- ✅ **Connect error code translation** — handlers return `status.Error(codes.X, ...)` (gRPC) but Connect-Go didn't recognize the wrapper, defaulting every error to `CodeUnknown` → HTTP 500. Added `translateGRPCError` in the Connect `unary` adapter mapping all 16 gRPC codes to their Connect equivalents. Without this, the auth-boundary tests showed Bob's `PermissionDenied` as 500 instead of 403, masking the real behaviour.
- ✅ **Rate limiting per org / API key** — `cache.RateLimiter` (Redis-backed fixed-window) + Connect/gRPC interceptors. Key derivation: API-key id > org id > user id. Default 1000 req/min, configurable. Returns `ResourceExhausted` (gRPC 8 / HTTP 429) with `Retry-After` and `X-RateLimit-*` headers; nil-receiver / nil-cache / zero-limit all degrade to allow-all so an unconfigured Redis can't mass-reject. 5 unit tests cover budget exhaustion, per-key isolation, and graceful degradation.
- ✅ **Cmd-K command palette** — global cmd/ctrl+K dialog at the dashboard root. Its generated navigation projection is shared with the sidebar/plugin registry and role-gated (admin / super_admin / personal); async user search uses Connect-ES (200ms debounce, super_admin only — server-gated, not just UI). E2E specs cover hotkey, role visibility, and navigation. Built on the existing cmdk + shadcn primitives (zero new deps).

---

## Roadmap (proposed)

In priority order, based on "where we'd lose deals or land in a CVE":

**Quarter 1 — production hardening**
1. ~~Enforce MFA on sensitive ops~~ — done 2026-04-25.
2. ~~Enforce API key scopes~~ — done 2026-04-25.
3. ~~Access-token revocation list~~ — done 2026-04-25 (Redis-backed).
4. ~~Impersonation TTL cap~~ — done 2026-04-25 (5 min default).
5. ~~OAuth state server-side / PKCE~~ — done 2026-04-25 (`BeginOAuth` RPC + signer + FE refactor).
6. ~~Rate limiting per org + per API key~~ — done 2026-04-25 (Redis fixed-window; Connect + gRPC interceptors).
7. ~~Cmd-K command palette~~ — done 2026-04-25 (role-gated nav + super_admin user search).
8. ~~Webhooks v2 — replay UI, signing-secret rotation, generic retry visibility~~ — done 2026-07-20.

**Quarter 2 — growth features**
6. Org-scoped subdomains + cookie scoping (~1 week).
7. Self-serve SSO admin UI (WorkOS Connections passthrough) (~3 days).
8. Sample-data onboarding extension (~2 days).
9. Usage dashboards (~1 week).
10. Status page + internal probes (~3 days).

**Quarter 3 — enterprise**
11. Audit log streaming to customer S3 / SIEM (~1 week).
12. Jurisdiction-specific legal content and consent review (deployment work).
13. ABAC / row-level rules where useful (selective; ~ 2 weeks scoped).
14. i18n (~2 weeks for full pass).

---

## Configuration cheatsheet

Environment variables consumed by the api:

| Var                            | Used for                                                    |
|--------------------------------|-------------------------------------------------------------|
| `POSTGRES_URL`                 | DB connection (codefly auto-injects)                        |
| `VAULT_ADDR`, `VAULT_TOKEN`    | Signing-key storage; falls back to ephemeral key in dev     |
| `BOOTSTRAP_ADMIN_EMAIL`        | First login matching this becomes super_admin              |
| `AUTH_PROVIDER`                | Required: `workos` / `auth0` / `google`; `dev` only for explicit local fixtures |
| `WORKOS_CLIENT_ID/SECRET`      | OAuth client credentials                                   |
| `WORKOS_ISSUER`, `WORKOS_JWKS_URL` | Override discovery URLs (optional)                      |
| `OAUTH_ALLOWED_REDIRECT_URIS`  | Required exact callback URI allowlist (comma-separated)     |
| `STRIPE_API_KEY`               | Enables billing endpoints                                   |
| `STRIPE_WEBHOOK_SECRET`        | Required with Stripe API key; exact-body webhook verification |
| `RESEND_API_KEY`               | Switches email sender from log-only to Resend               |
| `EMAIL_FROM`                   | Default sender address                                      |
| `APP_BASE_URL`                 | Exact HTTPS production origin for email and server-owned Stripe redirects |
| `ACQUISITION_MODE`             | `open_signup` (default), `invite_only`, `approval_required`, or `closed` |
| `WAITLIST_EMAIL_VERIFICATION`  | Require a time-limited verification email before waitlist approval |
| `SLACK_WEBHOOK_URL`            | Internal alerts (optional)                                  |
| `CODEFLY__FIXTURE`             | Loads fixture YAML (e.g. `dev-admin`); FE login picker too |

Frontend browser configuration (`NEXT_PUBLIC_*` values are baked into the client bundle):

| Var                          | Used for                                                    |
|------------------------------|-------------------------------------------------------------|
| `NEXT_PUBLIC_WORKOS_*`       | OAuth provider preset (presence enables provider in UI)     |
| `NEXT_PUBLIC_AUTH0_*`        | Same, for Auth0                                             |
| `NEXT_PUBLIC_GOOGLE_*`       | Same, for Google                                            |
| `NEXT_PUBLIC_LEGAL_ENTITY_NAME` | Operator named in configured legal content                |
| `NEXT_PUBLIC_LEGAL_CONTACT_EMAIL` | Legal/privacy contact; required before Terms acceptance |

Accounts REST and Connect browser calls are relative and same-origin. The
server-only `API_REST_INTERNAL` and `API_CONNECT_INTERNAL` Codefly bindings are
resolved by Next rewrites and server route handlers; backend origins are never
published to browser code.

---

## How to evaluate this starter against your needs

If you're picking a starter, ask:

1. Does it support our identity provider? **WorkOS / Auth0 / Google ✅; SAML via WorkOS ✅; LDAP ❌**
2. Multi-tenant from day one? **Yes — orgs/teams/roles built in.**
3. Stripe integration that won't bite us in prod? **Webhook signature + idempotency + portal + checkout ✅.** Dunning flows are basic.
4. Audit + compliance ready for an early SOC 2 push? **Audit retention, GDPR export/delete, impersonation tracking, versioned Terms evidence, and purpose consent are included.** Legal content still requires jurisdiction-specific review.
5. Real tests that actually exercise auth/billing/audit? **Yes — Playwright e2e against the running stack.**
6. Easy to run locally? **One command (`codefly run service --fixture dev-admin`); no docker-compose surgery.**
7. What will I have to build that's "obviously missing"? Per the gap list above: rate limiting, command palette, webhooks v2 dashboard, self-serve SSO admin, org subdomains. These are 1–5 days each.

---

*Maintained alongside the code. If you change a feature or close a gap,
update this file in the same PR.*
