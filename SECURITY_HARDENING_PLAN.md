# Security hardening plan — reconcile saas-starter × obin-platform

The remediation plan for the findings in
[`SECURITY_REVIEW.md`](./SECURITY_REVIEW.md), organized as a "best of both"
reconciliation between this module and obin-platform. It defines the
**reconciliation matrix**, the **Istio + k8s delivery model** (what ships as
app code vs. mesh policy), the **layered test strategy**, and the **pass-1
remediation status**. It is the plan-of-record for epic #215.

## The reconciliation idea

The two systems have complementary strengths. The bar for this module is to
keep ours and adopt theirs — not to trade one for the other.

| Property | saas-starter (this module) | obin-platform | Reconciled target |
|----------|----------------------------|---------------|-------------------|
| Tenant data isolation | **Postgres RLS `ENABLE`+`FORCE`** — physical L3 backstop | App-layer authz | **Keep RLS** as an independent backstop under the app gates |
| Token blast radius | 15 min access TTL, no `jti` revocation on hot path | **Short TTL + `jti`-bearing, revocable** | **Adopt short TTL + `jti`** on the sidecar path (H1) |
| Signing key | Ephemeral fallback on Vault miss | Cloud KMS `asymmetricSign` | **Fail-closed** in prod; KMS/Transit (M1) |
| Authz/audit coverage | Catalogs exist, no enforcing gate | **Default-deny coverage CI gates** | **Adopt coverage-as-CI** (#204) |
| Reach vs. identity | Shared internal secret only | Explicit reach/identity split | **Mesh reach gate + app identity gate** (#203, #217) |
| Trust boundary | **Constant-time** shared-secret compare | — | Keep |
| OAuth | **PKCE + refresh rotation w/ reuse detection** | — | Keep |
| SSRF | **DNS-rebind-safe** webhook client | — | Keep; extend to Slack (LOW #6) |

**Best-of-both** = keep our RLS + constant-time boundary + PKCE/rotation + SSRF
defense, **and** adopt their short-TTL/`jti`, coverage-as-CI, and reach/identity
separation.

## Principles (the bar)

1. **Fail closed, everywhere.** No fallback-to-broken, no fail-open on a
   backing-store error where strictness matters.
2. **Short blast radius.** Small TTLs + `jti`; isolate the internal surface.
3. **Strip everything you stamp.** Unconditionally, every edge — the strip set
   is a proven superset of the stamp set.
4. **Coverage is a CI gate, not a hope.** Un-gated / un-audited routes and
   un-covered internal mesh paths are un-mergeable.
5. **Defense-in-depth stays independent.** L1 (app authz) + L2 (interceptor) +
   RLS (physical) + mesh reach are separately sufficient, never load-bearing on
   one another.

---

## Platform: Istio + k8s (delivery model)

The deployment mesh is **Istio**. The load-bearing distinction:

> **Istio mTLS authenticates the *workload* (SPIFFE service account), not the
> *end user / tenant*.** It is the **reach** gate and never replaces the
> app-layer **identity/authz** gates (`requireInternalCredential`, JWT/OBO user
> checks, `requirePlatformRole`).

This maps the obin two-token split onto the mesh:

| Concern | Mechanism | Proves |
|---------|-----------|--------|
| **Reach** | Istio `PeerAuthentication` (STRICT) + `AuthorizationPolicy` | *which workload* may talk to this port/path |
| **Identity** | App credential — internal secret, JWT, OBO | *internal vs. tenant*, *which user*, *what role* |

Consequently several findings are delivered as **mesh policy** rather than app
code, layered on the existing NetworkPolicy default-deny. The mesh baseline
(#217) is foundational and unblocks the mesh-delivered items.

### Mesh baseline (#217)

1. **`PeerAuthentication: STRICT`** mesh-wide (at minimum accounts,
   auth-sidecar, frontend). No `PERMISSIVE` fallback.
2. **Default-deny `AuthorizationPolicy`** at the namespace, then explicit allows
   per declared dependency — mirroring the existing NetworkPolicy default-deny.
3. **Sidecar injection enforced** for all in-scope workloads (no un-injected pod
   can receive traffic).
4. **Single ingress entry** — external traffic only via the Istio ingress
   gateway to frontend/marketing; everything else mesh-internal.
5. App-layer credentials/authz stay on top as defense-in-depth.

### Mesh-delivered findings

- **H2 reach half (#203):** `AuthorizationPolicy` on accounts that ALLOWs the
  internal method paths only from allowlisted caller SAs and DENYs the
  ingress-gateway SA — gating the internal surface by workload identity even
  while it is multiplexed on the shared HTTP port
  (`multiplexInternalGRPC`). Paths:
  `/saas.accounts.v1.PermissionService/{CheckPermission,CheckAccess,Decide}`,
  `/saas.accounts.v1.IdentityService/ResolveIdentity`,
  `/saas.accounts.v1.APIKeyService/ValidateAPIKey`,
  `/saas.accounts.v1.PrincipalService/{GetPrincipal,GetAgentPrincipal}`, and the
  usage `ConsumeUsage` method.
- **M5 (#209, closed):** the sidecar↔accounts JWKS/key channel is mTLS by
  construction under STRICT; the assert-STRICT gate (#204) guarantees it.
- **M8 ingress half (#211):** anonymous per-IP rate-limiting at the Istio
  ingress, complementing the app-layer per-IP limiter.
- **LOW #2, #14:** slowloris (server timeouts) and `/metrics`/reflection
  exposure gain a mesh mitigation (ingress-only reach) on top of the app fix.

### Two policy renderers — keep consistent

Network/mesh policy is produced by **two** renderers and both must stay in
lockstep when mesh policy changes:

- **`services/accounts/code/pkg/cataloggen/testdata/network-policy.golden.yaml`**
  — the test-only topology-policy golden (25 `NetworkPolicy` resources today;
  see `module/DEPLOYMENT_TOPOLOGY.md`).
- **`gitops.go`** — the real per-environment installed bundle.

New `PeerAuthentication` / `AuthorizationPolicy` resources must be added to
**both** and covered by goldens (Part C Layer 5), or the mesh-coverage gate
fails.

---

## Part A — Signing key & token crypto (foundations)

The Ed25519 minter (`module/services/accounts/code/pkg/auth/ed25519/minter.go`)
is the root of session trust; the same seed feeds the OAuth-state signer and the
permissions plugin. Targets:

- **Fail-closed key load (M1, #205).** In a production profile, refuse to boot
  when the Vault key load fails — no ephemeral fallback (`loadSigningKey`,
  `work.go:1294`). Restrict the ephemeral path to an explicit dev/fixture mode.
- **KMS / Vault Transit.** Prefer asymmetric-sign via KMS/Transit so the private
  key never sits in process memory (aligns with obin's Cloud KMS
  `asymmetricSign`).
- **Short-TTL + `jti` tokens.** Cut `AccessTokenTTL` (15 m → ~3 m) and make the
  `jti` the revocation key consulted on the sidecar path (Part B.1). This is the
  crypto half of the reach/identity split: short-lived, revocable *identity*
  credentials behind the mesh *reach* gate.
- **Startup fail-closed for derived signers.** `OAuthStateSigner` must treat an
  empty seed as a hard startup error in non-dev builds (LOW #9); Vault key fetch
  must reject non-loopback plaintext `http://` (LOW #10); `HashKey` must fail
  closed rather than silently downgrade to SHA-256 (LOW #5).

---

## Part B — Remediation workstreams

### B.1 — Fail-closed session lifecycle (H1, M1)

- Add a revoker to the sidecar `Sidecar` struct; consult it in `checkJWT`
  (`sidecar.go:139`) after signature/claim validation, keyed on `jti`, backed by
  the Redis revocation set accounts writes, fronted by a short-TTL (1–5 s) local
  cache. Failure mode explicit: **fail-closed** on cache/store error for
  high-assurance routes, or a documented ≤N-second window — never silent
  fail-open (contrast the current `IsRevoked` fail-open, LOW #1).
- Cut `AccessTokenTTL`; keep refresh rotation + reuse detection unchanged.
- Fail-closed signing-key load in prod (Part A / M1).
- **Proof:** logout-then-reuse e2e (Part C Layer 4).

### B.2 — Handler-layer authz binding (M9)

Bind the platform-admin gate in the **handler** layer for all 14
`PlatformAdminService` handlers (`rpcs.go:1252`–`:1506`), so the business-layer
`requirePlatformRole` is defense-in-depth, not the only gate. Reorder
`ImpersonateUser` so the role check precedes `requireMFA`. This becomes the
first customer of the RBAC-coverage gate (B.4) — the gate is what prevents the
binding from drifting back out.

### B.3 — Internal-surface isolation (H2)

Two independent gates, per the reach/identity split:

- **Identity (app, landed):** oracle handlers require `requireInternalCredential`
  (`auth.go:434`); `ConsumeUsage` gated; `Decide` org-bound. Verify no
  service/agent principal calls `Decide` with a bare JWT and no internal token.
- **Reach (mesh, #203):** `AuthorizationPolicy` allowlisting caller SAs on the
  internal method paths (see delivery model). Optional further blast-radius
  reduction: physically split `internalGRPC` onto a dedicated non-ingress port.

### B.4 — Coverage as a CI gate (#204) — highest leverage

obin's strongest property is **provable coverage**. This module has the catalogs
(`module/services/accounts/generated/authz-methods.json`,
`module/AUTHORIZATION_CATALOG.md`) but no enforcing gate. Add, failing the build
on violation, wired into the canonical gate (`RELEASE_GATES.md` → `codefly ci run`):

1. **RBAC-coverage** — every RPC in `authz-methods.json` maps to a declared gate
   (public / internal / authenticated + permission) or a ticketed allowlist
   entry; unknown → fail. (Would have caught un-gated `ConsumeUsage` (H2) and
   the unbound platform-admin handlers (M9).)
2. **Audit-coverage** — every mutating RPC + bulk read emits audit or carries a
   ticketed allowlist entry (mirror obin's `check_audit_coverage.py`).
3. **Permission no-broadening** — diff permissions/roles vs. `main`; a
   broadening grant requires an explicit approval label.
4. **Header-lockstep** — the sidecar Go test
   `TestUntrustedHeaders_SupersetOfStampedHeaders` runs in CI; extend with an
   accounts-side assertion that every header the connect auth interceptor trusts
   is in the strip set.
5. **Mesh goldens** — validate `PeerAuthentication` (assert **STRICT**, no
   `PERMISSIVE`) and `AuthorizationPolicy` goldens alongside the 25 NetworkPolicy
   goldens; **internal-RPC mesh-coverage**: fail CI if an `EXPOSURE_INTERNAL`
   RPC path is not covered by an `AuthorizationPolicy` allowlist (the mesh
   analogue of the RBAC-coverage gate; supports #203, #217).

### B.5 — Identity & medium-series hardening

Each ships with its negative test.

- **M2 (#206)** — thread `EmailVerified` into `bootstrapOrLoadPlatformRole`;
  refuse an unverified bootstrap grant.
- **M3 (#207)** — require an audience binding at `oidc.New`; fail construction
  closed; presets must demand one.
- **M4 (#208)** — collapse `Authenticate` credential/identity failures to one
  generic `Unauthenticated` error; detail only in logs/audit.
- **M7 (#210)** — CSP + anti-clickjacking headers on the product frontend,
  mirroring marketing, with an MF-origin allowlist.
- **M8 (#211)** — per-IP anonymous rate-limit (don't skip on empty identity),
  Turnstile default-on/boot-warn, abuse-gate `SendMagicLink`, atomic limiter.
- **LOW roundup (#213)** — the 18-item backlog (fail-open-on-error paths,
  timeouts, latent SSRF, cookie flags, dev-provider guard, …); two items move to
  the mesh (#217).

---

## Part C — Layered test strategy

Defense-in-depth is only real if each layer is independently tested — a passing
higher layer must not be able to hide a broken lower one. Every behavior change
lands with the test at its layer.

- **Layer 1 — Semantic / boundary (unit).** Per-finding negative tests at the
  handler boundary. E.g. M4: `Authenticate` returns an *indistinguishable*
  error+code for no-account / inactive / not-invited. M3: `oidc.New`
  construction error; wrong-`aud` rejection.
- **Layer 2 — Interceptor / gate.** App-layer authz in isolation:
  `requireInternalCredential` rejects tenant callers
  (`TestRequireInternalCredential_RejectsTenantCallers`); each platform-admin
  handler denies a non-admin even with the business-layer check removed (M9).
- **Layer 3 — RLS physical backstop.** With the app gate deliberately bypassed,
  Postgres RLS (`ENABLE`+`FORCE`) still refuses cross-tenant rows. Hits a real
  database — never mocked (schema/migration is what's under test).
- **Layer 4 — Session lifecycle & browser.** The **logout-then-reuse e2e**:
  login → protected RPC (200) → logout → reuse old access token → **401** (H1).
  Signing-key fail-closed at boot (M1). Browser: dashboard is not framable and
  CSP is present (M7, DAST assertion).
- **Layer 5 — Coverage CI gates.** The B.4 gates are themselves tested: adding
  an un-gated RPC fails CI; adding a mutating RPC with no audit fails CI; a
  broadening permission diff fails without the label; an `EXPOSURE_INTERNAL`
  path with no `AuthorizationPolicy` allowlist fails; a `PERMISSIVE`
  PeerAuthentication fails assert-STRICT.
- **Layer 6 — Abuse / anonymous surface.** Anonymous endpoints are per-IP
  rate-limited; the limiter is atomic under concurrent burst (M8); magic-link
  send is abuse-gated; Turnstile-disabled state is explicit.

---

## Remediation status — pass 1

Pass-1 fixes are implemented in-tree, each with the negative test that would
have caught the finding (tracked for Docker-backed integration/e2e verify by
#214):

| Finding | Change | Test |
|---------|--------|------|
| **M6** header-strip drift | `x-scoped-roles` / `x-scoped-roles-truncated` added to `untrustedAuthHeaders` (`auth-sidecar/gateway.go:396`) | `TestUntrustedHeaders_SupersetOfStampedHeaders` |
| **H4** Envoy trust-header leak | `allow()` sets `OkHttpResponse.HeadersToRemove` for un-restamped trust headers (`auth-sidecar/sidecar.go:234,259`) | `TestUnit_Allow_RemovesUnstampedTrustHeaders` |
| **H3** Connect credential smuggling | `injectHeaderJWTCredential` clears `req.Authentication` before setting from the trusted header (`accounts/pkg/adapters/connect_handlers.go:400`) | `TestInjectHeaderJWTCredential/absent_header_drops_a_smuggled_client_credential` |
| **H2 (code half)** any-JWT oracle | Retired `requireInternalOrAuth`; oracle handlers + `ConsumeUsage` require `requireInternalCredential` (`auth.go:434`); `Decide` is org-bound | `TestRequireInternalCredential_RejectsTenantCallers` |

**Before merge (pass 1):** open the PR, run the Docker-backed integration + e2e
security suites, verify `Decide` (`EXPOSURE_AUTHENTICATED`) is safe for
tenants-in-own-org and internal callers, and confirm the sidecar
`HeadersToRemove` behavior in a real Envoy-fronted environment.

---

## Flip-gate rollout (shadow → enforce)

Any change that could break a legitimate caller — new RBAC gates, new
`AuthorizationPolicy`, the per-IP limiter — rolls out behind a **shadow→enforce
flip-gate**:

1. **Shadow.** Run the gate in report-only: app gates emit a `would_deny`
   counter; Istio policies use dry-run. Nothing is actually denied.
2. **Watch.** Confirm `would_deny` / Istio dry-run telemetry shows only the
   intended callers being flagged — no legitimate traffic.
3. **Enforce.** Flip to deny once the shadow signal is clean.

This is how the reach/identity gates and the coverage CI gates land without a
flag-day outage.

---

## Delivery index (epic #215)

| Item | Layer | Delivery | Issue | Plan ref |
|------|-------|----------|-------|----------|
| Istio baseline (STRICT + default-deny) | Mesh | Foundational | #217 | Platform delivery model |
| H1 revocation on sidecar + short TTL | App | App code + e2e | #202 | B.1, C-L4 |
| H2 internal-surface isolation | App + mesh | Code landed + AuthzPolicy | #214 / #203 | B.3 |
| Coverage CI gates + mesh goldens | CI | Gate | #204 | B.4, C-L5 |
| M1 fail-closed signing key | App | App code | #205 | A, B.1 |
| M2 bootstrap verified email | App | App code | #206 | B.5 |
| M3 OIDC audience binding | App | App code | #207 | B.5, C-L1 |
| M4 generic auth errors | App | App code | #208 | B.5, C-L1 |
| M6/H3/H4 (pass 1) | App | Landed | #214 | Pass-1 status |
| M7 frontend CSP | Browser | Next config | #210 | B.5, C-L4 |
| M8 anonymous rate-limit + abuse | App + mesh | Code + ingress | #211 | B.5, C-L6 |
| M9 handler-layer admin gates | App | App code | #212 | B.2, C-L2 |
| LOW backlog roundup | App + mesh | Mixed | #213 | B.5 |

Findings and their exploits: [`SECURITY_REVIEW.md`](./SECURITY_REVIEW.md).
