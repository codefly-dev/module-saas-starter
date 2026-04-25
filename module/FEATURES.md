# saas-starter — Features

> What this module ships with, what's stubbed, and how it stacks up against
> a world-class SaaS starter. Source-of-truth checklist for both end users
> picking a starter and contributors deciding what to build next.

Last updated: 2026-04-25

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

Everything orchestrated by codefly: `codefly run service frontend
--fixture dev-admin` brings up Postgres + Vault + Redis + api + sidecar +
frontend with seed data in one command.

---

## Auth flows

### Fixture (dev / test)

`dev-admin.yaml` seeds 4 users: Sarah Chen (super_admin), Alice (admin),
Bob (member), Carol (member). The login page detects fixture mode (no
`NEXT_PUBLIC_*_AUTHORIZE_URL` configured) and renders a one-click user
picker. Click → POST `/v1/auth/authenticate` with `{provider: "email",
provider_id: "dev-bob", provider_email: "bob@acme.com"}` → mint JWT.

Used by: every Playwright spec, every contributor running `codefly run`.

### WorkOS / Auth0 / Google (production)

Standard OAuth 2.0 authorization-code flow:

1. User clicks "Sign in with WorkOS" → frontend generates a CSRF state
   nonce, stores it in `sessionStorage`, redirects to WorkOS authorize URL.
2. WorkOS authenticates the user, redirects back to `/auth/callback?code=…&state=…`.
3. Frontend verifies state matches the stored nonce.
4. Frontend POSTs `{provider: "workos", profile: {code, redirect_uri}}` to
   `/v1/auth/authenticate`.
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
| Access token  | Ed25519 JWT, 15-min TTL, claims: sub/iss/aud/exp/nbf/iat/jti/sid/org/or/pr/acting |
| Refresh token | Opaque random, SHA-256 hashed in `sessions` table, 30-day TTL   |
| Rotation      | OWASP pattern: each refresh consumes the old token, issues a new pair in the same family |
| Reuse defense | Presented-but-revoked refresh → revoke entire user's session families (all devices) |
| Logout        | Marks the refresh family revoked; access tokens valid until expiry |
| Revocation    | Family-based; access tokens NOT individually revocable (15-min blast radius) |

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
/customers.AuthService/Authenticate
/customers.AuthService/RefreshToken
/customers.AuthService/Logout
/customers.AuthService/GetJWKS
```

All other RPCs require a valid bearer JWT. Public REST endpoints (Stripe
webhook, OpenAPI spec) live outside the Connect/gRPC mux and are
explicitly exempted.

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
| MFA (TOTP)                          | 🟡    | Setup, verify, list, disable + backup codes — but NOT enforced on login or sensitive ops |
| Refresh-token rotation              | ✅    | OWASP family revocation on reuse                                 |
| Session list + revoke               | ✅    | `ListActiveSessions` per user, revoke-by-family                  |
| Logout                              | ✅    | Single-device + all-devices                                      |
| OAuth state / CSRF                  | 🟡    | Validated client-side in `sessionStorage`; no server-side double-check (gap) |
| OAuth PKCE                          | ❌    | Comments mention PKCE but exchanger uses `client_secret` (acceptable for confidential server-side; PKCE adds defense for SPA-driven flows) |
| Account lockout (failed attempts)   | ❌    | No counter on user table                                         |
| Email verification                  | 🟡    | `email_verified` flag stored; no flow that issues + checks       |
| Password reset                      | ❌    | No password ⇒ no reset                                           |
| Device fingerprinting               | ❌    | Not modeled                                                      |

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
| Invitations                  | ✅    | Email-based; accept, revoke, list pending                              |
| Org branding                 | 🟡    | Logo + name stored; update RPC missing                                 |
| Transfer ownership           | ❌    | No explicit RPC; only role reassignment                                |
| Leave org                    | ❌    | Member must be removed by admin                                        |
| Org switcher (multi-org user)| 🟡    | Backend supports; FE picks the most-recently-joined org as session default |
| Org-scoped subdomain         | ❌    | All routing is `app.example.com/admin/...` not `<org>.example.com`     |

### RBAC / permissions

| Feature                | Status | Notes                                                                                |
|------------------------|--------|--------------------------------------------------------------------------------------|
| Built-in roles         | ✅    | Org: owner/admin/member · Team: owner/admin/member · Platform: super_admin/billing/support |
| Custom roles           | ✅    | `CreateRole` / `AssignRole` with arbitrary permission strings                        |
| Permission matrix      | ✅    | `CheckPermission(subject, action, resource)`                                         |
| Bootstrap super_admin  | ✅    | First login matching `BOOTSTRAP_ADMIN_EMAIL` becomes super_admin; self-disarms forever |
| Fine-grained scopes    | 🟡    | API keys carry scope strings (`x-scopes` header); not enforced at handler level (gap) |
| ABAC / row-level rules | ❌    | Not modeled                                                                          |

### Billing (Stripe)

| Feature                      | Status | Notes                                                            |
|------------------------------|--------|------------------------------------------------------------------|
| Checkout sessions            | ✅    | `/v1/billing/checkout` — creates Stripe checkout, redirects user |
| Customer portal              | ✅    | `/v1/billing/portal` — one-click "manage subscription"           |
| Plans (DB-modeled)           | ✅    | `plans` table with `stripe_product_id`, `stripe_price_id`        |
| Subscriptions                | ✅    | Track active/trialing/past_due/canceled                          |
| Webhook signature verify     | ✅    | HMAC verified before processing                                  |
| Webhook idempotency          | ✅    | `stripe_webhook_events` dedup table                              |
| Trial periods                | ✅    | Stripe-driven; status mirrored locally                           |
| Dunning emails               | 🟡    | `payment_failed` triggers email; no in-app prompts or retry orchestration |
| Usage-based billing          | 🟡    | Schema supports `EntitlementChecker`; no `RecordUsage` RPC       |
| Invoices                     | 🟡    | Webhooks update DB; no list-invoices RPC for end users            |
| Tax (sales tax / VAT)        | 🟡    | Stripe Tax can be enabled; no local tax config UI                |
| Proration on plan change     | ✅    | Handled by Stripe automatically                                  |
| Refunds                      | ❌    | Manual via Stripe dashboard only                                 |

### Notifications

| Feature                | Status | Notes                                                               |
|------------------------|--------|---------------------------------------------------------------------|
| In-app notifications   | ✅    | DB-backed, list / mark-read / unread-count                          |
| SSE notification stream| ✅    | `/api/notifications/stream` Server-Sent-Events                      |
| Email transactional    | ✅    | Welcome, invitation, GDPR-export-ready, billing event emails        |
| Email templates        | ✅    | DB-backed templates with variable substitution                      |
| User notification prefs| ❌    | No opt-out per category                                             |
| Outbound webhooks      | 🟡    | Customer-facing webhooks (CRUD + dispatcher exist; signature signing implemented; retry/backoff scaffolded but not battle-tested) |
| Push notifications     | ❌    | No web push or mobile push                                          |
| SMS notifications      | ❌    | No SMS provider                                                     |
| Slack / Teams hooks    | 🟡    | Internal Slack notifier (errors/health); not customer-facing        |

### Audit log

| Feature                | Status | Notes                                                              |
|------------------------|--------|--------------------------------------------------------------------|
| Async event emit       | ✅    | `AsyncAuditEmitter` (1024 buffer); never blocks request path       |
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
| Sessions list          | ✅    | All active sessions platform-wide                                  |
| Feature flags          | ✅    | DB-backed; per-org gates; `useFeatureFlag()` hook on FE            |
| Entitlements           | ✅    | Per-org plan → feature mapping with overrides                      |
| Webhooks (system view) | ✅    | List + delete from admin                                           |
| Time-bound impersonation | ❌  | No automatic session expiry on impersonated sessions               |
| Audit log viewer       | ✅    | Same UI as user-facing log, super-admin sees all orgs              |

### Compliance & legal

| Feature                  | Status | Notes                                                            |
|--------------------------|--------|------------------------------------------------------------------|
| GDPR data export         | ✅    | User-initiated; emailed when ready                               |
| GDPR data deletion       | ✅    | Cascade through all PII tables                                   |
| Audit retention          | ✅    | 90 days default, configurable                                    |
| Consent / TOS versioning | ❌    | No `terms_accepted_at`, no policy version table                   |
| Cookie consent           | ❌    | No banner / consent record                                       |
| SOC2 evidence collection | 🟡    | Audit log + access reviews would feed into SOC2; no auto-pack    |

### Frontend (Next.js)

The FE is intentionally MVC-style: most pages are thin shells around
library components in `src/features/<area>/ui/`, so consumers can swap
implementations without rewriting the route handlers.

| Surface                | Pages                                                                          |
|------------------------|--------------------------------------------------------------------------------|
| Auth                   | `/auth/login`, `/auth/callback`, `/auth/magic-link`                            |
| Dashboard (every user) | `/`, `/notifications`, `/settings/{mfa,notifications,data}`                    |
| Pricing                | `/pricing`                                                                     |
| Onboarding             | `/onboarding`                                                                  |
| Org admin (`/admin/*`) | `users`, `organizations`, `teams`, `roles`, `invitations`, `api-keys`, `audit-log`, `webhooks`, `entitlements`, `billing/success` |
| Platform admin         | `/admin/platform/{admins,feature-flags}`, `/admin/sessions`                    |
| Docs                   | `/docs/sdks`, `/docs/compliance`                                               |

### Tests

| Layer       | Coverage                                                                |
|-------------|-------------------------------------------------------------------------|
| Unit (Go)   | Auth/identity/business — `*_test.go` per package                        |
| Integration | Sidecar↔backend gateway, audit retention, billing handler               |
| e2e (Playwright) | 32 specs across 8 files (login, navigation, admin-flow, webhooks, auth-boundary, revocation, command-palette, sdk-smoke), full stack via `withDependencies` (~54s warm, ~2min cold) |
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
| Outbound webhooks (customer endpoints)     | 🟡          | ✅ (with retry, signature, replay UI) |
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
| Onboarding checklist                           | 🟡          | ✅                |
| Org-scoped subdomains (`acme.example.com`)     | ❌          | ✅ (multi-tenant best practice for B2B) |
| Custom SSO (SAML / OIDC dynamic clients)       | 🟡          | ✅ (WorkOS handles SAML behind the scenes; we'd need explicit "add SSO connection" UI) |
| Webhooks UI (test event, replay, signing key)  | 🟡          | ✅ (Stripe-style)  |
| API rate limiting per org/key                  | ✅          | ✅ (2026-04-25 fix: Redis-backed fixed-window limiter on Connect + gRPC; per API-key > per-org > per-user fallback) |
| Usage-based billing UI                         | 🟡          | ✅ (Linear/Vercel style) |
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
6. **Onboarding checklist with sample data toggle** — convert sign-ups
   to active users. We have fixtures already; surface as "load demo data".
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

### Resolved 2026-04-25

- ✅ **User identity endpoints unauthenticated** — `AddIdentity`, `FindUserByIdentity`, `ListUserIdentities` would let any authenticated caller enumerate provider identities or attach attacker-controlled identities to any user. Now gated by `requireSelfOrPlatformAdmin` / `requirePlatformAdmin`.
- ✅ **gRPC server had no in-process auth interceptor** — handlers assumed sidecar presence; direct port hits bypassed auth. Added `grpcAuthInterceptor` mirroring the Connect interceptor (defense in depth: api validates the bearer regardless of upstream).
- ✅ **Connect server had no CORS** — browser preflight returned 405; every Connect-Web request from the FE failed in production-style architectures. Added `rs/cors` middleware.
- ✅ **MFA enforcement was cosmetic** — TOTP setup existed but no handler required `mfa_satisfied=true`. JWT now carries `mfa` claim (true when user has no enrolled device OR cleared a challenge). New `requireMFA(ctx, actorID)` gate applied to: `ImpersonateUser`, `GrantPlatformRole`, `RevokePlatformRole`, GDPR `RequestDeletion`, billing `/v1/billing/checkout`, billing `/v1/billing/portal`. Returns `FailedPrecondition` so FE can prompt for TOTP.
- ✅ **API key scopes forwarded but not enforced** — sidecar set `X-Scopes`; handlers ignored. New `requireScope(ctx, "resource:action")` gate with wildcard support (`*`, `users:*`, `*:read`). Applied to `ListUsers`, `UpdateUser`, `DeleteUser` as a starter set; extend to other resources per business needs (JWT-authenticated callers bypass — RBAC handles them).
- ✅ **Access tokens not individually revocable** — Logout only killed the refresh chain; old access tokens stayed valid up to 15 min. New `auth.TokenRevoker` interface + `cache.NewTokenRevoker` Redis impl. `Logout(refresh, accessToken)` now calls `JWTMinter.RevokeAccess` which adds the jti to the revocation list with TTL = remaining `exp`. `VerifyAccess` consults the list. Falls back to `NoopTokenRevoker` (no Redis) → original behavior.
- ✅ **Impersonation had no time limit** — admin "view-as" sessions inherited the normal 15-min TTL. New `Config.ImpersonationTokenTTL` (default 5 min) auto-applied when minting tokens with `acting` claim set.
- ✅ **Cache invalidation on member-remove** — confirmed correct on a closer read. `CacheInvalidator.InvalidateMembership` calls `cache.Delete` against shared Redis, so all api instances see the change immediately. The 30s TTL is safety net, not staleness window.
- ✅ **OAuth `state` not validated server-side** — added `auth.OAuthStateSigner` (HMAC-SHA256, key derived from the JWT private key with a domain label, 10-min TTL). New `BeginOAuth(provider, redirect_uri) → state` RPC mints a server-signed self-validating token bound to provider + redirect_uri. `Authenticate` re-verifies on callback; mismatch returns the canonical `ErrInvalidOAuthState` (no oracle on the cause). FE refactored to call `BeginOAuth` before redirect; falls back to client-only random state when the RPC is unreachable so offline dev still works. 6 unit tests cover sig tampering, provider-mismatch, redirect-mismatch, expiry, and key-divergence.
- ✅ **PKCE for OAuth code flow** — FE now generates a 64-byte `code_verifier` per sign-in, computes SHA-256 `code_challenge`, and includes both in the authorize URL. `code_verifier` rides through `Authenticate` to `Exchanger.Exchange`, which forwards it as the standard `code_verifier` form parameter to the provider's token endpoint. Belt-and-suspenders alongside the existing `client_secret` flow — recommended even for confidential clients per OAuth 2.1.
- ✅ **Connect error code translation** — handlers return `status.Error(codes.X, ...)` (gRPC) but Connect-Go didn't recognize the wrapper, defaulting every error to `CodeUnknown` → HTTP 500. Added `translateGRPCError` in the Connect `unary` adapter mapping all 16 gRPC codes to their Connect equivalents. Without this, the auth-boundary tests showed Bob's `PermissionDenied` as 500 instead of 403, masking the real behaviour.
- ✅ **Rate limiting per org / API key** — `cache.RateLimiter` (Redis-backed fixed-window) + Connect/gRPC interceptors. Key derivation: API-key id > org id > user id. Default 1000 req/min, configurable. Returns `ResourceExhausted` (gRPC 8 / HTTP 429) with `Retry-After` and `X-RateLimit-*` headers; nil-receiver / nil-cache / zero-limit all degrade to allow-all so an unconfigured Redis can't mass-reject. 5 unit tests cover budget exhaustion, per-key isolation, and graceful degradation.
- ✅ **Cmd-K command palette** — global cmd/ctrl+K dialog at the dashboard root. Static nav list role-gated (admin / super_admin / public-personal); async user search via Connect-ES (200ms debounce, super_admin only — server-gated, not just UI). 4 e2e specs cover hotkey, role visibility, navigation. Built on the existing cmdk + shadcn primitives (zero new deps).

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
8. Webhooks v2 — replay UI, signing-secret rotation, retry tuning (~3–5 days).

**Quarter 2 — growth features**
6. Org-scoped subdomains + cookie scoping (~1 week).
7. Self-serve SSO admin UI (WorkOS Connections passthrough) (~3 days).
8. Onboarding checklist + sample-data toggle (~3 days).
9. Usage dashboards (~1 week).
10. Status page + internal probes (~3 days).

**Quarter 3 — enterprise**
11. Audit log streaming to customer S3 / SIEM (~1 week).
12. Consent / TOS versioning (~3 days).
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
| `AUTH_PROVIDER`                | `workos` / `auth0` / `google` / empty (fixture mode)        |
| `WORKOS_CLIENT_ID/SECRET`      | OAuth client credentials                                   |
| `WORKOS_ISSUER`, `WORKOS_JWKS_URL` | Override discovery URLs (optional)                      |
| `STRIPE_API_KEY`               | Enables billing endpoints                                   |
| `STRIPE_WEBHOOK_SECRET`        | Webhook signature verification                              |
| `RESEND_API_KEY`               | Switches email sender from log-only to Resend               |
| `EMAIL_FROM`                   | Default sender address                                      |
| `APP_BASE_URL`                 | Used in email templates for return URLs                     |
| `SLACK_WEBHOOK_URL`            | Internal alerts (optional)                                  |
| `CODEFLY__FIXTURE`             | Loads fixture YAML (e.g. `dev-admin`); FE login picker too |

Frontend (`NEXT_PUBLIC_*` only — these get baked into the client bundle):

| Var                          | Used for                                                    |
|------------------------------|-------------------------------------------------------------|
| `NEXT_PUBLIC_API_CONNECT`    | Connect-ES base URL (sidecar in prod, api direct in dev)    |
| `NEXT_PUBLIC_API_REST`       | grpc-gateway REST base URL                                  |
| `NEXT_PUBLIC_BACKEND_URL`    | Fallback for the above                                      |
| `NEXT_PUBLIC_WORKOS_*`       | OAuth provider preset (presence enables provider in UI)     |
| `NEXT_PUBLIC_AUTH0_*`        | Same, for Auth0                                             |
| `NEXT_PUBLIC_GOOGLE_*`       | Same, for Google                                            |

---

## How to evaluate this starter against your needs

If you're picking a starter, ask:

1. Does it support our identity provider? **WorkOS / Auth0 / Google ✅; SAML via WorkOS ✅; LDAP ❌**
2. Multi-tenant from day one? **Yes — orgs/teams/roles built in.**
3. Stripe integration that won't bite us in prod? **Webhook signature + idempotency + portal + checkout ✅.** Dunning flows are basic.
4. Audit + compliance ready for an early SOC 2 push? **Audit retention, GDPR export/delete, impersonation tracking — yes.** Cookie consent + TOS versioning — no, add yourself.
5. Real tests that actually exercise auth/billing/audit? **Yes — Playwright e2e against the running stack.**
6. Easy to run locally? **One command (`codefly run service frontend --fixture dev-admin`); no docker-compose surgery.**
7. What will I have to build that's "obviously missing"? Per the gap list above: rate limiting, command palette, webhooks v2 dashboard, self-serve SSO admin, org subdomains. These are 1–5 days each.

---

*Maintained alongside the code. If you change a feature or close a gap,
update this file in the same PR.*
