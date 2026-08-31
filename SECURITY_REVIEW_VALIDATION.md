# Security review validation — issue #225

Validation pass over the findings register in issue #225, executed cold against
`main` @ `3922317` (2026-08-23). Method: every register entry re-verified by
reading the current code at the cited symbol (line numbers below are as of this
commit), and the charter's build/test commands re-run. Where this document and
the code disagree, the code wins.

## Executive verdict

Every HIGH and MEDIUM finding is **fixed and merged** (the charter's ⏳ column
is stale — #202, #203, #205–#212 all landed via PRs #216–#231). One HIGH is
**partial**: H2c's mesh reach gate is real and test-asserted but
namespace-granular, not workload-granular (no per-service ServiceAccounts).
All seven "verified strong" control groups **hold** — no regressions found
anywhere. Of the 18 LOW items, 10 are fixed, 2 are partial (items 4 and 14),
and 6 remain open (two of those deliberately: one deferred behind a proto
change, one documented won't-fix).

## Command results

All commands from the charter pass (Docker-backed integration/e2e suites not
run — no Docker in the validation sandbox):

| Command | Result |
|---|---|
| `auth-gateway: go vet ./... && go test ./...` | ok (2.9s) |
| `accounts: go vet + go test ./pkg/adapters/ ./pkg/business/` | ok (0.7s / 28.4s) |
| module root: `go vet . && go test .` (gitops mesh assertions) | ok (1.9s) |
| `TestUntrustedHeaders_SupersetOfStampedHeaders` | PASS |
| `TestUnit_Allow_RemovesUnstampedTrustHeaders` | PASS |
| `TestGateway_StripsCallerIdentityAndTrustCredentials` | PASS |
| `TestInjectHeaderJWTCredential` (5 subtests, incl. smuggled-credential drop) | PASS |
| `TestRequireInternalCredential_RejectsTenantCallers` (3 subtests) | PASS |

The module-root package `go:embed`s gitignored build artifacts
(`module/services/*/.cache/`); on a fresh clone the root row fails to
**compile** until a codefly build regenerates them — a build error there is
environmental, not a regression.

## HIGH findings

| ID | Expected | Actual | Verdict |
|---|---|---|---|
| H1 (#202) | jti revocation enforced in sidecar `checkJWT`; short TTL | Revoker consulted after claim validation (`sidecar.go:196-217`), **fail-closed by default** (503 on store error; opt-out only via `SIDECAR_REVOCATION_FAIL_OPEN`), logout-route cache bypass, `revoked-jti:` key contract matches the accounts writer byte-for-byte. TTL 15m→3m (`minter.go:62,89`). Unit + real-stack logout-then-reuse tests pass. | **FIXED** |
| H2a (#214) | Oracle RPCs require internal credential | `requireInternalOrAuth` retired (zero code references). All six oracles gated by `requireInternalCredential` (`auth.go:439`), plus `AuthorizeEvidenceRead`/`CheckAuthorizationRevision`. No in-repo caller invokes `Decide` at all, so no bare-JWT caller exists. | **FIXED** |
| H2b (#214) | `ConsumeUsage` gated | Gate at `usage_rpcs.go:29` before any service call; dedicated test plus the shared rejects-tenant-callers test. | **FIXED** |
| H2c (#203) | Per-service SAs + method-level AuthorizationPolicy | Method-level `deny-<target>-internal-authority` DENY policy generated from `authz-methods.json` (all 10 `EXPOSURE_INTERNAL` paths), waypoint fronting for L7 enforcement, ingress-gateway SA explicitly not exempt — all asserted in `gitops_test.go:758-855`. **But** the allowlist is the single shared `cluster.local/ns/<ns>/sa/default` (`gitops.go:1194,1268`); no ServiceAccount objects are emitted, so the reach gate is namespace-, not workload-granular. App-layer `requireInternalCredential` remains the identity boundary (acknowledged at `gitops.go:1253-1256`). | **PARTIAL** |
| H3 (#214) | `header_jwt` smuggling blocked | Unconditional `req.Authentication = nil` at `connect_handlers.go:408` precedes the trusted-header read; smuggling subtest passes. | **FIXED** |
| H4 (#214) | Trust headers stripped on allow | `allow()` emits `HeadersToRemove` = untrusted − stamped (`sidecar.go:309-325`); `x-codefly-public-origin` and `x-codefly-internal-token` always removed; enforced on all three surfaces (ext_authz, Envoy route config, Go gateway). | **FIXED** |

## MEDIUM findings

| ID | Expected | Actual | Verdict |
|---|---|---|---|
| M1 (#205) | Signing key fails closed outside dev/fixture | `loadSigningKey(ctx, allowEphemeral)` errors on Vault failure or missing Vault config unless dev/fixture (`work.go:1343-1360`); dev/fixture itself refused outside `codefly.IsLocal()` (`work.go:243-245,1323`). Tests pass. | **FIXED** |
| M2 (#206) | Bootstrap super_admin requires verified email | `bootstrapOrLoadPlatformRole` takes `emailVerified`; unverified returns before the slot claim, so the bootstrap slot is neither granted nor consumed (`resolver.go:960-962`). Postgres-backed test passes. | **FIXED** |
| M3 (#207) | OIDC audience binding required | `oidc.New` fails construction without `Audience` or `ClientIDClaim`+`ClientID` (`validator.go:138-140`); wrong/absent aud rejected on both enforcement paths; all presets and all construction sites carry a binding. | **FIXED** |
| M4 (#208) | Generic Authenticate errors | 28-sentinel `authenticateOracleErrors` set collapses to `Unauthenticated`/"invalid credentials" (`rpcs.go:853-927`); `ErrGroupNotAllowed`→`PermissionDenied`, JWKS→`Unavailable` with generic messages; dev detail-reveal keeps the gRPC code identical. Byte-identical-error tests pass. | **FIXED** |
| M5 (#209) | Subsumed by Istio STRICT | Channel still plaintext at app layer by design (`main.go:112-117`); mesh-wide `PeerAuthentication` STRICT + namespace default-deny + ambient generated (`gitops.go:960-971,798`) and asserted incl. a raw `PERMISSIVE` scan (`gitops_test.go:659-755`). | **HOLDS** |
| M6 (#214) | Strip set ⊇ stamp set | `untrustedAuthHeaders` includes `x-scoped-roles*`; lockstep guard test passes. | **FIXED** |
| M7 (#210) | Frontend CSP + anti-clickjacking | Full header set incl. `frame-ancestors 'none'` + `X-Frame-Options: DENY` via `server/security-headers.mjs`, MF-origin allowlist limited to `script-src`/`connect-src`, unit + e2e tests. | **FIXED** |
| M8 (#211) | Anonymous throttling, Turnstile, magic-link, atomic limiter | Anonymous requests fall through to a per-IP key with trusted-proxy XFF handling; limiter is atomic Lua INCR with a concurrency test; `SendMagicLink`/`RegisterUser`/`JoinWaitlist` abuse-gated; edge limiter has a dedicated per-IP MFA budget and fail-closed auth-class routes. Turnstile remains default-off, promoted to a loud boot warning + fail-closed misconfig checks. | **FIXED** |
| M9 (#212) | Handler-layer platform-role gates | All 14 `PlatformAdminService` handlers call `requirePlatformRole` (support/super_admin per method); `ImpersonateUser` role check precedes the MFA probe (test asserts MFA is never probed for non-admins); nil-store test design makes gate fall-through panic. | **FIXED** |

## LOW / hardening (#213)

| # | Item | Verdict | Evidence |
|---|---|---|---|
| 1 | Revocation fail-open on cache error (accounts) | **STILL-OPEN** | `pkg/auth/revocation.go:20-34` still bool-only fail-open; `token_revoker.go:50` computes and discards the error classification. Sidecar path (#202) is fail-closed; accounts in-process verify never got parity. |
| 2 | Gateway HTTP timeouts | **FIXED** | `main.go:239-242` (ReadHeader/Read/Idle/MaxHeaderBytes); `WriteTimeout` deliberately omitted for the 5-min `WaitForDelegation` stream, documented in-code. |
| 3 | Non-atomic rate limiter | **FIXED** | `rate_limiter.go:63` atomic Lua INCR+PEXPIRE; concurrency regression test. |
| 4 | XFF/trusted-proxy attribution | **PARTIAL** | Spoofing defense correct on both services (XFF ignored from untrusted peers). Gap: sidecar `newProxyTrust` silently drops malformed CIDRs (accounts fails boot); nothing requires the setting behind a proxy, and shipped configs leave it empty (per-IP budgets collapse to one bucket behind an LB). |
| 5 | Vault `HashKey` SHA-256 downgrade | **FIXED** | `vault.go:85-92` fails closed; 3 tests. |
| 6 | SlackNotifier SSRF | **FIXED** | Uses `NewWebhookEndpointPolicy().HTTPClient()` (pinned-IP dial, 443-only, no redirects); SSRF-block test. |
| 7 | OAuth state replayable in TTL | **STILL-OPEN** | State remains stateless HMAC; nonce minted but never stored/consumed (`oauth_state.go:99-128`), acknowledged in-code. Bounded by 10-min TTL + provider one-time code. |
| 8 | OIDC nonce | **STILL-OPEN** | No `nonce` anywhere in the OIDC package or OAuth path; partially compensated by the M3 audience binding. |
| 9 | `OAuthStateSigner` fallback key | **FIXED** | Empty seed is a hard error in all builds (`oauth_state.go:47-55`); boot fails closed. |
| 10 | Vault key fetch over plaintext http | **STILL-OPEN** | `vault_key.go:48-68` has no scheme/host validation; token + private seed would travel cleartext over an `http://` address. |
| 11 | `DeleteRole` org scope | **STILL-OPEN (deferred)** | Gate is `requirePlatformAdmin` only; in-code TODO defers org scoping behind adding `org_id` to the proto. Blast radius limited to platform admins. |
| 12 | Role/scope object-id binding | **FIXED** | `requireVisibleRole` under `WithOrgTx` on AssignRole/GrantScope/ShareRecord; GrantScope also validates the scope node. |
| 13 | API-key modulo bias | **FIXED** | Rejection sampling in `randomBase62` (`api_keys.go:207-233`) + distribution test. |
| 14 | Unauth `/metrics` + gRPC reflection | **PARTIAL** | Sidecar reflection now local-only (`main.go:151-156`). Accounts reflection still unconditional on the public server (`grpc_gen.go:209` — generated file, needs a generator change). `/metrics` still unauthenticated on both services; reach is mesh-limited (in-mesh exposure only), no path-level policy. |
| 15 | OpenAPI wildcard CORS | **FIXED** | Header removed from `api/openapi/route.ts` (no regression test). |
| 16 | `codefly_session` cookie flags | **FIXED** | `SameSite=Lax` always, `Secure` when served over https (`auth.tsx:263-272`); JS presence-hint only, never trusted server-side. |
| 17 | Dev/fixture provider via env | **FIXED** | `requireLocalForDevFixtureProvider` hard-fails startup outside `codefly.IsLocal()` (`work.go:243,1318-1328`). |
| 18 | `headerjwt` AllowedAlgs in perimeter-decode | **STILL-OPEN (documented won't-fix)** | `WithValidMethods` is a no-op for `ParseUnverified`, so it was removed with an explanatory comment (`validator.go:155-170`); exp/aud/iss still enforced; the verified path enforces the alg twice. If the intent was to reject unsigned tokens under perimeter trust, that is not implemented. |

## Regression checks — verified-strong controls

All seven groups **HOLD**; no weakenings found.

| Group | Verdict | Key evidence |
|---|---|---|
| Crypto/tokens | HOLDS | EdDSA `WithValidMethods` + keyfunc re-check on minter and sidecar; iss/aud/exp/`WithExpirationRequired`; kid fallback always a real key (JWKS validators hard-fail on missing/unknown kid); `subtle.ConstantTimeCompare`/`hmac.Equal` throughout. |
| Refresh rotation | HOLDS | `FOR UPDATE` single consumption; reuse → user-wide revocation committed before the sentinel surfaces; `validateRefreshReplacement` rejects any absolute-lifetime slide, family/user/device change. |
| RLS (L3) | HOLDS | `app_tenant` NOLOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS (migration 61); `SET ROLE` per checkout / `RESET` on release with connection destruction on failure; every RLS relation `ENABLE`+`FORCE`; `app.bypass` gone from live policies (migration 68); empty GUC → NULL → 0 rows; `set_config(..., true)` tx-local; `users_select` own-row-only (migration 69). |
| L1↔JWT coupling | HOLDS | All org gates funnel through `RequireVerifiedDatabaseScope` before cache or DB; unexported context key; forwarded identity stripped unless the constant-time gateway token passes; listener-class cross-refusal intact. |
| RBAC default-deny | HOLDS | `pgx.ErrNoRows` → false; strict NULL scope/org semantics; built-in role deletion refused; global/foreign-org role writes blocked by policy and by `requireVisibleRole`. |
| Webhooks/SSRF/secrets | HOLDS | Stripe HMAC over raw body + ±300s window + API re-fetch; Resend Svix over raw size-capped body; pinned-IP DNS-rebind-safe dialer, 443-only, redirects disabled; 32-byte `crypto/rand` hashed single-use tokens; Vault Transit envelope with purpose binding and no plaintext fallback. |
| Frontend | HOLDS | Zero XSS sinks; redirect sanitizers; PKCE S256 + server-signed state re-validated server-side; refresh cookie `HttpOnly` `SameSite=Strict` `Secure` scoped to `/v1/auth`; plugin BFF same-origin + strict path allowlist + streamed size caps. |

## Drift flags (code wins)

- **Charter status column stale**: every ⏳ HIGH/MEDIUM item has merged
  (#216–#231); only this validation issue remains open from the epic.
- **`Decide` gate stricter than documented**: the charter and
  `SECURITY_HARDENING_PLAN.md` (§B.3, pass-1 table) describe
  `requireInternalOrOrgMember` / "org-bound"; the shipped gate is
  `requireInternalCredential` only (`principal_rpcs.go:167`), and
  `requireInternalOrOrgMember` does not exist. Stricter is fine — but the plan
  doc should be reconciled so a later "restore the org-member path" cleanup
  doesn't reopen the hole. The verify-before-merge check passes for this repo —
  no in-repo caller invokes the `Decide` RPC — but external solution runtimes
  attached through the host seam were not audited; one calling `Decide` with a
  bare tenant JWT now fails closed (`Unauthenticated`) rather than being
  authorized, so any breakage would be functional, not a security exposure.
- **`SECURITY_REVIEW.md` LOW table stale**: still lists all 18 items as a
  pending #213 rollup; actual state is the table above.
- **Dead legacy minter**: `pkg/infra/jwt.go` still carries a 15-minute,
  no-jti `TokenService` with zero callers — worth deleting so it can't be
  re-wired.
- **Stale comments**: `postgres.go`/`tenant_tx.go` comments say `BeforeAcquire`
  where the code uses `PrepareConn` (behavior correct).

## Residual open items (priority order)

1. **H2c granularity** — emit per-service ServiceAccounts and narrow the
   internal-authority allowlist from `sa/default`; the current gitops test
   locks in the shared-SA shape (explicit tripwire, but also inertia).
2. **LOW 1** — bring the accounts in-process `IsRevoked` to fail-closed parity
   with the sidecar (interface needs an error return); at minimum log the
   discarded error at `token_revoker.go:50` so a Redis outage is observable.
3. **LOW 14** — gate accounts gRPC reflection behind `codefly.IsLocal()` (via
   the generator) and put `/metrics` behind auth or a non-public listener on
   both services.
4. **LOW 10** — reject non-loopback plaintext `http://` Vault addresses in
   `LoadKeyFromVault` (already a named target in the hardening plan §A).
5. **LOW 7 + 8** — one-shot OAuth state consumption and OIDC nonce.
6. **Mesh test gap** — assert the ingress-allow `principals` value in
   `gitops_test.go` (today only ports are asserted; a widened allowlist would
   ship green).
7. **M8 edges** — magic-link REST routes fail open on limiter-backend outage
   (non-protobuf extensions skip the auth-class metadata); `RateLimitClass`
   budgets are carried but not consumed by the edge limiter; no test covers
   `SendMagicLink`'s abuse gate; Turnstile default remains off (warning only).
8. **LOW 4 config half** — sidecar should fail boot on malformed
   `TRUSTED_PROXY_CIDRS` like accounts does.
