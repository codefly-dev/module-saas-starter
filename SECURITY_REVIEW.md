# Security review — module-saas-starter

Verified findings from the deep security review of this module, tracked by the
hardening epic (#215). Each finding lists its **location** (file + symbol),
**exploit** (how it is reached and what it yields), **fix** (the remediation),
and **status**. The reconciliation plan, delivery model (app-layer vs. Istio
mesh), and layered test strategy live in
[`SECURITY_HARDENING_PLAN.md`](./SECURITY_HARDENING_PLAN.md).

> **Locations cite the file and the symbol** (function/var), which are stable,
> with the line number as of the review pass in parentheses. Prefer the symbol
> when a line has drifted. Every finding below was confirmed against the code in
> this repository, not inferred from the issue text.

## How to read a severity

- **HIGH** — reachable on a primary request path and yields identity/authz
  bypass, credential smuggling, or a materially extended incident-response
  window. Fix before the next release.
- **MEDIUM** — a real weakness that is either latent (guarded today by another
  layer) or requires a specific misconfiguration/deploy shape to exploit.
- **LOW / hardening** — defense-in-depth, footguns operators can copy, and
  fail-open-on-error paths that should fail closed.

## Summary

| ID | Sev | Finding | Status | Issue |
|----|-----|---------|--------|-------|
| H1 | HIGH | Access-token revocation not enforced on the sidecar (gateway) path | Open | #202 |
| H2 | HIGH | Internal authority gRPC co-hosted on the public HTTP port | Code landed; mesh open | #214 / #203 |
| H3 | HIGH | Connect header-JWT mode does not clear a smuggled client credential | Remediated (in-tree) | #214 |
| H4 | HIGH | Envoy un-restamped trust headers not removed from the upstream request | Remediated (in-tree) | #214 |
| M1 | MED | Signing key falls back to an ephemeral key when Vault is unreachable | Open | #205 |
| M2 | MED | Bootstrap `super_admin` grant does not require a verified email | Open | #206 |
| M3 | MED | OIDC audience binding not enforced when unconfigured | Open | #207 |
| M4 | MED | Identity-resolution errors leak a user-enumeration oracle | Open | #208 |
| M5 | MED | ~~Sidecar↔accounts control channel not mTLS~~ | Closed — subsumed by mesh STRICT (#217) | ~~#209~~ |
| M6 | MED | Header-strip set drifted from the stamped `x-scoped-roles` headers | Remediated (in-tree) | #214 |
| M7 | MED | Product frontend ships no CSP / anti-clickjacking headers | Open | #210 |
| M8 | MED | Anonymous endpoints have no app-layer rate limit; abuse opt-in | Open | #211 |
| M9 | MED | Platform-admin handlers gate only in the business layer | Open | #212 |
| L* | LOW | 18-item hardening backlog (fail-open-on-error, timeouts, SSRF, cookie flags, …) | Open | #213 |

---

## HIGH

### H1 — Access-token revocation is not enforced on the browser (gateway) path

- **Location:** `module/services/auth-sidecar/code/sidecar.go` — `checkJWT`
  (`:139`). The sidecar validates the session JWT with pure local Ed25519 crypto
  and holds **no revoker**. Revocation is only consulted in the accounts
  `Minter.VerifyAccess` (`module/services/accounts/code/pkg/auth/ed25519/minter.go:495`),
  which runs on the **direct-to-accounts fallback** path — not the gateway path,
  where accounts trusts the sidecar-stamped identity headers. Logout
  (`RevokeAccess`, `module/services/accounts/code/pkg/business/auth.go`) and the
  admin session-kill populate the revocation set, but do not affect an
  already-issued access token on the browser→gateway→sidecar path.
- **Exploit:** "kill this compromised session now" is deferred by up to the
  access-token TTL — default **15 min** (`AccessTokenTTL`, `minter.go:59,85`;
  impersonation 5 min). The refresh token is revoked immediately (no *new*
  access tokens), which bounds but does not close the window: a stolen access
  token keeps working on the primary path until natural expiry.
- **Fix:** Add a revoker to the sidecar and consult it in `checkJWT` after
  signature/claim validation, keyed on the JWT `jti`, backed by the same Redis
  revocation set accounts writes, fronted by a short-TTL (1–5 s) local cache.
  Decide the failure mode explicitly (fail-closed for high-assurance routes, or
  a documented ≤N-second cache window — never silent fail-open). Cut
  `AccessTokenTTL` (e.g. 15 m → 3 m) so even the un-checked window is small.
- **Proof:** login → protected RPC (200) → logout → immediately reuse the old
  access token → **must 401** (fails today). See also the `IsRevoked`
  fail-open note under LOW.
- **Status:** Open — #202. Test layer: [Plan Part C Layer 4](./SECURITY_HARDENING_PLAN.md).

### H2 — Internal authority gRPC is co-hosted on the public HTTP port

- **Location:** `module/services/accounts/code/pkg/adapters/rest_gen.go` —
  `multiplexInternalGRPC` (`:93`). Any HTTP/2 request with
  `Content-Type: application/grpc` on `EndpointHttpPort` (`:95`) is routed to the
  internal gRPC server, **bypassing the tenant listener's interceptor**.
- **Exploit:** the internal authority RPCs (`CheckPermission`, `CheckAccess`,
  `Decide`, `ResolveIdentity`, `ValidateAPIKey`, `GetPrincipal`,
  `GetAgentPrincipal`, `ConsumeUsage`) were reachable from the public port with
  only a bare/any JWT, protected by a single shared internal secret.
- **Fix (two halves):**
  1. **Code (landed):** the ungated oracle handlers now require
     `requireInternalCredential` (`module/services/accounts/code/pkg/adapters/auth.go:434`),
     `ConsumeUsage` is gated, and `Decide` is org-bound
     (`requireInternalOrOrgMember`). Tracked for integration verify by #214.
  2. **Mesh (open, #203):** an Istio `AuthorizationPolicy` on the accounts
     workload that ALLOWs the internal method paths only from the allowlisted
     caller service accounts and DENYs them from the ingress-gateway SA — a
     *reach* gate by workload identity, valid even while the surface is
     multiplexed on the shared port. Istio proves *which service*; the app
     credential proves *internal vs. tenant*.
- **Status:** Code landed (#214); mesh reach gate open (#203, depends on #217).

### H3 — Connect header-JWT mode does not clear a smuggled client credential

- **Location:** `module/services/accounts/code/pkg/adapters/connect_handlers.go`
  — `injectHeaderJWTCredential` (`:400`).
- **Exploit:** in header-JWT (perimeter-decoded) mode the handler set the
  identity from the trusted header but did not clear a client-supplied
  `req.Authentication`, so a caller could smuggle a second credential in the
  request body alongside the trusted header.
- **Fix (landed):** `injectHeaderJWTCredential` now clears `req.Authentication`
  before setting it from the trusted header (`:408`). Negative test:
  `TestInjectHeaderJWTCredential/absent_header_drops_a_smuggled_client_credential`.
- **Status:** Remediated in-tree; integration verify tracked by #214.

### H4 — Envoy un-restamped trust headers are not stripped from the upstream request

- **Location:** `module/services/auth-sidecar/code/sidecar.go` — `allow` (`:234`).
- **Exploit:** on an `allow` decision the sidecar stamped its own trust headers
  but did not instruct Envoy to remove client-supplied trust headers it chose
  *not* to restamp, so a spoofed trust header could survive to the upstream.
- **Fix (landed):** `allow` now sets `OkHttpResponse.HeadersToRemove` for every
  un-restamped trust header (`sidecar.go:259`). Negative test:
  `TestUnit_Allow_RemovesUnstampedTrustHeaders`. This pairs with M6 (the strip
  set must be a superset of the stamped set — the header-lockstep invariant).
- **Status:** Remediated in-tree; integration verify tracked by #214.

---

## MEDIUM

### M1 — Signing key silently falls back to an ephemeral key

- **Location:** `module/services/accounts/code/work.go` — `loadSigningKey`
  (`:1294`); ephemeral generation at `:1307` after a Vault-load warning
  (`:1305`).
- **Exploit:** a transient Vault blip at boot makes each replica sign with a
  different key → intermittent auth failures, existing sessions become
  unverifiable, and the sidecar's pinned public key stops matching:
  **fail-open-to-broken** instead of fail-closed. The same seed feeds the
  OAuth-state signer and the permissions plugin.
- **Fix:** in a production profile, **refuse to boot** if the Vault key load
  fails (no ephemeral fallback); restrict the ephemeral path to an explicit
  dev/fixture mode; prefer KMS / Vault Transit so the private key never sits in
  process memory.
- **Status:** Open — #205. See [Plan Part A](./SECURITY_HARDENING_PLAN.md).

### M2 — Bootstrap `super_admin` grant does not require a verified email

- **Location:** `module/services/accounts/code/pkg/auth/pg/resolver.go` —
  `bootstrapOrLoadPlatformRole` (`:918`). It matches `BOOTSTRAP_ADMIN_EMAIL`
  against a plain email with **no `EmailVerified` gate**, unlike the invite
  (`:486`), waitlist (`:534`), and SSO-JIT (`:746`) paths, which all check
  `c.EmailVerified`.
- **Exploit:** on a fresh deploy with open signup and a generic OIDC IdP that
  asserts an unverified email, an attacker who knows/guesses the bootstrap
  address and logs in before the operator claims full platform `super_admin`
  (one-shot race).
- **Fix:** thread the caller's `Claims` (or an `emailVerified bool`) into
  `bootstrapOrLoadPlatformRole` and refuse the grant when the email is
  unverified — exactly as the sibling email-authorized paths do.
- **Status:** Open — #206.

### M3 — OIDC audience binding is not enforced when unconfigured

- **Location:** `module/services/accounts/code/pkg/auth/oidc/validator.go` —
  `jwt.WithAudience` is appended only if `cfg.Audience != ""` (`:143`), and
  `ClientIDClaim` is the only alternative binding. The Clerk preset
  (`presets.go`) sets neither; a generic `oidc` stack with `IDENTITY_AUDIENCE` /
  `IDENTITY_CLIENT_ID_CLAIM` unset is likewise unbound.
- **Exploit:** an unbound validator accepts **any** correctly-signed, unexpired
  token from the issuer regardless of relying party (audience-confusion on a
  shared IdP tenant). Residual exploitability is limited by the
  `client_secret` + PKCE code exchange, but it is a latent footgun operators can
  copy from the presets.
- **Fix:** require at least one audience binding (`Audience`, or
  `ClientIDClaim` + `ClientID`) at `oidc.New` — **fail construction closed** if
  none is provided; make the Clerk/Okta presets demand a binding.
- **Status:** Open — #207.

### M4 — Identity-resolution errors leak an enumeration oracle

- **Location:** `module/services/accounts/code/pkg/adapters/rpcs.go` —
  `Authenticate` (`:834`) returns `ErrNoAccount` / `ErrAccountInactive` /
  `ErrSignupNotAllowed` / invitation-mismatch verbatim through
  `translateGRPCError`, despite
  `module/services/accounts/code/pkg/auth/errors.go` documenting *"Never leak
  these strings … produce a generic 'invalid credentials'."*
- **Exploit:** an unauthenticated caller can distinguish "no account" vs.
  "inactive" vs. "not invited" — a user-enumeration / account-state oracle.
- **Fix:** collapse credential/identity-resolution failures at the
  `Authenticate` boundary to a single generic error + code (`Unauthenticated`,
  "invalid credentials"); keep the detailed sentinel only in logs/audit.
- **Status:** Open — #208. Test layer: [Plan Part C Layer 1](./SECURITY_HARDENING_PLAN.md).

### M5 — Sidecar↔accounts control channel not mTLS — **closed**

Subsumed by the Istio baseline: mesh-wide STRICT `PeerAuthentication` (#217)
makes the sidecar↔accounts JWKS/key channel mTLS by construction, and the
assert-STRICT coverage gate (#204) guarantees no `PERMISSIVE` regression.
Tracked historically as #209 (closed).

### M6 — Header-strip set drifted from the stamped headers

- **Location:** `module/services/auth-sidecar/code/gateway.go` —
  `untrustedAuthHeaders` (`:396`).
- **Exploit:** the sidecar stamps `x-scoped-roles` / `x-scoped-roles-truncated`
  (`sidecar.go:196,202`) but the untrusted-header strip list did not include
  them, so a client could spoof scoped-role headers on the ingress edge.
- **Fix (landed):** `x-scoped-roles` and `x-scoped-roles-truncated` were added to
  `untrustedAuthHeaders`. Lockstep guard test:
  `TestUntrustedHeaders_SupersetOfStampedHeaders` — the strip set must be a
  superset of everything the sidecar stamps. Wiring this test into CI is the
  header-lockstep coverage gate (#204).
- **Status:** Remediated in-tree; integration verify tracked by #214.

### M7 — Product frontend ships no CSP / anti-clickjacking headers

- **Location:** `module/services/frontend/code/next.config.mjs` sets **no**
  security headers, while `module/services/marketing/code/next.config.mjs` sets
  a full CSP + `X-Frame-Options: DENY` + `frame-ancestors 'none'` + COOP +
  `Referrer-Policy` + `nosniff` (verified: marketing has the headers, frontend
  has none).
- **Exploit:** the logged-in dashboard is framable → clickjacking of
  org-switch, "Stop Impersonating", member removal, webhook-secret rotation,
  and admin flows. Absent CSP also removes defense-in-depth for the Module
  Federation surface, which executes remote solution scripts in-origin
  (`SolutionOutlet.tsx`).
- **Fix:** add an `async headers()` block to the frontend config mirroring
  marketing (`X-Frame-Options: DENY` / CSP `frame-ancestors 'none'`,
  `Referrer-Policy`, `nosniff`, COOP) with a CSP whose `script-src` / `connect-src`
  allowlist the registered solution-manifest origins.
- **Status:** Open — #210. Test layer: [Plan Part C Layer 4](./SECURITY_HARDENING_PLAN.md).

### M8 — Anonymous endpoints have no app-layer rate limit; abuse is opt-in

- **Location:**
  - `module/services/accounts/code/pkg/adapters/rate_limit_interceptor.go` —
    `rateLimitKeyFromCtx` (`:145`) returns `""` for callers with no verified
    identity (`:159`); the limiter is then **skipped**, so `Authenticate` /
    `BeginOAuth` / `RegisterUser` / `JoinWaitlist` / magic-link have no
    app-layer budget.
  - `module/services/accounts/code/work.go` — abuse protection defaults to
    `DisabledVerifier` when `ABUSE_PROTECTION_MODE` is unset (`:733`).
  - `module/services/accounts/code/pkg/business/magic_links.go` — `SendMagicLink`
    has no `abuseVerifier.Verify` call.
  - `module/services/accounts/code/pkg/cache/rate_limiter.go` — non-atomic
    `Get`+`Set` (`:63`) rather than `INCR`.
- **Exploit:** if the CDN/gateway throttle is absent or misconfigured →
  email-bombing (Resend cost amplification) and enumeration/credential-stuffing
  against the anonymous surface.
- **Fix:** derive a per-IP key when identity is absent (don't skip the limiter;
  require/validate `TRUSTED_PROXY_CIDRS` behind a proxy); make Turnstile
  default-on or emit a loud boot warning; add `abuseVerifier.Verify` to
  `SendMagicLink`; switch the limiter to atomic `INCR`+`EXPIRE`. The
  ingress-side per-IP budget is delivered as Istio ingress rate-limiting (#211).
- **Status:** Open — #211. Test layer: [Plan Part C Layer 6](./SECURITY_HARDENING_PLAN.md).

### M9 — Platform-admin handlers gate only in the business layer

- **Location:** `module/services/accounts/code/pkg/adapters/rpcs.go` — 14
  `PlatformAdminService` handlers (`:1252`–`:1506`: `SearchUsers`,
  `SuspendUser`, `UnsuspendUser`, `ImpersonateUser`, `ListActiveSessions`,
  `RevokeSession`, `GrantPlatformRole`, `RevokePlatformRole`,
  `ListPlatformAdmins`, `ListFeatureFlags`, `GetJobOperations`, `ListJobs`,
  `GetJob`, `ReplayJob`) gate only on `requireAuth`; the platform-role check
  lives only in the business layer.
- **Exploit:** **not exploitable today** — each business method opens with
  `requirePlatformRole` (verified default-deny). But the two layers are unbound
  and inconsistent with siblings that gate in the handler (`OverrideEntitlement`,
  `UpsertFeatureFlag`): dropping one business check would silently expose a
  platform-admin endpoint. Minor: `ImpersonateUser` runs `requireMFA` *before*
  the role check, probing a non-admin's MFA state before denial.
- **Fix:** add an explicit `requirePlatformRole` in each of the 14 handlers
  (defense-in-depth); reorder `ImpersonateUser` so the role check precedes
  `requireMFA`. This is the first customer of the RBAC-coverage CI gate (#204).
- **Status:** Open — #212. Test layer: [Plan Part C Layer 2 / Layer 5](./SECURITY_HARDENING_PLAN.md).

---

## LOW / hardening

Independent backlog items — each is defense-in-depth, a fail-open-on-error path
that should fail closed, or an operator footgun. Rolled up as #213; two move to
the mesh (#217).

| # | Item | Location | Fix |
|---|------|----------|-----|
| 1 | Revocation fail-open on cache error | `pkg/auth/revocation.go:23`, `pkg/cache/token_revoker.go:42` | `IsRevoked` returns `false` on Redis error; fail-closed for high-assurance tenants (ties to H1). |
| 2 | No HTTP server timeouts on the gateway | `auth-sidecar/main.go:212` | Set `ReadHeaderTimeout` / `ReadTimeout` / `WriteTimeout` / `MaxHeaderBytes` (slowloris). → mesh mitigation via #217. |
| 3 | Non-atomic rate limiter | `pkg/cache/rate_limiter.go:63` | Use `INCR`+`EXPIRE` / Lua (ties to M8). |
| 4 | Rate-limit XFF attribution | `auth-sidecar/ratelimit.go:194` | Require/validate `TRUSTED_PROXY_CIDRS` behind a proxy. |
| 5 | Vault `HashKey` silent SHA-256 downgrade | `pkg/infra/vault.go:76` | Fail closed instead of falling back on Vault error. |
| 6 | `SlackNotifier` unguarded outbound HTTP (latent SSRF) | `pkg/business/slack.go:19` | Route through the hardened `WebhookEndpointPolicy` client. |
| 7 | OAuth `state` replayable within TTL | `pkg/auth/oauth_state.go` | Add a Redis one-shot nonce, or bind to the PKCE challenge. |
| 8 | OIDC `nonce` never validated | `oidc/exchanger.go`, `oidc/validator.go` | Generate + assert nonce (matters multi-provider). |
| 9 | `OAuthStateSigner` random fallback key | `oauth_state.go:41` | Make an empty seed a hard startup error in non-dev builds. |
| 10 | Vault key fetch over plaintext HTTP | `pkg/auth/ed25519/vault_key.go` | Reject non-loopback `http://` Vault addresses. |
| 11 | `DeleteRole` no org scope | `rpcs.go:538` → `business/permissions.go:45` | Add `org_id` scoping (TODO already at `rpcs.go:549`). |
| 12 | Role/scope assignment object-id binding | `rpcs.go:560` / `:666` | Apply `requireVisibleRole` on the assignment path. |
| 13 | API-key modulo bias | `business/api_keys.go:209` | Full-int base62 or rejection sampling. |
| 14 | Unauthenticated `/metrics` + gRPC reflection | `telemetry_metrics.go:111`, `auth-sidecar/main.go:133`, accounts `grpc_gen.go:209` | Restrict `/metrics`; gate reflection to non-prod. → mesh mitigation via #217. |
| 15 | OpenAPI route `Access-Control-Allow-Origin: *` | `frontend/.../api/openapi/route.ts:14` | Drop or scope to same-origin. |
| 16 | `codefly_session` cookie not `Secure` / `SameSite=Lax` | `frontend/.../lib/auth.tsx:242` | Append `; Secure` on HTTPS. |
| 17 | Dev/fixture provider selectable by env | `work.go:914,1081` | Hard-refuse when a production profile selects `IDENTITY_PROVIDER=dev`/`fixture`. |
| 18 | `headerjwt` `AllowedAlgs` not enforced in perimeter-decode | `headerjwt/validator.go:155` | Harmless but misleading — document or remove the field. |

---

## Module strengths (kept as the backstop)

The review confirmed several properties this module already does **better** than
the platform it reconciles with; the plan keeps them as independent
defense-in-depth rather than replacing them:

- **Postgres RLS as a physical L3 backstop** — `ENABLE` + `FORCE` row-level
  security, so a bypassed app-layer gate still cannot cross a tenant boundary.
- **Constant-time trust boundary** on the internal shared secret.
- **PKCE** on the OAuth code exchange; **refresh-token rotation with reuse
  detection**.
- **DNS-rebind-safe SSRF defense** on the hardened webhook client.
- **Header-lockstep invariant** — the untrusted-strip set is a proven superset
  of the stamped set (M6 test), so nothing the sidecar stamps can be spoofed in.

See [`SECURITY_HARDENING_PLAN.md`](./SECURITY_HARDENING_PLAN.md) for how these
combine with the adopted platform strengths (short-TTL + `jti`, coverage-as-CI,
reach/identity separation).
