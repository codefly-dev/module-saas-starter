# SaaS Starter implementation roadmap

Status: active
Started: 2026-07-12
Last reviewed: 2026-07-26
Companion checklist: [TODO.md](TODO.md)

This roadmap turns the architecture review into an executable program of work.
It supersedes older forward-looking checklists where they disagree with this
document. `TODO.md` is the day-to-day source of status; this document explains
the design, sequencing, implementation rules, and acceptance criteria.

## Outcome

Build a secure, reusable SaaS kernel that Codefly can compose into Warden,
Codefly itself, Mind, and future products. The completed starter must provide:

- Secure identity, sessions, MFA, users, organizations, teams, and permissions.
- Notifications, email, API keys, audit, billing, subscriptions, and entitlements.
- Connect, gRPC, and gRPC-Web through one service implementation, with optional
  generated JSON REST APIs.
- An Envoy/Istio gateway that authenticates, rate-limits, routes, and sanitizes
  headers from a generated route and policy catalog.
- A modular frontend with a typed compile-time plugin path and a controlled
  runtime plugin path for Warden.
- Codefly-generated clients, servers, policies, routes, networking, plugin
  registration, documentation, and contract tests.
- Durable inbox/outbox workers for every external or asynchronous side effect.
- A CI and release process that prevents generated surfaces from drifting.

## Delivery rules

Every implementation change follows these rules:

1. **Deny by default.** Missing configuration, policy, identity, tenant, or
   dependency must reject access or fail startup when security depends on it.
2. **One source of truth.** A route, permission, or plugin capability is declared
   once and generated into all consumers. Hand-maintained parallel inventories
   are transitional and must have parity tests.
3. **Gateway and service enforcement.** The gateway performs coarse method-level
   authentication and authorization. Domain services still enforce tenant,
   resource ownership, and state-dependent rules.
4. **No side effect without durability.** Billing events, webhooks, email,
   notifications, exports, and agent approvals use a transactional inbox or
   outbox and leased workers.
5. **Separate credentials by authority.** Runtime tenant requests, privileged
   workers, and migrations never share a database principal.
6. **Generated boundaries, handwritten behavior.** Codefly generates contracts,
   registration, routing, policy metadata, and tests—not business decisions.
7. **Compatibility is explicit.** APIs and plugins declare versions and supported
   compatibility ranges. Breaking changes are checked in CI.
8. **A checkbox requires evidence.** An item is complete only when its acceptance
   tests, documentation, observability, and rollback path exist.

## Target architecture

```text
browser / SDK / CLI / agent
              |
        Istio / Envoy gateway
 TLS, exact routes, limits, ext_authz,
 untrusted-header removal, request identity
              |
        authentication + PDP
 JWT / API key / workload identity,
 generated coarse RPC policy
              |
       one Connect-Go HTTP handler
 Connect + gRPC + gRPC-Web on one port
 optional generated REST transcoding
              |
  domain services enforce tenant/resource rules
              |
  +-----------+----------------+----------------+
  |                            |                |
tenant runtime DB       privileged job DB   migration owner
  |                            |
  +------ transactional inbox / outbox ------+
                               |
       leased workers: Stripe, email, notifications,
       outbound webhooks, privacy exports, audit export
```

Codefly compiles the following input graph:

```text
versioned protobuf + method policy options
plugin.codefly.yaml manifests
module.codefly.yaml service/dependency graph
configuration and secret schemas
```

into:

```text
Go/TypeScript types and clients
Connect handler interfaces and server registration
Envoy/Istio route and authorization configuration
optional REST/OpenAPI output
frontend plugin routes/navigation/permission types
Codefly endpoints, dependencies, and network policy
contract, parity, and compatibility tests
reference documentation
```

## Program sequence

```text
P0 safety
  -> P1 contract compiler and protocol convergence
      -> P2 identity, data, and worker durability
          -> P3 plugin platform and Mind capabilities
              -> P4 product depth and operational maturity
```

P0 may contain parallel work, but P1 must not automate insecure policy. P2
depends on the generated policy vocabulary from P1. Runtime plugins in P3 depend
on stable versioned contracts and entitlements from P1/P2.

## Phase 0: stop-ship security and correctness

Exit gate: the public authentication bypass is removed, every exposed method has
an explicit policy, MFA is a real transaction, external events are not silently
lost, secrets and forwarded identity fail closed, and P0 regression suites run
in CI.

### P0.1 Authentication configuration and fixture isolation

Implementation instructions:

1. Split production provider validation from development fixture validation in
   the business service. They must not share an implicit fallback.
2. Production `Authenticate` accepts only a provider authorization code that is
   exchanged and validated. Fixture authentication requires both a Codefly
   identity profile that explicitly selects fixture mode and an explicit
   Codefly fixture; either input alone fails closed.
3. The development path treats `provider_id` as an opaque fixture token and
   obtains provider, subject, and email exclusively from the allowlisted fixture
   validator. Request-supplied email and verification flags are never trusted.
4. Empty, unknown, or incompletely configured production providers fail service
   startup with a non-secret diagnostic.
5. Staging and production ignore fixture selection and never register or seed
   development fixture authentication.

Acceptance criteria:

- A request without an OAuth code fails when development authentication is not
  explicitly configured.
- A development token not present in the selected fixture fails.
- A caller cannot override a fixture user's email, provider, or subject.
- WorkOS/Auth0/Google cannot start with missing required credentials.
- The `dev-admin` Codefly scenario still works when fixture mode is explicit.

### P0.2 Authorization closure

Implementation instructions:

1. Produce a temporary checked-in method matrix for all proto methods: exposure,
   tenant rule, role/permission, resource ownership, MFA, scopes, internal-only,
   audit event, and idempotency class.
2. Add deny-by-default Connect and gRPC interceptors. An unclassified method is
   a startup or test failure, never public by omission.
3. Immediately repair known gaps: organization member mutation, team member
   reads, API-key administration, webhook administration, notification ownership,
   GDPR ownership, identity resolution, permission decisions, and validation of
   API keys.
4. Put internal identity and policy-decision RPCs on a private listener and
   require workload identity. The current Codefly Go runtime exposes this as
   internal-only gRPC multiplexed over the private REST h2c listener; move it
   to a generated named endpoint once the runtime supports multiple endpoints
   with the same API. Until workload identity lands, a missing shared secret
   must fail closed.
5. Add table-driven tests for anonymous, unrelated tenant member, member, admin,
   owner, platform admin, API key, and workload callers.

Acceptance criteria:

- Every service method is in the policy matrix.
- CI fails when a new method lacks policy metadata or tests.
- Cross-tenant and resource-ID substitution tests fail closed.
- Internal APIs are unreachable through the public gateway.

### P0.3 MFA and step-up authentication

Implementation instructions:

1. Model a short-lived `mfa_transactions` record containing subject, session
   intent, expiration, attempt count, required factors, and one-use state.
2. Primary authentication returns only `mfa_required` and a restricted,
   audience-bound transaction token when an enrolled factor exists. It must not
   return a normal refresh token.
3. Add a login challenge operation distinct from enrollment verification.
4. On successful TOTP, WebAuthn, or backup-code verification, atomically consume
   the transaction and mint the normal session.
5. Encrypt TOTP secrets with a versioned Vault/KMS envelope. Never return the
   secret after enrollment confirmation.
6. Represent assurance in tokens using `amr`, `auth_time`, and a session-bound
   assurance level. Sensitive actions require a recent step-up.

Implemented: TOTP, one-use recovery codes, and WebAuthn passkeys/security keys
share the durable MFA handoff. WebAuthn RP/origin policy is Codefly-configured;
credential and ceremony state is Vault-encrypted and the assertion counter,
ceremony, login transaction, and new session commit atomically.

Acceptance criteria:

- Enrolled users cannot obtain a normal session without a verified factor.
- MFA transactions are one-use, expire, rate-limit attempts, and work across
  replicas.
- Backup codes are one-use hashes and cause an audit/notification event.
- Step-up freshness is tested for billing, API keys, webhooks, and admin actions.

### P0.4 Gateway, headers, logging, CORS, and rate limits

Implementation instructions:

1. Strip all client-provided identity, tenant, role, scope, and impersonation
   headers at the edge. Only the trusted authorization service may add them.
2. Make API forwarded-identity trust an explicit production contract backed by
   a separate gateway credential and private Codefly networking; direct traffic
   must validate JWTs. Replace the shared gateway credential with workload
   identity when the mesh exposes process identity.
3. Replace full response-body error logging with structured fields and a bounded,
   redacted diagnostic identifier.
4. Use an exact CORS allowlist. Never reflect arbitrary origins with credentials.
5. Replace the raw Redis rate-limit connection with a maintained client, TLS
   verification, bounded pools/timeouts, and explicit fail-open/fail-closed rules
   by route class.
6. Trust forwarded client IPs only from configured proxies.
7. Make readiness require every dependency needed to serve the advertised route
   set. Keep liveness limited to process health.

Acceptance criteria:

- Header-spoofing integration tests cover REST, Connect, gRPC, and gRPC-Web.
- Auth tokens, passwords, MFA codes, webhook payloads, and PII never appear in
  logs.
- CORS, proxy-chain, Redis-failure, and readiness behavior are tested.

### P0.5 Stripe billing safety

Implementation instructions:

1. Make the webhook HTTP handler do only signature verification, payload bounds,
   transactional inbox insertion, and a fast `2xx` response.
2. Store the raw body, provider event ID, event type, API version, livemode,
   received time, attempts, status, lease, and last error.
3. Process with leased workers. Reconcile current Stripe object state rather than
   assuming event order.
4. Use Stripe idempotency keys for every retryable mutation.
5. Make plan, price, trial, currency, tax behavior, and redirect origins
   server-owned configuration. The client selects only an allowed catalog key.
6. Require organization billing-admin permission and recent MFA for checkout and
   portal creation.
7. Use the privileged worker database pool for cross-tenant reconciliation.

Implemented foundation: the public endpoint now verifies the exact body,
atomically inserts one immutable inbox row, and returns immediately. Rows retain
the raw payload and Stripe metadata and move through
`pending -> processing -> retrying -> succeeded|dead_letter`. Replicas claim
disjoint batches with `FOR UPDATE SKIP LOCKED`; owner-checked expiring leases,
bounded attempts, and delayed retries make request delivery and processing
failure independent. Every subscription-changing event now hydrates Stripe's
current object. Provider-read start time plus an organization-scoped PostgreSQL
advisory lock makes local projection monotonic even when older HTTP reads finish
last or an event for a prior subscription arrives late. Every Stripe POST now
uses a required stable operation key. The browser selects only a checkout-enabled
catalog key; price, currency, trial, tax behavior, and exact redirect origins are
server-owned. Checkout and portal creation require owner/admin or delegated
`billing:write` authority plus fresh AAL2 evidence. Receipt stays on the
fail-closed tenant request pool, while claims and cross-tenant projection use a
separate, grant-limited `app_billing_worker` BYPASSRLS pool.

Acceptance criteria:

- Duplicate, failed-then-retried, concurrent, and out-of-order events converge.
- No acknowledged event can remain invisible or be considered processed after a
  failed dispatch.
- Checkout cannot choose arbitrary trials, prices, organizations, or redirects.
- A reconciliation command can rebuild local subscription state from Stripe.

Reference: <https://docs.stripe.com/webhooks> and
<https://docs.stripe.com/api/idempotent_requests>.

### P0.6 Outbound webhook safety and durability

Implementation instructions:

1. Generate a random signing secret during endpoint creation, encrypt it through
   Vault/KMS, and reveal it only once. Support explicit rotation with overlap.
2. Validate scheme, normalized host, resolved addresses, port, and redirect
   behavior. Block loopback, private, link-local, multicast, metadata, and cluster
   ranges at both validation and connect time.
3. Disable redirects and enforce egress network policy.
4. Insert delivery work in the same transaction as the domain event.
5. Lease deliveries using `FOR UPDATE SKIP LOCKED` or an equivalent claim. Use a
   stable event/delivery ID, bounded exponential backoff, retry budget, dead-letter
   state, replay, and per-endpoint concurrency limits.
6. Sign version, timestamp, event ID, and exact body. Document verification and
   replay tolerance for consumers.

Acceptance criteria:

- SSRF tests cover redirects, DNS rebinding, IPv4/IPv6 private ranges, metadata
  addresses, and encoded/alternate address forms.
- Queue saturation, process restarts, and multiple replicas do not lose or double
  claim deliveries.
- Secret rotation and dead-letter replay are audited.

Reference: <https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html>.

Implemented foundation: signing keys are server-generated, Vault-enveloped,
revealed once, and dual-signed during bounded rotation overlap. Public HTTPS
validation is repeated in a DNS-pinning dialer and backed by an IPv4/IPv6 egress
deny policy with redirects disabled. Domain audit events and exact-body delivery
rows fan out transactionally. A grant-limited worker pool uses endpoint advisory
locks, `SKIP LOCKED`, expiring owner leases, bounded retry/dead-letter state, and
stable event IDs across replay. The generated API and webhook admin UI expose
the complete lifecycle, and `module/WEBHOOKS.md` defines consumer verification.

### P0.7 Dependency and release hygiene

Implementation instructions:

1. Upgrade vulnerable production frontend dependencies, especially Next.js and
   Hono, and commit a clean lockfile.
2. Repair ESLint's TypeScript/React flat configuration and make warnings policy
   explicit.
3. Remove the workstation-local `file:` dependency on Codefly. Consume a released
   package or a workspace package included in clean checkout builds.
4. Fix the object-storage agent version and stale `api` service references in
   Playwright.
5. Add secret scanning, dependency review, SBOM generation, image scanning, and
   provenance/signing to release CI.

Implemented foundation: the official Next core-web-vitals and TypeScript flat
presets now parse the complete source tree before the starter's architecture
rules, with an explicit error/warning policy. Next, Sentry/OpenTelemetry, Hono,
URI/YAML parsing, PostCSS, Vitest/Vite, WebSocket, and HTTP-client dependency
lines are patched; the full npm audit reports zero vulnerabilities. The local
unpublished Codefly JavaScript package remains open.
The required CI workflow now builds and vets every Go module, runs ordinary and
Codefly dependency-backed Go tests without sharing harness processes, enforces
Buf format/lint/build for both protobuf services, and runs frontend clean install,
lint, typecheck, unit tests, and production build. The SDK and CLI inputs are
commit/version pinned; temporary v1 Buf exceptions explicitly point to the P1
versioned-package migration instead of silently disabling protobuf checks. The
last org-less identity lookup also selects an explicit NULL-org SQL shape, so an
optional organization is never represented as an empty UUID parameter.
The security workflow now scans complete Git history and pull-request dependency
changes, enforces reviewed source/license policy, builds and scans every runtime
image, emits independently scanned SPDX SBOMs, and records GitHub provenance and
SBOM attestations for exact image digests on main and release tags. Runtime
images use pinned current bases, non-root users, and no package managers; the
database migration image is a minimal Postgres-only binary instead of the
vulnerability-heavy all-driver upstream CLI.
The frontend now uses Next.js 16.2.11 and React/React DOM 19.2.8 with the stable
React Compiler.
App Router page and layout files remain server boundaries, while authentication,
plugin routing, accordions, and browser-origin discovery live in narrow client
islands. A script-free external-store theme provider replaces `next-themes`, and
compiler correctness findings are enforced. The same baseline is implemented in
the Codefly Next.js factory; Codefly's shared Node upgrader now understands npm
11 workspace arrays, upgrades peer-coupled packages atomically per workspace,
and reports workspace-only changes. Publishing core and agent 0.0.115 remains a
separate release step before consumer pins move.
The CI workflow now exposes an aggregate clean-checkout check that cannot pass
when a prerequisite is skipped, runs version tags, and promotes only after the
complete Playwright suite succeeds against a freshly created Codefly `dev-admin`
fixture stack. The stack uses resolved service endpoints, a production frontend
build, real dependencies and migrations, unconditional cleanup, and retained
failure traces. `RELEASE_GATES.md` defines the required-check configuration.

Acceptance criteria:

- A clean checkout can install, lint, typecheck, test, and build without sibling
  repositories.
- Production dependency audit has no unaccepted high/critical findings.
- Every release has an SBOM and traceable generated-code inputs.

## Phase 1: Codefly contract compiler and protocol convergence

Exit gate: all public and internal API surfaces derive from one versioned contract,
Connect/gRPC/gRPC-Web use one handler, route and policy parity are enforced, and a
clean regeneration produces no diff.

### P1.1 Version the API and modernize Buf

- Use the accepted namespace family in `module/CONTRACT_VERSIONING.md`:
  `saas.accounts.v1` for the product API, `saas.gateway.auth.v1` for the
  internal auth/PDP API, and `saas.policy.v1` for shared method options.
- Move proto files into the matching package directory and upgrade to Buf v2 with
  STANDARD lint rules.
- Add Buf breaking-change comparison against the latest release tag.
- Split the monolithic proto by bounded context while keeping one module contract.
- Define deprecation and support windows before renaming existing services.

Implemented: both protobuf modules use Buf v2 `STANDARD` lint, stable package
directories, pinned generation templates, Codefly-owned regeneration with
clean-output enforcement, and breaking comparison against the latest prior
`v*` tag containing that stable package. Accounts is split into 22 bounded
contexts; exact legacy procedure aliases are retained at the edge for the
documented migration window.

### P1.2 Define method policy options

Create custom protobuf options representing:

```text
exposure: public | authenticated | internal
tenant: none | user | org_member | org_admin | org_owner | team_member
permissions/scopes: repeated strings
resource bindings: request field -> owner/org/team lookup rule
mfa: none | enrolled | recent_step_up
audit: event name + success/failure policy
idempotency: forbidden | optional | required
rate-limit class and request/response sensitivity
```

Options must be declarative and finite; do not embed a general policy language in
protobuf. Complex state-dependent authorization stays in domain code.

Implemented: `saas.policy.v1.MethodPolicy` defines finite exposure,
tenant, permission/scope, resource-binding, MFA, audit, idempotency, rate-limit,
platform-role, and sensitivity vocabulary as extension `51000` of
`MethodOptions`. All 125 RPCs are annotated. Exact local Codefly generation
emits Go and TypeScript bindings; runtime admission and the review matrix read
the descriptors directly; validation rejects incomplete policy, bad vocabulary,
and invalid resource field paths. `module/METHOD_POLICY.md` defines the
fail-closed authoring invariants.

### P1.3 Generate one service catalog

Implement a Codefly generator that consumes descriptors and emits a normalized
catalog. Generate from it:

- Connect service registration.
- Gateway exact path matchers and upstream ownership.
- Authorization method metadata.
- Optional grpc-gateway mappings and OpenAPI.
- TypeScript clients and frontend permission constants.
- Human-readable API/policy documentation.
- A machine-readable inventory used by parity tests.

Delete manual `*_gen.go` registration and route YAML only after generated output
has functional equivalence and migration tests.

Implemented foundation: `saas.catalog.v1` supplies generated Go and TypeScript
catalog types, and the accounts compiler discovers every service directly from
the registered protobuf file graph. It emits a deterministic 25-service,
125-method `generated/service-catalog.json` with canonical procedures,
request/response and streaming shape, all HTTP bindings, full typed policy,
source provenance, and Codefly ownership. Semantic validation and CI fail on
missing/invalid policy, duplicate routes, incomplete ownership, inventory
ordering/grouping drift, unknown permission/scope vocabulary, invalid
entitlement definitions, transport mismatch, or generated-file drift. The
catalog plus a strict finite implementation-binding file now generates every
Connect registration and compile-time handler-interface assertion. Runtime mux
parity proves all 125 catalog procedures resolve through 25 Connect service
patterns; the former handwritten registration block is gone. The gateway
compiler emits a typed 355-route public-edge inventory, generated
auth-sidecar/Envoy Connect whitelist, and exact/path-template Istio manifest
with Codefly ownership and named endpoints. Internal methods are omitted and
public/protected behavior is descriptor-derived. Istio activation waits for the
frontend/static route catalog so deployment cannot regress to an API-only
surface. The authorization compiler now emits `saas.authz.methods.v1` for all
126 procedures with complete policy, deterministic policy fingerprints, and
edge limiter behavior. Auth-sidecar joins Connect and known REST routes to its
generated policy lookup; parity repaired stale registration exposure and URL
classification is gone from limiter failure handling. The generated
`saas.rest.surface.v1` projection now drives 119 opt-in descriptor routes,
accounts registration/allowlisting, auth-sidecar routing, and verified public
OpenAPI; internal RPCs have no HTTP annotations and five non-protobuf routes
remain explicit extensions. The frontend projection now generates typed
Connect clients for all 25 accounts services plus finite permission, API-key
scope, and entitlement constants. Frontend role gates, common client hooks,
and entitlement administration consume those types, while Go and Vitest parity
tests pin all 126 procedures. The deployment projection now compiles a strict
module topology into the actual Codefly module/service manifests, a typed
8-service/12-endpoint/8-dependency inventory, and 16 default-deny Kubernetes
NetworkPolicies. Each service edge is limited to declared endpoint ports;
descriptor parity requires accounts gRPC, Connect, and REST endpoints. The
final P1 producer now discovers all 36 Next.js pages and joins them with a
strict binding for three built-in plugins and 25 canonical navigation items.
Generated TypeScript feeds the current plugin registry, sidebar, command
palette, and user menu; permissions and frontend ownership are validated
against the service and deployment catalogs. All planned P1 catalog producers
are present. `P1-GEN-009` completed the final retirement: raw-gRPC
registrations now derive from the catalog implementation binding, while one
five-route non-protobuf source replaces the descriptor-equivalent REST YAML
fixtures and rejects future collisions.

### P1.4 Converge protocols and gateway deployment

- Serve Connect, gRPC, and gRPC-Web from the same Connect-Go handler/port.
- Keep REST transcoding opt-in per service or method.
- Route frontend pages/assets explicitly, including the SPA/Next catch-all.
- Choose one deployed edge path: Istio/Envoy data plane plus an auth/PDP service.
  Do not retain a second gateway implementation with a different route inventory.
- Generate Codefly endpoints and network policies from service ownership.

Implemented: `deployment/topology.bindings.codefly.yaml` is the single topology
source for all eight Codefly services, 12 endpoints, eight dependency edges,
two module-interface exports, and finite public egress. Generation writes the
runtime `module.codefly.yaml`/`service.codefly.yaml` files and removes the broad
intra-namespace allow policy in favor of dependency-specific ingress/egress.
DNS, Istio control-plane/ingress, and HTTPS egress remain explicit exceptions.
The same source declares `auth-sidecar` as the module service entry. Because the
sidecar depends on the frontend, accounts, and cache—and those dependencies pull
in the remaining infrastructure—Codefly resolves the complete eight-service
graph from either the module directory or the repository's single-module
workspace without a manually repeated service name. Its public HTTP endpoint is
the product ingress; the frontend remains private behind it. The separate
marketing service owns apex/`www`/docs ingress and consumes pricing only through
the configured public HTTPS projection, without a Codefly product dependency.

Reference: <https://connectrpc.com/docs/go/getting-started/> and
<https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/ext_authz_filter>.

### P1.5 Contract CI

CI must run:

- `buf format`, lint, generation, and breaking checks.
- Generated-diff checks.
- Proto/service/Connect/gRPC/REST/OpenAPI/Envoy/TypeScript route parity.
- Public/internal exposure snapshots reviewed like API changes.
- Clean-checkout Go and frontend builds.
- Deployment rendering, schema validation, and network-policy tests.

The format, lint, descriptor build, release-tag breaking, Codefly generation,
and generated-output clean-diff gate are active. Codefly uses exact local
plugins in CI, avoiding registry availability and quota as release dependencies;
the remote templates remain version-pinned fallbacks. Service, authorization,
route, frontend, deployment-topology, Codefly-manifest, and NetworkPolicy drift
are all rejected. Frontend page/plugin/navigation catalog drift is rejected as
well. Pinned Kubernetes/CRD schema validation remains open.

## Phase 2: identity, database, and job durability

Exit gate: tenant isolation does not depend on an application-settable bypass,
session changes are atomic and current, asynchronous work survives failures, and
privacy/billing workflows are recoverable.

### P2.1 Database authority separation

- Create migration-owner, tenant-runtime, and privileged-worker roles.
- Give the tenant runtime no `SUPERUSER`, `BYPASSRLS`, role membership, or ability
  to assume the privileged role.
- Remove the `app.bypass` policy mechanism.
- Inventory every table by tenant/global classification and enforce `FORCE ROW
  LEVEL SECURITY` where appropriate.
- Replace universally readable user rows with narrow lookup APIs/views.
- Add migration tests that inspect grants, role membership, owners, and policies.

### P2.2 Sessions, refresh rotation, and organization switching

- Atomically consume/rotate refresh tokens with a row lock or compare-and-swap.
- Re-resolve status, memberships, roles, platform role, and assurance on refresh.
- Revoke or version sessions when access-relevant state changes.
- Enforce absolute and idle session lifetimes plus per-device management.
- Add `SwitchOrganization` as a token exchange after verifying current membership;
  the frontend selector must never merely substitute a request organization ID.

### P2.3 Reusable inbox/outbox worker platform

- Define common inbox, outbox, attempt, lease, schedule, dead-letter, and audit
  primitives.
- Make handlers idempotent and observable; define ordering keys where needed.
- Add worker heartbeats, graceful shutdown, bounded concurrency, poison-message
  isolation, replay, and operational dashboards.
- Migrate Stripe, outbound webhooks, email, notifications, audit exports, privacy
  jobs, and agent approvals onto the platform.

### P2.4 Privacy and retention workflows

- Build a data inventory and purpose/retention classification.
- Bind every export/deletion request and status read to the verified subject.
- Run durable jobs, upload encrypted artifacts to object storage, and issue
  short-lived signed URLs after recent authentication.
- Implement deletion/anonymization by correct user identifiers, including
  billing/legal holds, organization ownership transfer, retries, and audit.
- Test exported content against the inventory so new data stores cannot be omitted.

### P2.5 Entitlements and feature access

- Separate payment state, subscription state, product catalog, and computed
  entitlements.
- Make entitlement checks tenant-scoped, cached with explicit invalidation, and
  available to backend, frontend, gateway, and plugin manifests through generated
  types.
- Support seats and usage as separate meters with reconciliation jobs.

### P2.6 Principal authority and Work Context

- Treat every direct RBAC subject as a Principal, not as a human user. Teams
  remain indirect/group subjects. Preserve the legacy `SUBJECT_KIND_USER = 1`
  protobuf alias for compatibility while making `SUBJECT_KIND_PRINCIPAL = 1`
  canonical and migrating stored `user` assignments to `principal`.
- Issue short-lived, audience-bound Codefly Work Context capabilities from the
  permissions plugin for a new Task/root Session, another root Session under
  the same Task, or an attenuated child-agent Session.
- Resolve owner membership, active Agent Principal, exact requested
  resource/action/scope grants, team attribution, and monotonic organization /
  Principal authorization revisions inside one verified `service-postgres`
  Reader transaction. A request field or cached display context may not select
  the authoritative tenant or Principal.
- Keep Task and Session lifecycle rows in the product. Accounts owns current
  identity/RBAC and capability exchange only.
- Cache permission computations only behind revisioned keys and explicit
  invalidation. Issuance and row-level authority must fail closed when current
  revision state cannot be established; stale cache may narrow presentation but
  never widen execution or reads.

Implemented foundation: `WorkContextService` contributes three generated
gRPC/Connect/REST operations and uses the shared Codefly Ed25519 Work Context
SDK. Migrations 78–79 add authorization revisions and principal-uniform role
subjects. The store binds verified tenant/owner scope, checks exact owner and
Actor authority, and signs only current facts. Compile, RPC attenuation, direct
Agent Principal, human RBAC, cross-tenant RLS, database-role, grant, inventory,
and policy tests pass against fresh Codefly-managed PostgreSQL.

Acceptance criteria:

- One human owner can authorize a registered Agent Principal without encoding
  the Agent as a user.
- A child Session can only attenuate its parent capability and cannot change
  Task owner or tenant.
- Revoked membership, revoked Actor, changed role/scope, stale revision, foreign
  tenant, wrong audience, expiry, and replay-policy violations fail closed.
- A real product consumes the capability through released Codefly/Warden SDKs
  without importing Accounts internals or scanning Codefly carriers.

## Phase 3: plugin platform and Mind delegation

Exit gate: compile-time plugins are the default extension mechanism, Warden can
load trusted runtime plugins through signed manifests, and Mind uses durable,
audience-bound capabilities rather than broad user tokens.

### P3.1 One plugin contract

Unify the frontend plugin interfaces around a versioned manifest containing:

```text
id, version, api compatibility, frontend entry, backend services,
routes, navigation, permissions, entitlements, feature flags,
configuration schema, migrations, event subscriptions,
allowed origins/egress, integrity, and signature metadata
```

Generate frontend route/nav registries, backend registration, gateway routes,
Codefly endpoints/networking, configuration UIs, compatibility checks, and docs.

### P3.2 Trusted compile-time plugins

- Make local typed plugins the default for ordinary SaaS products.
- Support lazy page components, route-level error boundaries, plugin-scoped query
  keys, navigation, settings, permissions, and entitlements.
- Detect duplicate IDs, routes, navigation keys, migrations, and permissions at
  generation time.
- Wire the active admin shell exclusively from the generated registry.

### P3.3 Warden runtime plugins

- Load only from a controlled same-origin registry.
- Verify signed manifests and pinned artifact hashes before activation.
- Perform host API/version and backend/frontend manifest handshakes.
- Apply permission and entitlement ceilings, CSP/egress allowlists, rollout,
  health, rollback, and kill-switch controls.
- Treat runtime JavaScript as trusted host code. Use an iframe/worker/remote-app
  boundary when code is not fully trusted.

Reference: <https://nextjs.org/docs/app/guides/lazy-loading>.

### P3.4 Mind workload identity and capabilities

- Authenticate workloads with SPIFFE/SPIRE or an equivalent short-lived workload
  identity, not a shared cluster token.
- Define agent principals separately from users and API keys.
- Issue signed, audience/subject/org/action/resource-bound, short-lived, one-use
  capabilities with a manifest-defined maximum authority.
- Persist approval requests, decisions, policy version/hash, risk inputs,
  approvers, redemption, expiry, and revocation.
- Require explicit approver roles and recent MFA for high-risk actions.
- Provide audit, notification, timeout, cancellation, and replay-safe execution.

## Phase 4: product and operational maturity

Exit gate: the starter supports the full common SaaS lifecycle, has reliable
operations and recovery, and can be released as a versioned Codefly building block.

### P4.1 Account and tenant lifecycle

- Email verification, secure recovery, passkeys/WebAuthn, identity linking, and
  session/device management.
- Invitations with expiry, revocation, domain rules, resend controls, and safe
  acceptance for existing/new users.
- Organization ownership transfer, member suspension, deletion, export, and
  domain/SSO policy.
- Team nesting policy, bulk administration, and SCIM-ready identifiers.

### P4.2 Notifications and communication

- User and organization preferences by channel/event.
- Durable in-app, email, Slack, and webhook delivery.
- Templates with versioning, localization readiness, previews, test sends, and
  safe variable schemas.
- Digests, quiet hours, unsubscribe, bounce/complaint handling, and delivery logs.

### P4.3 Billing depth

- Seat reconciliation, usage meters, trials, coupons, taxes, invoices, grace
  periods, dunning, cancellation, reactivation, and plan migrations.
- Customer-visible billing history and admin/support reconciliation tooling.
- Sandbox clocks and deterministic lifecycle tests.

### P4.4 Administration and support

- Scoped support roles, time-bound impersonation with user-visible banners,
  reason/ticket capture, and immutable audit.
- Organization/user search with privacy-aware result shaping.
- Feature/entitlement overrides with expiry and approval.
- Event, job, webhook, and billing replay tools with dry-run support.

### P4.5 Reliability, security, and releases

- OpenTelemetry traces/metrics/log correlation, SLOs, dashboards, and alerts.
- Backup, restore, point-in-time recovery, disaster-recovery exercises, and
  migration rollback/forward-fix procedures.
- Pod disruption, autoscaling, resource limits, topology spread, and zero-downtime
  schema/application rollout tests.
- Threat modeling, penetration testing, secret rotation, incident response, and
  security disclosure process.
- Semantic versions for the starter, contracts, generator, and plugin SDK, with
  upgrade guides and compatibility fixtures.

## CI and release gates

The required pipeline, in order:

1. Formatting and static checks for Go, TypeScript, protobuf, YAML, Rego, SQL.
2. Dependency, secret, license, and generated-provenance checks.
3. Unit and property tests.
4. Database migration and RLS tests with each real database role.
5. Contract generation, clean-diff, and breaking checks.
6. Gateway/protocol/authz integration tests.
7. Worker failure/retry/concurrency tests.
8. Frontend component, accessibility, and production-build tests.
9. Playwright journeys against an explicitly configured fixture stack.
10. Deployment render/schema/policy tests.
11. Ephemeral-environment smoke tests for OAuth sandbox and Stripe sandbox.
12. Signed artifact/image publication with SBOM and release notes.

No phase exits with skipped security tests or an undocumented manual deployment
step.

## Definition of done for every TODO

An item can be checked only when:

- The implementation and generated artifacts are committed.
- Unit and negative tests cover the change.
- Relevant cross-tenant, retry, concurrency, or protocol tests exist.
- Logs, metrics, audit events, and failure behavior are defined.
- Configuration and secrets are schema-validated and documented.
- CI runs the tests from a clean checkout.
- Migration, rollout, compatibility, and rollback/forward-fix are documented.
- `TODO.md` contains a short evidence link or commit/PR reference.
