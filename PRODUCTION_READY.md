# saas-starter — path to production

Scope of this document: **Tier 1 (ship-blockers)** only. Observability, Sentry, backups, Directory Sync, Admin Portal are out of scope here — separate docs later.

## Architectural decisions (locked)

0. **Gateway is the single ingress.** Every request — including `/auth/login` and `/auth/signup` — enters through the sidecar/gateway. No service is reachable directly. The gateway applies a per-route policy: login/signup public, refresh requires valid refresh token, everything else requires valid access token. Per-IP rate limiting on public routes, per-user on authenticated routes.

1. **Two auth paths, hard-separated.** Login/signup/refresh/logout go through the backend (via the gateway). Every other request goes through the sidecar and only the sidecar. The backend's auth surface area is four endpoints — nothing else.

2. **Our own JWT is the runtime token.** On login/signup, backend mints an Ed25519-signed JWT carrying `sub=user_id`, `org_id`, `org_role`, `platform_role`, `sid=session_id`. Sidecar validates that token on every request — no DB hit, no provider knowledge.

3. **WorkOS is login-only.** WorkOS tokens never enter the runtime path. They're validated exactly once in `/auth/login` or `/auth/signup`, then thrown away. The sidecar has no WorkOS dependency.

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

8. **Token TTL**: access token 15 min, refresh token 7 days. `POST /auth/refresh` is the only endpoint besides the four above that needs session-state access. Stale role propagation window = 15 min, documented and accepted.

9. **`AUTH_PROVIDER=workos|dev`** env var selects the validator used at login. `dev` replaces today's `X-Dev-Role` header bypass with a dev validator reading the fixture seed. The sidecar does not read this env var — it only validates our JWT.

10. **Orgs are canonical in our DB.** `orgs.provider_org_id` is a nullable link to a WorkOS organization for SSO configuration, but not the source of truth.

11. **Invitations stay ours.** Local DB, local email, 7-day hashed tokens — already implemented, keep as-is.

12. **First super_admin bootstrap**: env-gated `BOOTSTRAP_ADMIN_EMAIL`. On first login of that email, `IdentityResolver.Resolve` grants super_admin inside the JIT provisioning transaction and stamps `bootstrap_state.bootstrapped_at`. Self-disarms forever.

13. **WorkOS last.** Land schema + backend interfaces + Dev validator + sidecar rewrite + business-layer strip first. Plug in WorkOSValidator + AuthKit as the final phase.

## Backend auth surface (the ONLY auth endpoints)

```
POST /auth/login      { provider, code }              → { access_token, refresh_token }
POST /auth/signup     { provider, code, org_name }    → { access_token, refresh_token }
POST /auth/refresh    { refresh_token }               → { access_token }
POST /auth/logout     { refresh_token }               → { }
```

Everything else under `pkg/business/` (users, orgs, teams, invitations, api_keys, audit, etc.) takes `userID`, `orgID`, `orgRole`, `platformRole uuid.UUID / string` as parameters derived from sidecar headers. No auth imports. No JWT parsing. No provider knowledge.

## Hot path (every non-auth request)

```
browser → sidecar
  1. Extract JWT from Authorization header or session cookie
  2. LocalValidator.Validate() — Ed25519 signature check (in-memory pubkey)
  3. Read claims directly: sub, org_id, org_role, platform_role, sid
  4. Forward headers: X-User-ID / X-Org-ID / X-Org-Role / X-Platform-Role / X-Session-ID
  5. Proxy to upstream service
```

No database, no provider API, no policy evaluation. ~200 lines of Go + the validator.

Total request overhead: one Ed25519 verify (microseconds) + one header rewrite. No network, no DB.

## Login path (once per session)

```
browser → WorkOS hosted UI → redirect with code
  frontend → POST /auth/login { provider: "workos", code }
  backend /auth/login:
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

Signup is the same shape but creates a new `orgs` row with the user as owner if `org_name` is present and the identity has no existing org.

## What gets deleted

- `X-Dev-Role` / `X-Dev-User-ID` header bypass → replaced by `AUTH_PROVIDER=dev`
- Current `Authenticate` RPC in `pkg/business/auth.go` → replaced by `/auth/login` + `/auth/signup`
- Any `pkg/business/*.go` import of `jwt`, `auth`, or session/token parsing
- Sidecar's current policy evaluation, if any — the sidecar becomes a pure passthrough after JWT check. OPA policy evaluation (if retained) moves into the backend gRPC interceptor, keyed off `X-User-ID` + method name.

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
- **Refresh token rotation**: every `/auth/refresh` mints a new refresh and invalidates the previous. Reuse of an invalidated refresh → revoke the entire session family (OWASP pattern). Detection logged to audit.
- **Refresh tokens hashed at rest** (argon2id with pepper from Vault). Constant-time comparison (`crypto/subtle`).
- **Cookies**: refresh token in `HttpOnly; Secure; SameSite=Strict; Path=/auth`. Access token in memory only (never localStorage).
- **CSRF**: double-submit token on state-changing requests wherever cookies are in play.
- **Key rotation**: sidecar loads `current` + `previous` Ed25519 pubkeys from Vault. Backend mints with current, sidecar accepts either. Rotation = Vault update, no deploy.
- **Per-IP rate limits**: 10 req/min/IP on `/auth/login` + `/auth/signup`. 5 failed logins → 15 min lockout per IP. In-memory, Redis-backed later.
- **Per-user rate limits**: 60 req/min/user on refresh, 300 req/min/user on everything else.
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

## Task list (ordered)

Each row produces a commit. Each is independently testable.

### Phase 1 — foundation (no behavior change)
1. **UUID v7 utility** in `pkg/business/ids.go`; use `uuid.NewV7()` for new rows.
2. **Migration** for `provider_identities`, `sessions`, `orgs.provider_org_id`, `bootstrap_state`. Apply, verify roundtrip.
3. **`pkg/auth/` package skeleton** with `Claims`, `Identity`, `TokenValidator`, `IdentityResolver`, `JWTMinter` interfaces only. No implementations. Compiles as no-op.
4. **Copy current Ed25519 signer into `pkg/auth/ed25519/minter.go`**, wrap with `JWTMinter` interface. Unit test mint → verify roundtrip.

### Phase 2 — dev path end-to-end, no WorkOS yet
5. **Implement `pkg/auth/dev/validator.go`** — returns hardcoded Claims for a fixture-seeded identity.
6. **Implement `pkg/auth/pg/resolver.go`** — JIT provisioning + bootstrap check + session insert, all in one transaction.
7. **Implement the four backend endpoints** `/auth/login`, `/auth/signup`, `/auth/refresh`, `/auth/logout`. Wire to the validator/resolver/minter behind `AUTH_PROVIDER` env var (dev only at this point).
8. **Implement sidecar `localvalidator.go`** — validates our JWT, forwards canonical headers. Delete the current request handler's auth code.
9. **Strip `pkg/business/` of all auth imports.** Every function takes `userID, orgID uuid.UUID` + `orgRole, platformRole string` as parameters. Delete the old `Authenticate` RPC.
10. **Verification gate**: `codefly run service frontend --fixture dev-admin`; log in via dev validator, verify all the RPCs still work end-to-end through the new sidecar + new token path.

### Phase 3 — WorkOS
11. **Implement `pkg/auth/workos/validator.go`** — JWKS cache + token verify + `ExchangeCodeForToken` helper.
12. **Frontend: drop in `@workos-inc/authkit-nextjs`.** Hosted login, callback at `/auth/callback` which POSTs `{ code }` to backend `/auth/login`.
13. **Env config**: `WORKOS_API_KEY`, `WORKOS_CLIENT_ID`, `AUTH_PROVIDER=workos`, `BOOTSTRAP_ADMIN_EMAIL`.
14. **Verification gate**: fresh DB, `AUTH_PROVIDER=workos`, log in via WorkOS hosted UI as `BOOTSTRAP_ADMIN_EMAIL`, verify you end up with `super_admin` and `bootstrap_state.bootstrapped_at` stamped. Log in again, bootstrap path should no-op.

### Phase 4 — production hygiene
15. **Vault wiring** for the Ed25519 signing key. Key rotation plan: the sidecar supports two pubkeys during rotation, backend mints with the new one, old tokens verify against the old one until expiry.
16. **Vault wiring** for API key hash pepper.
17. **Migration path**: up-only, no reset-on-start. Failures halt boot.
18. **Rate limiting per X-User-ID** in sidecar. In-memory token bucket. Redis later if needed.
19. **Structured request logging** correlated to `X-Session-ID`.
20. **CORS + security headers** reviewed per endpoint.

## Verification gates

- **End of Phase 1**: `go build` clean, migration applied, no behavior change. Existing tests pass.
- **End of Phase 2**: `codefly run --fixture dev-admin` works end-to-end. Dev user can log in, create an org, invite another user, accept invitation — all flowing through the new sidecar + backend auth endpoints + local JWT.
- **End of Phase 3**: fresh env, WorkOS login, first login of `BOOTSTRAP_ADMIN_EMAIL` provisions super_admin.
- **End of Phase 4**: k6 load test at 500 rps on sidecar for 5 min, no memory growth, rate limit 429s under overload.
