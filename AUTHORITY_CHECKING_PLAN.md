# Authority-checking — enforcement, verification, and the delegated-authority lifecycle

> Scoping record for epic [#414]. Companion to [`AUTHZ.md`](AUTHZ.md) (the three
> layers the starter **ships**) and [`AUTHZ_GAP_ANALYSIS.md`](AUTHZ_GAP_ANALYSIS.md)
> (the *data* axis — which records/fields a caller may touch). This document
> covers the orthogonal *enforcement / verification* axis: given that policy is
> **declared** and authority is **minted**, where is it actually **checked**?
>
> This is a **review + scoping record, not an implementation**. It grounds each
> suspected gap in the current tree, decides code-vs-data and in-scope-vs-product
> per gap, and hands each workstream to the child issue that implements it. No
> behavioural code ships with it.

## Framing: authority-creating vs authority-checking

The platform already ships the **authority-creating** halves of scoped
delegation, and they are strong:

- Every accounts RPC declares its permission requirements in the IDL
  (`saas.policy.v1.MethodPolicy`, [`METHOD_POLICY.md`](module/METHOD_POLICY.md)).
- Accounts' `WorkContextAuthorityServer` mints scoped, signed, revision-pinned
  Work Contexts, owner-bound to the requesting user
  (`services/accounts/code/pkg/adapters/work_context_rpcs.go`).

What is thinner than the declaration count suggests is the **authority-checking**
halves — the code that *reads* those declarations and *verifies* those
capabilities at request time. Five workstreams close that gap; each is now a
child of this epic:

| # | Workstream | Child issue |
|---|---|---|
| A | Request-time permission enforcement interceptor (shadow → enforce) | [#415] |
| B | Fail-closed Work Context / signature verification at the edge | [#416] |
| C | Actor-authorized exchange (extend past the TTL cap) | [#417] |
| D | Generated permission/role catalog as a deploy step | [#418] |
| E | Per-org non-human Principal registration (code vs data) | [#419] |
| — | Replay protection for `SINGLE_USE` (adjacent, surfaced below) | [#420] |

## What already exists (the mint / declaration side)

- **Declared policy is authoritative and complete.** `MethodPolicy` carries
  `exposure`, `tenant`, `permissions`, `scopes`, `resource_bindings`, `mfa`,
  `platform_role`, `audit`, `idempotency`, `rate_limit`, sensitivity, and a
  bounded `conditions` predicate set (`proto/saas/policy/v1/options.proto:163-186`).
  A method with a missing or invalid policy is deliberately absent from the
  runtime lookup and therefore denied (`pkg/business/rpc_policy.go:346-363`).
- **The edge already enforces the enforceable-without-a-body subset.** The
  gateway consumes exposure, rate-limit class, the login-factor budget, and a
  policy fingerprint ([`AUTHORIZATION_CATALOG.md`](module/AUTHORIZATION_CATALOG.md)
  "Enforcement boundary", lines 39-59). Everything richer is explicitly deferred
  to "the accounts interceptors/handlers and domain policy adapter."
- **Work Contexts are minted, signed, and owner-bound.** `StartTask` /
  `StartRootSession` / `ExchangeAudience` / `StartChildSession`
  (`work_context_rpcs.go:201-384`) each mint via the SDK signer, journal the
  actor hop, and pin the parent to the caller.
- **Key discovery is solved.** The Work Context signing key **is** the
  access-token key — the same deterministic `kid`
  (`base64url(sha256(pub)[:8])`, `pkg/auth/ed25519/minter.go:188-194`) is fed to
  both the JWT minter and the Work Context authority
  (`work.go:298-301`). It is published at `/v1/auth/.well-known/jwks.json`
  (`work.go:285-288`, `pkg/adapters/jwks_http.go`). Callees never need Vault.

## Method

Every finding is grounded in the current tree. Load-bearing citations:

- Tier-only admission: `pkg/adapters/{connect,grpc}_auth_interceptor.go`
- Full declared policy: `pkg/business/rpc_policy.go`,
  `proto/saas/policy/v1/options.proto`
- Handler-level gates: `pkg/adapters/auth.go`
- Mint / owner-binding: `pkg/adapters/work_context_rpcs.go`
- Actor representations: core `WorkContextV1.ActorChain`; access-token `act`
  chain in `pkg/auth/identity.go:106-149`
- Principals: `pkg/adapters/principal_rpcs.go`, `pkg/business/principals.go`,
  `services/store/migrations/36_create_principals.up.sql`
- Catalog import: `cmd/role-catalog-import/main.go`,
  `pkg/infra/postgres_role_catalog.go`; composition in
  `tools/composition/composition.go`

---

## Findings — confirm / refute, per workstream

### A — Enforcement reads only `.Tier`; the richer declarations are unenforced — **CONFIRMED**

Both interceptors admit on **exposure tier alone**. After stripping forwarded
identity and (for tenant-facing listeners) verifying the bearer, they stamp
identity and return — they never consult `tenant`, `permissions`, `scopes`,
`platform_role`, `mfa`, or `resource_bindings`
(`connect_auth_interceptor.go:85-126`, `grpc_auth_interceptor.go:106-161`). The
full policy is loaded (`LookupRPCPolicy` returns the whole `RPCPolicy` including
`MethodPolicy`) but only `policy.Tier` is read.

Everything past the tier is therefore enforced — if at all — by hand-written
handler gates. The precise scale, re-counted against the tree (the issue's
round numbers undercount):

- **Tenant declarations are real but unenforced centrally.**
  `TENANT_REQUIREMENT_ORG_ADMIN` is declared on **37 RPCs** (the issue says 34),
  spread across 11 bounded contexts; `TENANT_REQUIREMENT_ORG_OWNER` is declared
  on **0** (enum value only). None is enforced by an interceptor.
- **The "~72 hand-written `require*` sites" is precisely the org/team-membership
  subset**, not the whole surface: `requireOrgAdmin` (30) + `requireOrgMember`
  (29) + `requireBillingAdmin` (6) + `requireTeamAdmin` (5) + `requireTeamMember`
  (2) = **72**. The complete `require*` call-site count in `code/` is **310**
  (`requireAuth` alone is 102). The design point stands either way: central
  enforcement is absent and the handler gates paper over it, drift-prone.
- **The `OWNED_RESOURCE` set is 9, not 8.** `RESOURCE_TARGET_OWNED_RESOURCE`
  appears on 6 webhook + 2 invitation + **1 missed** — `RevokePrincipal`
  (`authorization.proto:703`). All use `RESOURCE_LOOKUP_RESOURCE_TO_ORGANIZATION`,
  and **no runtime code resolves the org from the resource** — the only readers
  of `GetResourceBindings()` are generation/validation
  (`pkg/cataloggen/authz_methods.go`, `pkg/business/rpc_policy.go`). So a central
  interceptor cannot yet resolve these to a tenant; they stay `unsupported`
  until an ownership resolver exists.
- **`UpdateUser` / `DeleteUser` are a genuine declaration bug** — both pair
  `tenant: TENANT_REQUIREMENT_USER` with `permissions: "users:write"`
  (`identity.proto:207,230`). A self-scoped tenant with an org-capability
  permission reads as a `gap` under any comparator and must be fixed in the
  declaration, not worked around in the interceptor.

**No shadow-mode / classifier exists yet.** Greps for `shadow`, `broadening`,
`unsupported` in the authz path return nothing relevant, and
`rpc_policy_interceptor_test.go` tests only exposure/identity/credential
admission. #415 notes a shadow-mode patch exists against an older base and needs
a rebase — treat it as a starting point, not merge-ready.

**Shape of the fix (for #415).** A central interceptor that reads the *full*
`MethodPolicy` and, per call, classifies the outcome `ok` / `gap` /
`broadening` / `unsupported`, emitting an audit signal and **changing no
behaviour** (shadow). Flip to hard-deny only once shadow shows zero
`gap`/`broadening`/`unsupported` against real traffic, folding the 72 (then the
rest) hand-written gates into the central path as coverage lands. The two edge
cases above (`OWNED_RESOURCE` resolution, the `UpdateUser`/`DeleteUser`
declaration) are the known blockers to a clean shadow.

### B — No fail-closed verification of a presented Work Context — **CONFIRMED (fail-open today)**

The string `x-codefly-work-context` **does not appear anywhere in `module/`.**
The only Work Context verification that exists is server-side, over the request
*body's* parent-token field inside the mint authority (`verifyParent`,
`work_context_rpcs.go:490-544`). There is no edge/callee verifier for a
*presented* context header.

Worse, the gateway forwards it **unstripped and unchecked.** The gateway
authenticates only the `Authorization` bearer via the ext_authz sidecar and
re-stamps identity headers, but its strip list `untrustedAuthHeaders`
(`services/auth-gateway/code/gateway.go:396-403`) **omits**
`x-codefly-work-context`. A client-supplied Work Context therefore passes
straight through to the upstream, neither validated nor removed — the fail-open
gap #416 targets. The bearer is preserved to solutions on purpose
(`gateway_solutions.go:126-129`); the Work Context should be **verified**, not
merely forwarded.

**Two reusable conventions the new verifier must match** (so an accounts outage
reads as a `401`, not a distinct leaked error class):

1. Collapse every validation failure to a single opaque sentinel — the
   `ErrInvalidOAuthState` pattern (`pkg/auth/oauth_state.go:154-209`, "never
   differentiate the cause to the client").
2. Keep the fail-closed *unavailable* bucket distinct from *invalid* — the
   `ErrJWKSUnavailable` / `ErrRevocationUnavailable` sentinels
   (`pkg/auth/errors.go:37-46`) and the two-bucket boundary collapse in
   `grpc_auth_interceptor.go:144-152` ({temporarily-unavailable → `Unavailable`}
   vs {everything-else → one invalid `Unauthenticated`}).

**Net-new work for #416:** a multi-kid Work Context verifier. The published
JWKS carries only the *current* key (`minter.go:215-228`); rotation for
*access tokens* is handled by `JWT_PREVIOUS_PUBLIC_KEYS`
(`work.go:276,1362-1399`), but the mint-side Work Context verifier
(`work_context_rpcs.go:83-89`) trusts a single static kid and nothing reloads
it. The verifier owes a boot-time load + exported `Refresh(ctx)` (so a callee
fails closed at boot, not on first request) over the current **and** prior keys
— neither the static `Configure` verifier nor the gateway's boot-once
`fetchPublicKey` (`main.go:355-401`, never re-fetched) provides it.

### C — Every mint/exchange RPC is owner-bound; no actor can renew — **CONFIRMED**

All four mint/exchange RPCs open with `authorizeOwner(ctx, orgID)` =
`requireAuth` + `requireOrgMember` (`work_context_rpcs.go:476-488`), and the
derive paths additionally pin the parent to the caller:

```go
// verifyParent, work_context_rpcs.go:500-507
parent, err := s.verifier.Verify(token, codefly.WorkContextExpectations{
    Issuer:           s.issuer,
    TenantID:         orgID,
    OwnerPrincipalID: ownerID,   // the authenticated human caller
})
```

TTL is capped **twice**: proto `buf.validate` bounds `ttl_seconds` at 900
(`work_contexts.proto:58-61` et al.) and the SDK enforces
`WorkContextMaxTTL = 15m` at verify time. Because a delegated *actor* holds a
context but not the owner's bearer, it fails `requireAuth`/owner-match and
**cannot re-exchange** — any delegated task outliving the 15-minute cap fails
closed at the next call.

The asymmetry is precise: the actor chain **is** carried and re-validated
(`verifyParent` loops `parent.GetActorChain()` at `:528-542`, resolving each
actor's live authority and checking revocation via `requireChainNotRevoked` /
`AnyActorChainHopRevoked`), but it never *authorizes the caller*. Two distinct
actor representations exist —

- the signed **Work Context actor chain** (`WorkContextV1.ActorChain
  []*WorkActorV1`, each hop carrying `DelegationId` + `GrantedScopes`), seeded
  from the request's `actor_principal_id`; and
- the access-token **RFC 8693 `act` chain** (`pkg/auth/identity.go:106-149`),
  which is explicitly **audit metadata, not authorization** — "a service's
  authority to act is enforced separately (see the `may_act` story SVC-4), never
  implied by appearing here" (`identity.go:110-113`).

**The missing primitive is `may_act`** — referenced only in that comment; it
does not exist in code. An actor-authorized exchange (#417) would replace
owner-match with: (i) accept the *actor's own* credential as caller, (ii) verify
the actor is a legitimate, un-revoked hop in the parent's `actor_chain`, and
(iii) bound the renewed authority to that hop's `GrantedScopes` (renewed ⊆
original — never widening). Persistence substrate already exists
(`actor_chain_journal` + `actor_chain_revocations`, migration 99). #417 should
land **after** #415 so renewals are themselves enforced.

### D — The role catalog import is a manual CLI, not a deploy step — **CONFIRMED**

First, disambiguate two "catalogs" the issue elides:

- The **composition permission-*name* catalog** — module-compose merges an
  external `PermissionsContribution` (`composition.go:112-123`, schema
  `codefly/saas/permissions-contribution/v1`) into
  `deployment/generated/contributed-permissions.json` and the generated Go
  catalog `pkg/permissioncatalog/catalog_gen.go`. This is a build-time artifact,
  and it **is base** — both files are base-manifest-tracked
  (`tools/base-manifest.json:75,701`), so a consumer cannot hand-edit them; a
  permission arrives only via contribution → regeneration.
- The **built-in RBAC *role* catalog** — a versioned `roles.json` (roles +
  `resource:action` grants) that `cmd/role-catalog-import` diff-applies into the
  Postgres `roles` / `role_permissions` tables under the audited
  `app_control_plane` role (`pkg/infra/postgres_role_catalog.go:44`). This is a
  runtime DB-seeding concern, documented only in
  [`AUTHZ.md`](AUTHZ.md) "Built-in role catalog import".

The importer is **genuinely manual**: a repo-wide search for invocations of
`role-catalog-import` / `ImportRoleCatalog` finds only its own `main.go`, unit
tests, and docs — no Makefile target, CI job, k8s Job, or gitops wiring.

**There is no generic `DeployStep` framework**; `gitops.go` is a per-environment
Kubernetes/Istio YAML renderer, not a step registry. The deploy-time DB
precedent to model #418 on is the store migration runner (the standalone
`golang-migrate` binary in `services/store/code/main.go:34-52`) plus the
first-class `BootstrapJobEndpoints` topology hook (`gitops.go:220,1696-1830`,
with dedicated NetworkPolicies). Promoting the import means wrapping it as a
bootstrap-style Job that runs **after** store migrations, under
`app_control_plane`, pointed at a composed `roles.json`.

**The open design question for #418:** nothing today feeds
`contributed-permissions.json` (permission *names*) into the `roles.json` the
importer consumes (roles → permission grants). The permission strings stay in
`solution:resource:action` form, mapping to `WorkContextScope{resource_kind:
"<solution>:<resource>", actions: [...]}` (#418) — the contribution → role-grant
bridge is the piece to design, not just the Job wiring. Note this is a *third*
generation path, orthogonal to the two mesh/authz-policy renderers (test-only
`cataloggen` goldens vs the real `gitops.go` installer).

### E — Per-org **agent** Principal registration already exists as code; **service** does not — **REFUTED (for agents) / CONFIRMED (for services)**

The `question` in #419 (repo code vs runtime data) is largely answered by the
tree: it is **code, and it already ships for agents.**

- A `Principal` is a unified identity across `human` / `service` / `agent`
  (`principals.kind`, a `CHECK`-constrained enum, migration
  `36_create_principals.up.sql:30,44`). Org scope is enforced by kind: humans are
  cross-org (`org_id IS NULL`), services and agents are inherently per-org
  (`org_id IS NOT NULL`, `:48-51`).
- `CreateAgentPrincipal` (`principal_rpcs.go:62-84`, business at
  `principals.go:269-329`) is a real, per-org, **org-admin-gated**
  (`requireAuth` + `requireOrgAdmin`), idempotent-per-`agent_identifier`
  registration surface. And the mint path already **requires it**:
  `actor_principal_id` "must name an active, registered agent Principal in
  org_id" (`work_contexts.proto:46-48`). So the owner-binding checks in
  workstream C already have a concrete Principal to bind delegated actors to.
- The genuine gap: there is **no `CreateServicePrincipal` / generic
  `CreatePrincipal` surface.** `service` principals exist as a kind but are
  created *only* by backfill from `api_keys` (migration
  `37_backfill_principals.up.sql:16-46`). `CreateAgentRequest` is the only
  inbound create shape, always producing `kind=agent`.

**Decision for #419:** per-org non-human registration is **repo code in
accounts**, and for the agent kind it is already built (`CreateAgentPrincipal`).
#419 reduces to a narrow question — whether delegated execution needs a distinct
*service* Principal surface beyond the api-key backfill, or whether the agent
surface is sufficient — plus documenting that the registered agent Principal is
exactly what C's exchange binds against.

---

## Scoping decisions

| Workstream | Gap | Decision | Child |
|---|---|---|---|
| **A** | Interceptors admit on tier only; 37 ORG_ADMIN + the rest of the policy unenforced; 72 org/team gates paper over it | **In scope, staged.** Central full-policy interceptor, shadow → enforce. Fix the `UpdateUser`/`DeleteUser` declaration; leave the 9 `OWNED_RESOURCE` RPCs `unsupported` until an org resolver exists | [#415] |
| **B** | No edge verification of a presented Work Context; gateway forwards it unstripped | **In scope.** Multi-kid, fail-closed verifier with `Refresh(ctx)`; single invalid sentinel; add `x-codefly-work-context` handling at the gateway | [#416] |
| **C** | Every mint/exchange RPC owner-bound; actors cannot renew past the 15m cap; `may_act` is a comment, not code | **In scope.** Actor-authorized exchange keyed to a verified, un-revoked actor-chain hop, renewed ⊆ original. Land after A | [#417] |
| **D** | `role-catalog-import` is a manual CLI; no `DeployStep`; contribution→role bridge missing | **In scope.** Bootstrap Job after store migrations under `app_control_plane`; design the `contributed-permissions.json` → `roles.json` bridge | [#418] |
| **E** | Agent Principal registration already exists (code); service registration is backfill-only | **Mostly answered.** Code, in accounts, shipped for agents; #419 decides only whether a distinct service-Principal surface is needed and documents the C binding target | [#419] |
| **adjacent** | `SINGLE_USE` is a label with no replay store; contexts are replayable | Treat contexts as `REUSABLE` until a verifier-side replay store lands | [#420] |

## Exit criterion

A single CI job proves the whole authority-checking loop end to end:

1. An authenticated streaming turn **mints** a Work Context, **exchanges** it for
   a tool audience, and calls a **verifying** callee (B) that fails closed
   against the published JWKS.
2. The turn lands an audit row carrying the **full actor chain** (the
   `actor_chain_journal` hop written on each mint).
3. A **custom role** that revokes the relevant `resource_kind:action` scope makes
   the *same* turn **fail closed** — proving A enforces the declared permission,
   not merely the tier.
4. Green with the **dev-mode auth bypass removed** = parity with production
   admission.

This job depends on A (enforcement) and B (verification) at minimum; C adds a
renewal hop to step 1, and E supplies the agent Principal the actor chain binds
to.

## Summary

- The **authority-creating** side (declared `MethodPolicy`, minted owner-bound
  Work Contexts, solved key discovery) is present and correct.
- The **authority-checking** side has four real gaps, all confirmed against the
  tree: (A) admission reads only `.Tier`, leaving 37 ORG_ADMIN declarations and
  the permission/resource declarations unenforced behind 72 drift-prone handler
  gates; (B) a presented Work Context is neither verified nor stripped at the
  edge — fail-open today; (C) owner-binding blocks any delegated actor from
  renewing past the 15-minute cap, and the enabling `may_act` primitive does not
  exist; (D) the role catalog import is a manual CLI with no deploy step and no
  contribution→role bridge.
- One gap is **largely already closed**: (E) per-org **agent** Principal
  registration is repo code (`CreateAgentPrincipal`) that the mint path already
  binds against; only a distinct *service*-Principal surface is open.
- The issue's headline counts were slightly low — **37** ORG_ADMIN RPCs (not 34),
  **9** `OWNED_RESOURCE` RPCs (not 8; `RevokePrincipal` was missed), and the
  **72** figure is specifically the org/team-membership helper subset of 310
  total `require*` sites.
- Each workstream is scheduled as its own child issue ([#415]–[#419], plus
  [#420] for `SINGLE_USE`); this document is the decision record they build from.

[#414]: https://github.com/codefly-dev/module-saas-starter/issues/414
[#415]: https://github.com/codefly-dev/module-saas-starter/issues/415
[#416]: https://github.com/codefly-dev/module-saas-starter/issues/416
[#417]: https://github.com/codefly-dev/module-saas-starter/issues/417
[#418]: https://github.com/codefly-dev/module-saas-starter/issues/418
[#419]: https://github.com/codefly-dev/module-saas-starter/issues/419
[#420]: https://github.com/codefly-dev/module-saas-starter/issues/420
