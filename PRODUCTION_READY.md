# saas-starter — path to production

Scope of this document: **Tier 1 (ship-blockers)** only. Observability, Sentry, backups, Directory Sync, Admin Portal are out of scope here — separate docs later.

## Public launch readiness

The public company site is a separate `marketing` runtime. Before a production
build, replace the development fixture in `module/public/site.config.json` and
run:

```sh
node module/tools/generate-public-config.mjs
cd module/services/marketing/code
MARKETING_INDEXABLE=true \
MARKETING_CATALOG_URL=https://api.example.com \
MARKETING_STRICT_READINESS=1 \
npm run readiness
```

Readiness rejects example domains and contacts, development claims, a
non-indexable production site, unsafe URLs, and missing public pricing
configuration. Configure apex-to-`www`, `www`, `app`, docs, and external status
DNS explicitly; the copy-only AWS patch in
`module/deployment/kustomize/overlays/aws/marketing-domains.example.patch.yaml`
does not change an adopter's hosts automatically. Authentication callback
origins and cookies remain app-host scoped.

Production content, legal text, security claims, public prices, contact paths,
and optional analytics providers require adopter review. Repository defaults
are fixtures, not legal, compliance, customer, accessibility-certification, or
performance claims.

## Architectural decisions (locked)

0. **Frontend is the product entry; the gateway is the only API path.** Pages and static assets enter through `frontend/http`. Every backend request — including public OAuth, primary-authentication, MFA, refresh, logout, registration, and discovery ceremonies — is same-origin proxied through the private sidecar gateway. Accounts is never publicly reachable. Exposure, rate-limit class, tenant requirements, sensitivity, and audit behavior come from each protobuf method's `saas.policy.v1.method_policy`; unclassified methods fail descriptor validation and are denied at runtime.

1. **Two auth paths, hard-separated.** Authentication ceremonies, refresh/logout, and organization token exchange go through the accounts backend via the gateway. Normal product requests use the sidecar-verified runtime identity. The switch endpoint is authenticated by the current access token; it never accepts identity or role claims from the browser.

2. **Our own JWT is the runtime token.** On login/signup, backend mints an Ed25519-signed JWT carrying `sub=user_id`, `org=organization_id`, `or=organization_role`, `pr=platform_role`, and `sid=session_id`. Sidecar validates that token on every request — no DB hit, no provider knowledge.

3. **WorkOS is login-only.** WorkOS tokens never enter the runtime path. They are validated exactly once during `Authenticate`, then thrown away. The sidecar has no WorkOS dependency.

4. **`TokenValidator` + `IdentityResolver` live in the backend `pkg/auth/`**, not in the sidecar. They run during login/signup only. Sidecar has its own much smaller `LocalValidator` for our JWT.

5. **UUID v7 for all primary keys.** `github.com/google/uuid` v1.6+ `uuid.NewV7()`. Never v5 for user-visible ids.

6. **Identity translation layer.** `(provider, provider_sub)` → internal `user_id` via `provider_identities` table. Runs exactly once per provider identity, at first login. Provider's `sub` never enters the JWT or headers.

7. **Forwarded headers** (sidecar → upstream):
   - `X-User-ID` — canonical internal user uuid v7
   - `X-Org-ID` — canonical internal org uuid v7
   - `X-Org-Role` — `owner | admin | member`
   - `X-Platform-Role` — `super_admin | billing | support | ""`
   - `X-Session-ID` — uuid v7, for audit/correlation
   - `X-Acting-As-User-ID` — present only during impersonation

8. **Token and device-session policy**: access tokens live 15 minutes. A refresh family has a fixed seven-day absolute lifetime and a 24-hour idle window by default; rotation advances only idle expiry and cannot slide the absolute boundary. Initial login atomically enforces the configured active-device cap (default ten) and evicts the least-recently active family. Bounded display-only device metadata survives MFA and rotation, while management uses the stable family id and revokes the whole device. Refresh consumption, current-authorization resolution, family revocation, and successor insertion are one locked PostgreSQL transaction. Each refresh requires an active user and current selected-org membership, projects current org/platform roles, and evaluates current verified MFA enrollment. Concurrent reuse of a token consumed by rotation commits revocation of every active refresh session for the affected user; logout and administrative revocation are not misclassified as replay. Database triggers revoke affected refresh sessions atomically when user status, membership/org role, platform role, or verified MFA enrollment changes. Organization switching is a separate authenticated access-token exchange serialized on the exact active session row: the server resolves current target membership/roles, signs a fresh access token with the same `sid`, and updates only the selected organization projection. It does not rotate the refresh credential, create a device, or advance session lifetime, so a switch racing refresh cannot be misclassified as replay. An already-issued access token remains a signed snapshot for at most its 15-minute lifetime; high-risk deployments can shorten that TTL or add a stateful gateway session check without weakening refresh invariants.

9. **Codefly `identity` configuration selects the login adapter.** Fixture mode additionally requires an explicit Codefly fixture; WorkOS, Auth0, and Google use the same generic configuration contract. The sidecar does not select a provider—it only validates our JWT.

10. **Orgs are canonical in our DB.** `orgs.provider_org_id` is a nullable link to a WorkOS organization for SSO configuration, but not the source of truth.

11. **Invitations stay ours.** Local DB, local email, 7-day hashed tokens — already implemented, keep as-is.

12. **First super_admin bootstrap**: env-gated `BOOTSTRAP_ADMIN_EMAIL`. On first login of that email, `IdentityResolver.Resolve` grants super_admin inside the JIT provisioning transaction and stamps `bootstrap_state.bootstrapped_at`. Self-disarms forever.

13. **WorkOS last.** Land schema + backend interfaces + Dev validator + sidecar rewrite + business-layer strip first. Plug in WorkOSValidator + AuthKit as the final phase.

## Backend authentication surface

`AuthService` in `authentication.proto` is the only authentication/session
contract. Connect, gRPC, and REST transports are generated from it; handlers do
not maintain an independent route list.

| RPC | REST route | Admission credential |
|-----|------------|----------------------|
| `BeginOAuth` | `POST /v1/auth/oauth/begin` | Public, rate-limited ceremony |
| `Authenticate` | `POST /v1/auth/authenticate` | OAuth code or explicit development fixture token |
| `CompleteMFAChallenge` | `POST /v1/auth/mfa/complete` | One-use MFA login token |
| `BeginWebAuthnMFAChallenge` | `POST /v1/auth/mfa/webauthn/begin` | One-use MFA login token |
| `CompleteWebAuthnMFAChallenge` | `POST /v1/auth/mfa/webauthn/complete` | MFA login token plus one-use ceremony token |
| `RefreshToken` | `POST /v1/auth/refresh` | Opaque refresh credential |
| `SwitchOrganization` | `POST /v1/auth/switch-organization` | Current access token and its verified `sid` |
| `Logout` | `POST /v1/auth/logout` | Opaque refresh credential |
| `GetJWKS` | `GET /v1/auth/.well-known/jwks.json` | Public discovery |

Everything else under `pkg/business/` (users, orgs, teams, invitations, api_keys, audit, etc.) takes `userID`, `orgID`, `orgRole`, `platformRole uuid.UUID / string` as parameters derived from sidecar headers. No auth imports. No JWT parsing. No provider knowledge.

## Hot path (every non-auth request)

```
browser → sidecar
  1. Extract JWT from Authorization header or session cookie
  2. LocalValidator.Validate() — Ed25519 signature check (in-memory pubkey)
  3. Read claims directly: sub, org, or, pr, sid
  4. Forward headers: X-User-ID / X-Org-ID / X-Org-Role / X-Platform-Role / X-Session-ID
  5. Proxy to upstream service
```

No database, no provider API, no policy evaluation. ~200 lines of Go + the validator.

Total request overhead: one Ed25519 verify (microseconds) + one header rewrite. No network, no DB.

## Login path (once per session)

```
browser → WorkOS hosted UI → redirect with code
  frontend → POST /v1/auth/authenticate with OAuth code, signed state, PKCE verifier
  backend Authenticate:
    1. workos.ExchangeCodeForToken(code) → provider access_token
    2. WorkOSValidator.Validate(access_token) → Claims{provider, sub, email}
    3. IdentityResolver.Resolve(ctx, claims):
       a. Upsert provider_identities on (provider, sub)
       b. If new user: create users row + provider_identities row in tx
       c. Load existing org membership + roles
       d. Bootstrap check: if claims.email == BOOTSTRAP_ADMIN_EMAIL
          and bootstrap_state.bootstrapped_at is null,
          grant super_admin + stamp bootstrap_state, all in same tx
       e. Insert sessions row, return Identity
    4. jwt.Mint(identity) → access_token (15m) + refresh_token (7d)
    5. Return both in response body (frontend stores refresh in HttpOnly cookie,
       access in memory)
```

Registration is a separate public `UserService.RegisterUser` operation. It
creates the local user and initial organization; authentication then uses the
same provider-code exchange as every returning user.

## What gets deleted

- `X-Dev-Role` / `X-Dev-User-ID` header bypass → replaced by explicitly configured fixture identity plus a Codefly-selected fixture
- Caller-asserted provider subjects, emails, roles, organizations, or session ids; the server derives all identity and authorization state
- Any `pkg/business/*.go` import of `jwt`, `auth`, or session/token parsing
- Method admission is derived from the protobuf RPC inventory and enforced by deny-by-default Connect/gRPC interceptors. Resource ownership and tenant checks remain in handlers and PostgreSQL RLS; the sidecar only validates credentials and stamps canonical identity.

## Schema migration

New migration in `module/services/store/migrations/`:

```sql
-- provider_identities: (provider, sub) → user_id translation
CREATE TABLE provider_identities (
    id               uuid PRIMARY KEY,
    provider         text NOT NULL,
    provider_sub     text NOT NULL,
    user_id          uuid NOT NULL REFERENCES users(id),
    email            text,
    linked_at        timestamptz NOT NULL DEFAULT now(),
    last_seen_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_sub)
);
CREATE INDEX idx_provider_identities_user ON provider_identities(user_id);

-- sessions: refresh token tracking
CREATE TABLE sessions (
    id                  uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES users(id),
    refresh_token_hash  bytea NOT NULL,
    issued_at           timestamptz NOT NULL DEFAULT now(),
    last_used_at        timestamptz NOT NULL DEFAULT now(),
    expires_at          timestamptz NOT NULL,
    revoked_at          timestamptz,
    user_agent          text,
    ip_address          inet
);
CREATE UNIQUE INDEX idx_sessions_refresh_hash ON sessions(refresh_token_hash)
    WHERE revoked_at IS NULL;

-- orgs gets a nullable link to provider org
ALTER TABLE orgs ADD COLUMN provider_org_id text;
ALTER TABLE orgs ADD COLUMN provider text;
CREATE INDEX idx_orgs_provider_link ON orgs(provider, provider_org_id)
    WHERE provider_org_id IS NOT NULL;

-- bootstrap state (singleton row)
CREATE TABLE bootstrap_state (
    id              smallint PRIMARY KEY CHECK (id = 1),
    bootstrapped_at timestamptz
);
INSERT INTO bootstrap_state (id) VALUES (1) ON CONFLICT DO NOTHING;
```

v7 migration of existing tables is a separate migration, depends on current state and goes first.

## Interface definitions

New package: `module/services/api/code/pkg/auth/`

```go
// claims.go
package auth

import (
    "context"
    "time"
    "github.com/google/uuid"
)

// Claims is what a provider (WorkOS, dev, ...) returns after verifying a token.
// Used only at login/signup/refresh time.
type Claims struct {
    Provider       string    // "workos" | "dev"
    Subject        string    // stable provider user id
    Email          string
    ProviderOrgID  string    // optional
    ExpiresAt      time.Time
}

// validator.go
// TokenValidator verifies a token at the identity provider and returns Claims.
// Runs once per login/signup/refresh. NOT on the hot path.
type TokenValidator interface {
    Validate(ctx context.Context, token string) (*Claims, error)
}

// identity.go
// Identity is the resolved internal state. Becomes the JWT claims.
type Identity struct {
    UserID          uuid.UUID
    OrgID           uuid.UUID
    OrgRole         string
    PlatformRole    string
    SessionID       uuid.UUID
    ActingAsUserID  uuid.UUID  // zero unless impersonating
}

// IdentityResolver translates a provider Claims into an internal Identity,
// performing JIT provisioning and bootstrap checks. Transactional.
// Runs once per login/signup. NOT on the hot path.
type IdentityResolver interface {
    Resolve(ctx context.Context, c *Claims, orgNameOnSignup string) (*Identity, error)
}

// minter.go
// JWTMinter signs an Identity into an access token (short TTL) and optionally
// issues a refresh token stored in the sessions table.
type JWTMinter interface {
    MintAccess(identity *Identity) (string, error)
    MintRefresh(ctx context.Context, identity *Identity) (string, error)
    VerifyRefresh(ctx context.Context, refreshToken string) (*Identity, error)
}
```

Concrete implementations:
- `pkg/auth/workos/validator.go` — WorkOS token verifier (JWKS fetch + cache)
- `pkg/auth/dev/validator.go` — reads dev identity from fixture seed
- `pkg/auth/pg/resolver.go` — Postgres-backed resolver with JIT provisioning + bootstrap
- `pkg/auth/ed25519/minter.go` — JWT mint + verify + refresh storage (single Ed25519 keypair from Vault)

Sidecar-side (module/services/auth-sidecar/code/pkg/auth/):
- `localvalidator.go` — single-purpose Ed25519 JWT validator. Reads pubkey at startup, validates signature + exp on every request. No interfaces, no abstraction. This is the only auth code the sidecar runs on the hot path.

## Security hardening (applied throughout)

State-of-the-art, not "good enough". Every item below lands as part of the phases; no separate "security pass" at the end.

- **JWT**: EdDSA only. `alg: none` rejected. Audience + issuer + exp + nbf + jti validated. 60s clock skew tolerance.
- **Refresh token rotation**: every `/auth/refresh` atomically consumes the presented token, re-resolves current authorization, and inserts one successor. Reuse of an invalidated refresh revokes every active session for the user. Inactive users, removed selected-org memberships, expired refreshes, and MFA-policy rejection commit terminal revocation rather than leaving a reusable credential.
- **Refresh tokens hashed at rest** with SHA-256. Tokens contain 256 random bits, so a fast indexed digest is appropriate; the minter retains a constant-time comparison (`crypto/subtle`) as defense in depth.
- **Cookies**: refresh token in `HttpOnly; Secure; SameSite=Strict; Path=/auth`. Access token in memory only (never localStorage).
- **CSRF**: double-submit token on state-changing requests wherever cookies are in play.
- **Key rotation**: sidecar loads `current` + `previous` Ed25519 pubkeys from Vault. Backend mints with current, sidecar accepts either. Rotation = Vault update, no deploy.
- **Gateway rate limits**: pooled Redis-backed fixed windows use canonical org
  identity for authenticated traffic and trusted client IP otherwise. MFA
  completion has a dedicated 10 req/min/IP budget and each durable MFA
  transaction locks after five rejected factors. Authentication/refresh/MFA
  routes fail closed on limiter-operation errors; local development may use
  the in-memory backend.
- **WebAuthn/passkeys**: registration and assertion options are generated
  server-side with required user verification. `WEBAUTHN_RP_ID` is supplied by
  Codefly's `security` configuration; the exact request origin comes from
  auth-sidecar's SDK-injected endpoint after gateway-token verification.
  `WEBAUTHN_RP_ORIGINS` remains an optional direct-access fallback. Full
  credentials and ceremony state are Vault-encrypted; one-use state,
  authenticator counter updates, and session creation are transactionally
  locked.
- **Generic inbox/outbox foundation**: product-neutral `saas.jobs.v1`
  protobufs generate the shared envelope, scope, lease, failure, attempt, and
  state vocabulary for Go and TypeScript. Migration 72 stores exact payload
  bytes, enforces finite transitions and terminal immutability in PostgreSQL,
  and appends transition history automatically. Generated producer requests
  use collision-free structured ordering keys and deterministic exact-content
  fingerprints. Tenant/subject producers must enqueue inside the surrounding
  business transaction through one scope-checking database operation; request
  traffic has no raw job-table rights. Exact retries return the original job
  identity, conflicting key reuse fails without mutation, and a
  dedicated grant-limited `app_job_worker` role can touch the three job
  relations and no product tables while accepting privileged inbox/global
  ingestion. Generated internal worker commands drive
  atomic queue-scoped `SKIP LOCKED` claims, database-clock leases and
  heartbeats, strict ordering keys, scheduled retry, expired-lease recovery,
  fencing-token finalization, and dead letters. A reusable polling runtime adds
  safe typed failure persistence, arbitrary-error/panic redaction, lease
  heartbeats, queue-only metrics, per-poll/job OpenTelemetry spans, and bounded
  graceful shutdown. Super-admin generated APIs and `/admin/platform/jobs`
  expose payload-free queue/lifecycle history; dead-letter replay requires
  recent MFA, is idempotent and audited, and copies payload only inside
  PostgreSQL through a worker-only operation. Stripe, outbound webhooks, and
  transactional email are generated workload adapters on the platform; later
  workloads remain explicit `P2-JOB-008` onward slices. See `module/JOBS.md`.
- **Stripe webhook inbox**: the public endpoint verifies the signature over the
  unmodified body, encodes a generated versioned protobuf payload, atomically
  enqueues one immutable generic inbox job by Stripe ID, and returns before
  business processing. Multi-replica generic workers use atomic `SKIP LOCKED`
  claims, heartbeats, expiring owner-checked leases, and fencing tokens.
  Arbitrary failures are redacted, retry on a bounded schedule, and become
  visible dead letters after the attempt budget; a duplicate delivery never
  converts a failed event into a false success. Subscription events hydrate the current Stripe object;
  provider-read timestamps and organization-scoped PostgreSQL advisory locks
  prevent reverse completion or a late prior-subscription event from restoring
  stale state. Stripe POSTs require operation-scoped idempotency keys. Browsers
  choose only an enabled catalog key; price, trial, currency, tax policy, and
  exact redirects are server-owned. Checkout/portal creation requires
  organization billing authority and fresh AAL2 evidence. Cross-tenant claims
  run through the generic `app_job_worker` pool. Projection uses a separate
  four-connection `app_billing_worker` pool whose BYPASSRLS role has grants only
  on billing product tables; neither request traffic nor the billing projector
  has direct job-table authority.
- **Outbound webhook outbox**: endpoint keys are 256-bit, Vault-enveloped,
  subscription-bound, revealed only at create/rotate, and dual-signed during a
  bounded rotation overlap. Only normalized public HTTPS/443 endpoints are
  accepted. All IPv4/IPv6 answers are checked at registration and again in the
  IP-pinning dialer; redirects and environment proxies are disabled, while
  Kubernetes egress policy separately denies internal, metadata, special-use,
  and multicast ranges. Audit events and exact-body endpoint rows fan out in
  one transaction with generated generic outbox jobs. Structured subscription
  ordering keys preserve endpoint FIFO while the shared `app_job_worker` owns
  claims, heartbeats, fenced leases, retries, and dead letters. The isolated
  `app_webhook_worker` role can only read endpoint configuration and update
  customer-visible delivery history; it has no job-table authority. Replay
  retains stable event identity. Test and replay RPCs atomically queue generated
  jobs rather than executing a separate synchronous transport. See
  `module/WEBHOOKS.md` for the verification contract.
- **Transactional email outbox**: invitation and magic-link product writes
  commit atomically with generated exact-rendered email jobs under tenant and
  audited pre-authentication scope respectively. Billing events append email
  work by stable Stripe event identity and propagate enqueue failures for
  retry. The generic notification worker is the only production path to the
  provider and owns leases, retries, safe failures, dead letters, replay, and
  shutdown. Template rendering fails on malformed or unresolved variables,
  escapes HTML insertions, and persists immutable bodies. Automatic provider
  retries use the durable job UUID as their idempotency key; an intentional
  replay gets a new key while retaining the exact payload.
- **Audit log** (append-only, `audit_events` table): role grants, impersonation start/stop, session revocation, failed logins beyond threshold, bootstrap_admin activation, refresh reuse detection, JWT signature failures in excess.
- **Zero tokens in logs**: structured logger scrubs any field matching token shapes. Enforced via interceptor.
- **Constant-time** comparisons for all secret-equality checks.
- **HTTP security headers** at gateway: `Content-Security-Policy`, `Strict-Transport-Security`, `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: strict-origin-when-cross-origin`.
- **API keys**: argon2id with pepper. Key format `cfly_sk_<base32 random>`. Only prefix + last 4 stored in plaintext for UX ("cfly_sk_...abcd").
- **Timing**: login and signup responses take constant minimum time (`time.Sleep` to floor) to avoid user-enumeration via timing side channel.

## Test strategy (applied throughout)

Every commit that touches auth lands with tests. No separate test pass.

### Unit tests
- Each `TokenValidator` implementation against fixtures: valid, expired, wrong signature, wrong issuer, wrong audience, `alg: none`, missing claims, malformed.
- `IdentityResolver`: new identity JIT path, existing identity path, bootstrap hit (first call grants super_admin, second call no-ops), race condition (two concurrent first-logins for same identity → one row, one super_admin).
- `JWTMinter`: mint → verify roundtrip. Mint with current key, verify with previous key. Expired access rejected. JTI replay rejected.
- Refresh rotation: mint refresh A, use A → get B + new access, reuse A → detect reuse, revoke session family, log audit event.
- `localvalidator` (sidecar): all negative cases above. Key rotation during verify (previous key still valid).

### Integration tests (real postgres via `WithDependencies`)
- Each `/auth/*` endpoint end-to-end.
- Full login → refresh → authenticated request → logout flow.
- Concurrent refresh from same session → exactly one succeeds.
- Bootstrap flow: clean DB, dev validator, `BOOTSTRAP_ADMIN_EMAIL` matches → super_admin granted + `bootstrap_state.bootstrapped_at` stamped. Second login no-ops.
- Invitations flow end-to-end via the new token path (regression).
- Impersonation: super_admin impersonates user, `X-Acting-As-User-ID` populated, audit log row inserted, stop restores.

### Gateway / sidecar tests
- Public routes allow no-token requests.
- Authenticated routes reject missing/expired/revoked/forged tokens.
- Rate limits return 429 on overflow (per-IP on public, per-user on authenticated).
- Security headers present on all responses.
- Token redaction in logs verified (grep test output for any `cfly_`, `eyJ`, etc.).

### End-to-end
- The existing `dev-admin` scenario still works (regression gate).
- WorkOS path: mock AuthKit callback, verify full flow lands a valid session.

## Current work and verification gates

The original phased authentication migration is complete. Remaining hardening
work is tracked only in `TODO.md`; duplicating an ordered backlog here caused
the implementation and this document to diverge.

Every authentication/session change must pass these Codefly-owned gates:

1. Regenerate protobuf, gRPC, Connect, REST, OpenAPI, and TypeScript bindings
   from `authentication.proto` with the pinned service template.
2. Regenerate the service, authorization, frontend, REST, and gateway catalogs;
   their exact-count and byte-determinism tests must pass.
3. Run the complete accounts test suite with the Go race detector, including
   real-PostgreSQL refresh, revocation, device-cap, authorization-invalidation,
   organization-switch, and switch-versus-refresh concurrency tests.
4. Run the complete auth-sidecar suite so every generated route has matching
   admission metadata and spoofed identity/session headers remain rejected.
5. Run the complete frontend suite, lint, and production compile so every
   tenant-scoped query follows the signed active organization.
6. Exercise the `dev-admin` fixture end to end through the gateway. Production
   provider smoke tests must additionally cover signed OAuth state, PKCE, MFA,
   refresh, organization exchange, and logout.

Use `codefly test`, `codefly lint`, and `codefly compile` for these gates. Do not
encode a second CI implementation in repository-specific workflow scripts.
