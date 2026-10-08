# saas-starter — Module Reference

Multi-tenant SaaS backend with three-layer authorization (handler gates + RBAC
+ Postgres RLS), a Next.js authenticated product, a separately deployable
public marketing site, and per-service introspection endpoints.

A codefly **module** is a collection of **services**; each service owns its own proto. The accounts service of saas-starter exposes a self-describing catalog of its OWN RPCs / RBAC vocabulary / RLS-protected tables / scopes. A "module-level" view = aggregation across every service's catalog and is a separate concern (CLI / gateway / a downstream aggregator) — not encoded in any single proto.

## Quick links

- Functional contract (the *what*, with the `HOST-*` user stories): the
  umbrella docs repository's `modules/saas-starter.md`; this file stays the engineering
  reference (the *how*), and [README.md](./README.md) links the two.
- The one plan — every `HOST-*` story, its status and its proving test:
  [docs/PLAN.md](./docs/PLAN.md). Superseded plans sit under `docs/historical/`.
- Composing this module into a downstream workspace: [Composing this module into a workspace](#composing-this-module-into-a-workspace)
- Runnable local product with real identity: [LOCAL_DOGFOODING.md](./LOCAL_DOGFOODING.md)
- External-provider configuration groups: [LOCAL_DOGFOODING.md § Configure providers through Codefly](./LOCAL_DOGFOODING.md#configure-providers-through-codefly)
- Runtime capability owners and provider boundaries: [Runtime capability ownership](#runtime-capability-ownership)
- SigNoz dashboard/alert provisioning qualification: [module/SIGNOZ_PROVISIONING.md](./module/SIGNOZ_PROVISIONING.md)
- accounts service introspection (after `codefly run`): `GET /v1/.well-known/service-info`
- Connect-RPC: `saas.accounts.v1.IntrospectionService/GetServiceInfo`
- Code: `pkg/business/introspection.go`
- Test: `pkg/business/introspection_test.go`
- Source-of-truth protos: `module/services/accounts/proto/saas/accounts/v1/*.proto` (each bounded context owns one file)
- Normalized generator input: `module/services/accounts/generated/service-catalog.json`
- Catalog contract and workflow: `module/SERVICE_CATALOG.md`
- Generated frontend clients and vocabulary: `module/FRONTEND_CATALOG.md`
- Generated frontend plugin routes/navigation: `module/FRONTEND_PLUGINS.md`
- Generic event meters, quota semantics, and product integration: `module/USAGE_METERING.md`
- Evidence-bound trust claims and adopter responsibilities: `module/TRUST_CAPABILITIES.md`
- Which claim in the root architecture documents is backed by what, and which
  are kept only as history: [CLAIM_INVENTORY.md](./CLAIM_INVENTORY.md)
- Postgres roles, grant rules, and RLS authority: `module/DATABASE_AUTHORITY.md`
- Generated Codefly topology and NetworkPolicies: `module/DEPLOYMENT_TOPOLOGY.md`
- Marketing runtime, deployment, and extraction contract: `module/services/marketing/README.md`
- Typed public brand and site configuration: `module/public/site.config.json`
- Generated PDP input: `module/services/accounts/generated/authz-methods.json`
- Authorization catalog and enforcement boundary: `module/AUTHORIZATION_CATALOG.md`
- Platform-functionality reference (external audit mapped to shipped/partial/gap): `PLATFORM_REFERENCE.md`
- Generated gateway inventory: `module/services/accounts/generated/gateway-routes.json`
- Gateway route contract and rollout boundary: `module/GATEWAY_ROUTES.md`
- Solution trust model, registration authority, and compatibility: `module/SOLUTION_REGISTRATION.md`
- Access-token signing-key rotation runbook: `module/KEY_ROTATION.md`
- Module-to-host internal gRPC transport, and the origin/key rules a consumer
  must follow: `module/INTERNAL_TRANSPORT.md`
- Generated REST inventory: `module/services/accounts/generated/rest-surface.json`
- REST/OpenAPI contract and extension boundary: `module/REST_SURFACE.md`
- Resource follow subscriptions over events and notifications: `module/FOLLOWS.md`
- The host as an OAuth 2.1 authorization server, its two client sources, and
  resource indicators: [The host as an OAuth 2.1 authorization server](#the-host-as-an-oauth-21-authorization-server)

## Architecture

```text
example.com / www.example.com             app.example.com
  ↓                                         ↓
marketing service                         product frontend
  ├─ repository content                     ↓ same-origin API proxy
  ├─ public plan projection                auth-gateway
  └─ fixed auth handoff                      ↓
                                           accounts service
                                             ├─ adapters/ — gRPC + Connect + REST gateway servers
                                             ├─ business/ — RBAC + RLS wrappers
                                             └─ infra/ — Postgres + Redis + Vault

Product API traffic
  ↓ Connect-ES (TS, generated from buf)
Gateway (Envoy/KrakenD) — merges per-service REST endpoints
  ↓ Bearer JWT or X-API-Key, plus X-Scopes
auth-gateway — validates token, stamps x-user-id / x-org-id metadata
  ↓ gRPC
accounts service
  ├─ adapters/ — gRPC + Connect + REST gateway servers
  ├─ business/ — Service methods (RBAC + RLS wrappers go here)
  └─ infra/    — Store impl (Postgres + Redis + Vault)
       ↓
     Postgres pool with BeforeAcquire SET ROLE app_tenant
     ↓
     RLS-enforced reads / writes
```

The marketing process has no authentication session, database, Vault, object
store, admin client, or product feature dependency. Product and marketing have
independent build, health, deployment, rollback, cache, and hostname policies.

## Runtime capability ownership

This is the authoritative ownership decision for
[epic #98](https://github.com/codefly-dev/module-saas-starter/issues/98) and the
[PostHog](https://github.com/codefly-dev/module-saas-starter/issues/127),
[Unleash](https://github.com/codefly-dev/module-saas-starter/issues/128), and
[SigNoz](https://github.com/codefly-dev/module-saas-starter/issues/131) provider
implementations. Ownership names the only implementation allowed to serve a
capability; it does not claim that every planned provider is released yet.

| Capability | Single owner | Allowed SDK and runtime surface | Explicitly excluded |
|---|---|---|---|
| Runtime feature flags | Unleash | The provider-neutral `feature-flags@1` SDK may evaluate flags with consumer-scoped Edge/browser or server tokens. The Unleash admin API is module-only; public clients reach only Unleash Edge. | Product analytics, replay, exception capture, and OTLP/APM |
| Product analytics | PostHog | The `product-analytics@1` browser/server APIs may capture registered events and perform consent-gated identify, alias, organization grouping, and privacy suppression. An event may carry a variant already evaluated by Unleash for experiment analysis. | Flag definition or evaluation, exception capture, and traces, metrics, or logs |
| Session replay | PostHog | A browser-only recorder may start after separate replay consent and redaction policy resolve to allow. It has no server SDK or implicit analytics-consent fallback. | Flags, errors, and APM signals |
| Error tracking | Sentry | Browser/server exception events, release/environment tags, source-map upload, and the error-issue workflow. Provisioning credentials remain build/provider-only. | Performance transactions, profiles, replay, logs, metrics, and feature flags |
| APM traces, metrics, and logs | SigNoz | Applications use OpenTelemetry SDKs and standard OTLP configuration through the in-graph collector. SigNoz receives those signals as the OTLP backend. | Product analytics, replay, feature flags, and the Sentry error-issue contract |

Provider manifests are allowlists. `provider-posthog` may project only
`product-analytics@1`; its browser initialization must disable flag remote
configuration and exception autocapture, and replay must remain stopped until
replay consent is granted. `provider-unleash` may project only
`feature-flags@1`. Sentry may project only `error-tracking`; the starter fixes
Sentry trace sampling at zero and installs no Sentry tracing integration.
`provider-signoz`, if API qualification succeeds, may manage exactly owned
dashboards and alerts but projects no application runtime configuration. In
particular, dashboard provisioning is not an application telemetry contract:
application OTLP endpoints remain instrumentation configuration resolved
through the telemetry service.

The starter keeps the capability configurations independent. Selecting
PostHog never selects flags, errors, or observability; selecting Sentry never
selects tracing; selecting an OTLP exporter never selects error tracking.
PostHog's current direct HTTP adapters expose capture, identity, grouping, and
suppression only, so no bundled vendor SDK can silently enable an overlapping
feature.

### Database-backed feature-flag retirement

The `feature_flags` table, read-only `ListFeatureFlags` API, and
`/admin/platform/feature-flags` page are a legacy migration inventory, not a
runtime flag owner. No application runtime evaluates or mutates that table: the
former database evaluator, combined flag/entitlement checker, and mutation path
have been removed. The published v1 `UpsertFeatureFlag` RPC remains deprecated
for wire compatibility, but its handler always fails closed and the runtime
database roles have no write grants. The remaining surface is retired in this
order:

1. Land the
   [`feature-flags@1` contract](https://github.com/codefly-dev/core/issues/281),
   [Unleash service](https://github.com/codefly-dev/module-saas-starter/issues/130),
   and [Unleash provider](https://github.com/codefly-dev/module-saas-starter/issues/128).
2. Export each legacy row through the read-only inventory API and map `enabled`,
   `rollout_percent`, and
   `target_org_ids` to one explicitly owned Unleash project/environment and its
   strategies. Reject ambiguous mappings, import once, then verify equivalent
   evaluations against fixed organization fixtures. Do not add dual reads or
   dual writes.
3. Bind consumers to `feature-flags@1` and remove the admin route/navigation,
   list RPC and messages, business/infra read methods, generated read surfaces,
   permissions, and tests in the same cutover. Keep the deprecated v1 mutation
   compatibility shim until the stable-major support policy permits removal.
4. After the imported project is verified and no legacy consumers remain, add
   a forward migration that drops `feature_flags` and its grants/indexes. The
   historical migration that originally created the table remains immutable.

Entitlements and plan gates stay in Accounts and Postgres. They answer whether
an organization bought or is allowed a product capability; Unleash answers
which runtime behavior is rolled out. A flag may disable entitled behavior but
must never grant an entitlement, raise a quota, or replace authorization. Each
product path checks its entitlement independently from its flag evaluation.

## The host as an OAuth 2.1 authorization server

The host signs people in for its own public clients — an add-in, a CLI, a mobile
app, an MCP client — and it is the **only** sign-in UI: a client never talks to
an identity provider. Two spellings of the same authorization server exist, over
one registry, one authorization-code table, one minter and one session kind.

| Surface | Path | Owner |
|---|---|---|
| RFC 8414 authorization-server metadata | `GET /.well-known/oauth-authorization-server` | accounts (`pkg/business/oauth_authorization_server.go`), presented at the edge by the frontend proxy |
| Authorization endpoint | `GET /oauth2/authorize` | frontend (`src/app/(auth)/oauth2/authorize/route.ts`) → the login page → the consent page |
| Token endpoint (RFC 6749 form-encoded) | `POST /oauth2/token` | accounts (`pkg/adapters/oauth_http.go`) |
| The first registered client's own RPCs | `/v1/auth/clients/validate`, `/v1/auth/clients/authorize`, `/v1/auth/token` | accounts (`pkg/business/client_authorization.go`) |
| RFC 9728 protected-resource metadata for a solution's MCP endpoint | `GET /api/solutions/<id>/proxy/.well-known/oauth-protected-resource` | the solution's **runtime**, reached through the frontend's solution proxy (the gateway serves a solution's `.well-known` GET unauthenticated) |
| The MCP endpoint itself | `POST /api/solutions/<id>/proxy/mcp` | the solution's runtime, same route |

**The issuer is `APP_BASE_URL`.** RFC 8414 §2 requires an authorization
server's issuer to be an https URL, and a client that discovers the metadata
then verifies `iss` against it — so the published `issuer` and the `iss` of
every minted token are **one configured value**, resolved once at startup, never
from the request's `Host`. A deployment with no `APP_BASE_URL` publishes no
metadata and keeps the pre-metadata literal issuer, so the two cannot disagree —
and the registered-client browser handoff keeps working there, because issuing a
code never depends on a published issuer. One that sets it mints the URL and
keeps **accepting** the literal until tokens carrying it expire, so the change
signs nobody out.

**Registration credentials keep their own issuer.** The module- and
solution-registration credentials are internal, with their own audiences and
their own verifiers in the gateway and the frontend. They are not OAuth and
publish nothing, so they stay on the fixed `saas-starter` issuer rather than
following `APP_BASE_URL`: moving them would have refused a solution's frontend
half while its gateway half registered, and an active registration needs both.

**The resource is the path a client can reach, which is the solution proxy.**
`/api/solutions/<id>/proxy/*` is the **only** public route to a solution's
backend: it forwards the caller's bearer to the gateway and already serves the
solution's `.well-known` anonymously. `/solutions/<id>/*` is the gateway's own
internal surface — on the public origin that prefix is a page of the frontend
that redirects to login — so the RFC 8707 resource is
`<base>/api/solutions/<id>/proxy/mcp` and nothing else. An audience naming the
internal path is one no client could ever present at the resource it describes.

**A denied solution request carries the discovery challenge.** The gateway is
where it is refused — it strips identity, runs ext_authz and answers itself,
never proxying — so a challenge the runtime would have sent cannot reach anyone
through it. Every protected `/solutions/<id>/*` answers 401 with
`WWW-Authenticate: Bearer resource_metadata="<base>/api/solutions/<id>/proxy/.well-known/oauth-protected-resource"`,
and the proxy route **passes that header through**: it is the only public way
out, so a challenge it dropped was one nobody could read. The host serves no
second copy of that document — two for one resource are two places to disagree,
and a client follows whichever URL the challenge names.

**Every `/.well-known/` path is public, served or not, matched as a path
SEGMENT.** A well-known URI is a reserved metadata namespace (RFC 8615) and every
discovery chain begins by fetching one with no credential, so the page
middleware's login redirect must never reach them — including the ones this host
does not answer, which must get the 404 that says so. A segment test rather than
a root prefix, because these documents are not all at the root: a solution's
protected-resource metadata sits beneath its own route, and `/solutions/<id>/` is
a page of the frontend. A browser follows a 307 and looks fine; a non-browser
client follows it, parses a login page as JSON, and concludes that this host is
not an authorization server, which is a report pointing nowhere near the
middleware that caused it. The predicate is `isPublic` in `frontend/src/proxy.ts`
and it is tested directly for exactly that reason.

**Consent is enforced by the host, not advertised to the page.** A grant request
for a client whose authorization requires consent is refused unless it states
that the person approved. That does not defend against a hostile browser — one
holding the session can claim anything — but it is what stops a browser-side
defect from issuing credentials nobody approved: the host does not rely on the
page having read `requires_consent` correctly.

**The wire names are the contract.** Accounts serialises OAuth snake_case and
the frontend **decodes** it (`features/auth/model/oauth-authorization.ts`),
failing closed on anything it cannot read. A cast would type-check while
producing `undefined` for every field, and a unit test on either side cannot
establish the agreement because each side's test fixes the shape that side
expects — so the seam is held from both ends, each spelled against the other's
own serialisation (`accounts/pkg/adapters/oauth_wire_contract_test.go` and
`frontend/.../model/__tests__/wire-contract.test.ts`).

The OAuth surface is **raw HTTP, not transcoded RPCs**, because its shape is the
contract: RFC 6749 §3.2 requires a form-encoded token request and §5.2 a
specific JSON error object, neither of which grpc-gateway produces. A client
written against a standard OAuth library could not use a transcoded RPC.

Two client sources, which cannot collide — a registry slug can never spell
`https://`:

- **Operator-registered.** `IDENTITY_REGISTERED_CLIENTS`, a JSON array in the
  `identity` configuration group. Unset registers none, which refuses the flow.
- **Client ID Metadata Documents.** A `client_id` that is an https URL is
  fetched (bounded size and time, **no redirect at all** per CIMD draft-02 §5.1,
  and guarded at the dial against every non-public destination — the IANA
  special-purpose registries for both families, not just loopback and RFC 1918),
  validated, and used as a public client for that one flow. Failures and invalid
  documents are **not** cached (§5.2), so a publisher who fixes one is not made
  to wait out a window; the abuse a negative cache would have bounded is bounded
  by the authentication rate-limit class on the routes that reach it.
  **Nothing durable is written.** Gated by
  `IDENTITY_CLIENT_METADATA_DOCUMENTS`, unset meaning refused. There is **no**
  dynamic client registration (RFC 7591) and no `registration_endpoint` in the
  published metadata.

**Resource indicators (RFC 8707).** An authorization request may name one
resource: a solution's MCP endpoint **as a client can reach it**, which on a
deployed cell is the host frontend's solution proxy —
`https://<host>/api/solutions/<id>/proxy/mcp`. The gateway's own
`/solutions/<id>/*` is an internal surface, and on the public origin that prefix
is a frontend page that redirects to login, so a resource spelled that way names
a document no client can fetch.

The resource is recorded on the authorization code, persisted on the session, and
carried as a second `aud` value beside the host's own — so every existing
verifier still accepts the token. A rotation reissues the binding from the locked
session row, never from the request.

**A solution's MCP endpoint admits only a token issued for itself** (the security
posture's SP-SOL-07). A token carrying just the host audience is a credential for
the host's own API, and the whole purpose of a resource indicator is that one
audience is not the other: it is refused there, with a 401 and a challenge saying
what to authorize for. A deployment that has not been told its own public address
cannot state which resource its tool endpoint requires, so it refuses by name
rather than admitting what arrived.

That line is drawn at the tool endpoint, not across every solution route. A
solution's other routes are the product's own API surface, reached by a signed-in
person whose session token names no resource; a resource-bound token is still
confined there to the solution it names, so one solution's token never reads
another's data.

**The resource identifier is compared EXACTLY, by code-point equality, at both
issuance and admission.** The resource URL is itself the audience value this host
mints; a solution's runtime publishes that same string as its `resource`; and RFC
9728 §3.3 has the client require the document's `resource` to equal the URL it
dialled. No step in that chain folds case or re-renders the URL, so a host that
accepted several spellings of one resource would have several resources — and a
variant it minted would be rejected by the very client it was issued for. Both
sides compare against `auth.SolutionMCPResource`, the single place the string is
composed. A deployment with no configured public address cannot state what the
expected identifier is, so it refuses to issue one and refuses to admit one.

**A token's audience set has three readings, not two.** Bound to nothing, bound
to one valid resource, or unreadable — and the third is refused on every path. It
cannot be carried as an empty resource, because empty means "bound to nothing",
which is the reading admitted most widely. Two resource audiences, or one that is
not shaped like a resource identifier (userinfo, a query, a fragment, a
non-loopback `http` origin), are refused. The verifier's shape rule is the
issuer's own `auth.ParseResourceIndicator`, so verification cannot drift looser
than issuance; the gateway spells the identical rule on its own side of the
module boundary because nothing crosses it.

**A sign-in state that cannot be recorded as used is refused** (handbook
SP-IDENT-04). While the consumption store is unavailable, "has this state been
used before?" has no answer, and admitting on no answer means the single-use
property does not hold for the duration of the outage. The refusal carries its
own error so an operator can tell a failing dependency from a replayed state; the
caller maps both to one sentinel before answering.

**RFC 8252 §7.3 is unconditional for loopback.** A native client takes an
ephemeral port from the operating system at the moment of the request, so it
cannot have registered the port it will listen on, and any port is allowed at
request time whether or not the registration named one. The port is the only
component relaxed — scheme, host, path and query must match, and userinfo and a
fragment are refused.

**This changes what an existing registration means.** The rule lives on
`RegisteredClient.AllowsRedirect`, which the operator-declared registry uses too,
so a client already declared with `http://localhost:3000/auth/callback` now
matches that path on **any** port. An operator who wrote the port expecting it to
pin should read it as no longer pinning: on loopback the port is not the thing
that identifies the recipient, which is why §7.3 requires this and why PKCE is
what protects the exchange.

**A session cookie is not a credential at this perimeter.** The gateway verifies
a bearer — an access token or an API key — and reads no cookie anywhere; a cookie
is how the frontend holds a session for its own pages. A caller presenting one is
told that, rather than reading "authentication required" while holding what looks
to them like a credential.

**Consent.** The host asks the person to approve a client by name when the
client registered itself by publishing a document (nobody but the person can
vouch for it) or when the request narrows the credential to a named resource.
An operator-declared client asking for nothing in particular keeps the silent
handoff it has today.

The consent screen always shows the origin the host **verified** — the one it
fetched the document from. A document's own `client_uri` is read by nothing: a
document at `https://client.example.com/doc` may claim any `client_uri`, and
displaying that claim would name a publisher the host never reached (CIMD
draft-02 §8.5). The client's `client_name` is shown as untrusted presentation
beside the verified origin, never instead of it.

A redemption is checked against the client's policy **as it stands then**, not
only against the code: a metadata client withdraws a callback by removing it
from its document, since there is no row to delete.

An MCP token is a **session** credential: the gateway stamps
`x-credential-kind: session` with a session id, so a solution's SDK mints the
viewer's Work Context from it exactly as from a browser session. Lifetime,
refresh rotation and revocation are the registered-client tokens'.

## Three layers of authorization

| Layer | Where | What it asserts |
|---|---|---|
| **L1** Handler gates | `adapters/auth.go`, `adapters/connect_auth_interceptor.go`, `adapters/rpcs.go` | "Should this caller be ALLOWED to invoke this RPC?" — `requireAuth` / `requireOrgMember` / `requireOrgAdmin` / `requirePlatformAdmin` / `requireMFA` / `requireScope`, plus rate-limiting. |
| **L2** RBAC | `business/service.go:CheckPermission` (+ Postgres `roles` / `role_permissions` / `role_assignments`) | "Does this caller have THIS capability for THIS resource?" — wildcard support (`*:*`, `users:*`, `*:read`), team inheritance. |
| **L3** RLS | Postgres `ROW LEVEL SECURITY` + `WithOrgTx` / `WithUserTx` / `WithControlPlane` | "Even if L1+L2 said yes, does the row physically belong to the scope this transaction selected?" — fail-closed via `BeforeAcquire SET ROLE app_tenant`. |

The three guarantees are complementary, not interchangeable: each answers a
question the others do not ask, so none of them is a backstop for a bug in
another.

- RLS constrains a statement to the scope its transaction selected. It does not
  decide whether the caller may select that scope, and it cannot tell a
  correctly-scoped write that should not have been allowed from one that should
  — a proposed team member who does not belong to the parent organization is a
  well-scoped row.
- L2 answers whether this caller holds this capability, having been told which
  organization the caller is acting in. It does not verify that the subject of
  the operation belongs to that organization.
- L1 answers whether this RPC admits this caller at all. It reads identity
  headers stamped by the gateway and does not see rows.

What each layer does catch is the failure to *use* the next one: an unwrapped
Store call returns zero rows rather than every tenant's, because the pooled
connection is downgraded to `app_tenant` before the query runs. See `AUTHZ.md`
for the deep dive.

## RLS — scope inventory

Every public application relation carries exactly one scope, and the scope
decides its required database boundary. The inventory itself is **not repeated
here**: it lives once as the executable map `relationsByScope`
(`services/accounts/code/pkg/infra/postgres_role_hardening_test.go`), which a
live database is checked against, and once as the table in
[module/DATABASE_AUTHORITY.md](./module/DATABASE_AUTHORITY.md), which
`TestDatabaseAuthorityScopeInventoryMatchesCode` (`module/tools`) holds to it.
Read the inventory there; a third hand-maintained copy would only drift.

What is worth knowing here is the shape:

- **tenant** relations enter through `WithOrgTx(orgID)`. Policies are direct
  (`org_id` on the row), JOIN (`team_members` → `teams.org_id`), polymorphic
  (`audit_events`, `roles` — built-ins carry a NULL `org_id` and are globally
  readable), or self-referential (`organizations`).
- **user** relations enter through `WithUserTx(userID)` and are RLS-forced in
  the same way tenant relations are. The `WHERE user_id = $1` predicate is no
  longer the safety property — it was, before `34_rls_user_scoped`,
  `35_rls_sessions` and their successors brought these relations under forced
  policies.
- **global** catalogs carry no RLS; exact grants are their whole boundary.
- **pre-auth**, **job**, and **worker** relations are reachable only from the
  control-plane role or one named worker role, never from request traffic.

There is no application-settable RLS bypass. Cross-scope work runs under
`WithControlPlane`, which is a distinct database role
(`app_control_plane`) with `BYPASSRLS` and its own grants — not a flag a request
transaction can set.

## RBAC vocabulary

| Resource | Actions | Built-in roles holding write |
|---|---|---|
| `*` | `*` | admin |
| `users` | read, write | admin, editor (write); +viewer (read) |
| `teams` | read, write | admin, editor (write); +viewer (read) |
| `knowledge` | read, write | admin, editor (write); +viewer (read) |
| `billing` | read, write | admin (write); +editor (read) |
| `audit` | read | admin |
| `webhooks` | read, write | admin |
| `api_keys` | read, write | admin |

Built-ins: **admin** (wildcard), **editor** (read+write on domain), **viewer** (read-only). Custom roles can be created per org via `CreateRole(orgId)`.

## API-key scopes

| Scope | Description |
|---|---|
| `*:*` | Root |
| `users:read|write` | List/manage users |
| `orgs:read|write` | Read/manage org metadata + members |
| `teams:read|write` | List/manage teams |
| `roles:read|write` | List/manage roles + assignments |
| `api_keys:read|write` | List/revoke API keys (a key never creates one) |
| `audit:read` | Read audit events |
| `invitations:read|write` | List/manage invites |
| `webhooks:read|write` | Manage webhook subs (incl. RotateSecret needs `:write` + MFA) |
| `billing:read|write` | View / open billing portal |
| `entitlements:read` | View overrides + usage |

Wildcard semantics: `users:*` matches all `users:X`; `*:read` matches read across resources.

A scope grants only the RPCs whose method policy declares it: accounts checks
the declared scopes on every RPC before any handler runs, an RPC that declares
none refuses API keys, and a key with no scopes has no authority. Only an
interactive session can create an API key: `CreateAPIKey` declares no scope and
refuses every key, whatever it carries. The rules and the transports they cover
are in [module/services/accounts/TRUST_BOUNDARY.md](module/services/accounts/TRUST_BOUNDARY.md#api-key-scopes).

## RPC catalog (selected)

The full machine-readable list is derived from protobuf descriptors and `saas.policy.v1.method_policy`; only editorial summaries remain in `pkg/business/introspection.go:rpcDescriptions`. The complete deterministic generator input is checked in at `module/services/accounts/generated/service-catalog.json`, and the redacted runtime view is served at `GET /v1/.well-known/service-info`. Highlights:

### IntrospectionService

| RPC | Path | Authz |
|---|---|---|
| `GetServiceInfo` | `GET /v1/.well-known/service-info` | public (privileged tiers redacted for anonymous callers) |

### PermissionService — RBAC

| RPC | Path | Authz | Audit |
|---|---|---|---|
| `CreateRole` | `POST /v1/roles` | org_admin | ✓ |
| `ListRoles` | `GET /v1/roles?org_id=…` | auth | |
| `DeleteRole` | `DELETE /v1/roles/{id}` | platform_admin | ✓ |
| `AssignRole` | `POST /v1/role-assignments` | org_admin | ✓ |
| `RevokeRole` | `DELETE /v1/role-assignments` | org_admin | ✓ |
| `ListRoleAssignments` | `GET /v1/role-assignments?org_id=…&subject_id=…` | org_member | |
| `CheckPermission` | `POST /v1/permissions:check` | public (internal-only) | |

### OrganizationService

`CreateOrganization`, `GetOrganization`, `UpdateOrganization`, `ListMembers`, `AddMember`, `RemoveMember`, `LeaveOrganization`, `DeleteOrganization` — all org-scoped, RLS-wrapped. Creation follows `ORGANIZATION_CREATION`; leaving and deleting (archive) are described in [AUTHZ.md](./AUTHZ.md#leaving-deleting-and-creating-organizations-973). The platform view is `PlatformAdminService/ListAllOrganizations` and `GetOrganizationRoster`.

### AuthService organization exchange

`SwitchOrganization` is an authenticated, target-id-only token exchange. The
server locks the current device session using verified `sub` + `sid`, resolves
the target membership and roles from PostgreSQL, and returns a new access token
without rotating the refresh credential or changing device/lifetime state. The
frontend decodes the signed `org` claim into one global organization context;
tenant-scoped pages do not maintain independent organization filters.

### TeamService

`CreateTeam`, `ListTeams`, `AddMember`, `RemoveMember`, `ListMembers` — team_id-scoped operations resolve team→org via `WithControlPlane` first, then enter `WithOrgTx` for the actual write. A caller that already carries a verified org claim skips the resolve.

### BillingService public catalog

`ListPublicPlans` (`GET /v1/public/plans`) is a public, credential-free
projection of the authoritative server catalog. It exposes only presentation,
checkout eligibility, trial/tax terms, and entitlement limits, plus a
deterministic revision. The marketing service never copies pricing into
content and disables a plan CTA when the catalog says checkout is unavailable.

### Other domain services

`UserService`, `APIKeyService`, `AuditService`, `AuditExportService`, `InvitationService`, `WebhookService`, `BillingService`, `PlatformAdminService`, `UsageService`, `SSOAdminService`, `MFAService`, `ConsentService`, `NotificationService`, `OnboardingService`, `GDPRService`, `UserSettingsService`, `AuthService`, `IdentityService`.

## Usage metering

`UsageService.ConsumeUsage` is an internal-only protobuf API for trusted product
services. Each logical operation supplies an organization, canonical meter,
positive quantity, idempotency key, and optional event time/dimensions. The
service resolves the plan/override inside the same tenant transaction, locks the
monthly aggregate, and persists an immutable accepted or rejected receipt.
Retries return that receipt without incrementing again; reusing the key with a
different payload fails. `UsageService.GetUsage` is the authenticated tenant
read path. Periods are UTC calendar months and `period_end` is exclusive.

Seats and API-key counts remain computed cardinality gauges. They must not be
written as usage events because their authoritative rows can be reconciled
directly. Their admission checks are serialized with the authoritative write in
one tenant transaction; pending invitations reserve seats, while expired
invitations and expired or revoked API keys release capacity. New event meters
become active by adding their canonical key to the product plan/override
catalog; unknown keys resolve to a disabled limit.

The protobuf and storage contract is product-neutral. The internal gRPC
listener is multiplexed onto the private REST h2c listener and is not a module
export. Cross-module callers use the named `accounts/authority` endpoint
(`P1-NET-007`), which serves only the module surface; `ConsumeUsage` is not on
it yet because it authorizes on the perimeter credential alone. Exporting the
mixed listener or making ingestion public is not an acceptable integration
shortcut. See `module/USAGE_METERING.md` for the complete producer contract.

## Frontend admin pages

Every page lives under `frontend/code/src/app/admin/`:

| Route | Backend RPCs |
|---|---|
| `/admin/roles` | `ListRoles`, `CreateRole`, `DeleteRole` |
| `/admin/teams` | `ListTeams`, `CreateTeam`, `AddTeamMember`, `RemoveTeamMember`, `ListTeamMembers` |
| `/admin/organizations` | `GetOrganization`, `UpdateOrganization`, `ListMembers`, `AddMember` (also the inline role change), `RemoveMember`, `LeaveOrganization`, `DeleteOrganization`, **`AssignRole`/`RevokeRole`/`ListRoleAssignments` via the per-member "Manage roles" dialog** |
| `/admin/organizations/settings` | `GetOrgSettings`, `UpdateOrgSettings` |
| `/admin/invitations` | `CreateInvitation`, `ListInvitations`, `RevokeInvitation`, `IssueInvitationLink` (copy an accept link) |
| `/admin/api-keys` | `CreateAPIKey`, `ListAPIKeys`, `RevokeAPIKey` |
| `/admin/webhooks` | full webhook CRUD + `RotateSecret`, `TestWebhook` |
| `/admin/audit-log` | `QueryAuditLog` |
| `/admin/sso` | `GetOrgSSO`, `StartSSOSetup`, `DisableSSO` |
| `/admin/billing` | `OpenBillingPortal`, `ListInvoices` |
| `/admin/platform/admins` | platform role grant/revoke |
| `/admin/platform/organizations` | `ListAllOrganizations`, `GetOrganizationRoster`, `CreateOrganization` (with `owner_user_id`), `UpdateOrganization`, `AddMember`, `RemoveMember`, `DeleteOrganization` |

Client-side gating uses `<RoleGate>` (`src/components/auth/role-gate.tsx`) — display-only; backend remains authoritative.

## Composing this module into a workspace

A downstream workspace does not fork or copy saas-starter. It **composes** the
module: Codefly resolves a pinned, content-addressed release into the consumer
under `modules/<name>/`, and the consumer only ever ADDS files beside the base —
never edits the base in place. All paths below are relative to that composed
module root (the `modules/<name>/` directory).

### Composing

```bash
codefly add module --agent saas-starter <name>
```

That creates the consumer's module shell; the base itself is never copied by
hand. Never compose with `rsync`, a directory copy, or a hand-edited manifest —
Codefly resolves the pinned release so the result has reproducible provenance.
Which release, and who must have signed it, is the pin below.

### Pinning the package by identity

A workspace resolves the published module package at run time. The reference
names the source repository and a package version rather than a path:

```yaml
modules:
  - name: saas-starter
    source: codefly-dev/module-saas-starter
    version: "0.1.0"
```

Resolution fails closed, so the workspace must also say who it trusts to have
signed that package:

```yaml
module-trust:
  repositories:
    codefly/saas-starter: https://github.com/codefly-dev/module-saas-starter
  signers:
    codefly/saas-starter:
      https://github.com/codefly-dev/module-saas-starter/.github/workflows/ci.yml@refs/heads/main: <base64 Ed25519 public key>
```

`codefly/saas-starter` is the package id from
`module/module.package.codefly.yaml`; the signer is the workflow identity the
release was signed under, scoped to that package id so its key vouches for no
other package. Every `module-package/vX.Y.Z` release publishes both
with the public key filled in — copy the block out of the release notes, or
download the release's `module-trust.yaml` asset, which is written during
signing from the key the signature was verified against and so always matches
that release.

Neither the notes nor that asset are themselves signed, so taking the key from
a release is trust on first use, not proof of it. What pinning buys is
everything after: once the key is in your workspace, every later release must
be signed by it or resolution fails closed. Establish it once, deliberately,
and review changes to it as you would any other credential in your repository.

Codefly then fetches the release, checks its detached signature and archive
digest against this policy, and materializes it into a content-addressed cache.
Nothing is written into the consumer's tree and no sibling checkout is needed —
which is what makes this the CI-portable route.

### Overlay discipline

Base files are **upstream-owned**: the package is the exact set of files the
release ships, and the base only moves via a new tag. A consumer composes by
ADDING files on the side — a product plugin package, extra services,
integration tests — never by editing a base file in place; the next compose
replaces an in-place edit with the base's copy. A change a base file genuinely
needs goes upstream: land it in canonical saas-starter, cut a new tag, and pin
it. The base gets stronger and every consumer benefits.

### Composing a subset of services

A consumer may compose only some of the module's services. The composed set is
the `services:` list in the consumer's `module.codefly.yaml`. `check` skips
manifest files that belong to a non-composed service and reports them as an
expected omission, not a missing base file — module-level files are always
enforced. So absent base files for a service you never composed are normal;
missing base files for a service you DID compose are a real failure.

For the frontend product-plugin overlay specifically — package layout,
generated projections, and gates — see
[docs/frontend-plugin-installation.md](./module/docs/frontend-plugin-installation.md).

## How to extend the module

### Adding a new per-tenant table

1. Write the migration (`store/migrations/N_create_X.up.sql`) with `org_id NOT NULL REFERENCES organizations(id) ON DELETE CASCADE`.
2. In a follow-up migration, enable RLS:
   ```sql
   ALTER TABLE X ENABLE ROW LEVEL SECURITY;
   ALTER TABLE X FORCE  ROW LEVEL SECURITY;
   CREATE POLICY X_tenant ON X
     USING (org_id::text = current_setting('app.current_org_id', true))
     WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
   ```
3. Add a `Store` method in `pkg/business/store.go`.
4. Implement in `pkg/infra/postgres_X.go`. Use `s.getQueryExecutor(ctx)` so context-scoped tx (from `WithOrgTx`) is reused.
5. Wrap every Service-layer call site in `s.store.WithOrgTx(ctx, orgID, ...)`.
   Cross-tenant workers require a dedicated least-privilege worker role and
   pool; do not add an application-settable bypass branch to a new policy.
6. Add a cross-tenant blocking test in `pkg/business/rls_X_test.go` mirroring `rls_audit_export_test.go`.
7. Add the table to `pkg/business/introspection.go:serviceRLSTables` so it shows up in `GetServiceInfo`.

### Adding a new RPC

1. Add the message + RPC to its bounded-context file under `proto/saas/accounts/v1`. Add a complete `option (saas.policy.v1.method_policy)`; missing or `UNSPECIFIED` policy is denied and fails tests. Annotate `google.api.http` only when REST exposure is intended.
2. From the accounts service directory, run `codefly generate proto --proto ./proto --output .. --template accounts/proto/buf.gen.yaml` (Docker, Codefly CLI ≥ 0.1.160; NEVER run `buf generate` directly). One run of the versioned proto companion regenerates Go, gRPC, Connect, gateway, the raw OpenAPI document and the modular TypeScript together, with plugins pinned by the image and a `goimports` pass. The older `--output . --local --template buf.gen.local.yaml` spelling, and the "companion first, then local" pair, no longer work from CLI 0.1.160: `--local` now runs inside the companion as well, where that template's `go run`/`npx` plugins fail. See [module/REST_SURFACE.md](./module/REST_SURFACE.md#regeneration).
3. Import browser types from their bounded module, for example `@/gen/saas/accounts/v1/teams_pb`; do not restore the former monolithic TypeScript barrel.
4. Implement: `pkg/business/<feature>.go` (the Service method, with a `WithOrgTx`/`WithUserTx`/`WithControlPlane` wrap), `pkg/infra/postgres_<feature>.go` (raw SQL), `pkg/adapters/rpcs.go` or a new `<feature>_rpcs.go` (handler authz + Validate + Service call), and a Connect adapter. Keep any still-manual gRPC/REST implementation wiring current.
5. For a new service only, add its finite implementation source to `pkg/adapters/connect_bindings.yaml`; never hand-register a Connect service or procedure. If it opts into REST, also classify the service as `generated` or `plugin` in `pkg/adapters/rest_bindings.yaml`. Existing services need no binding change when an RPC is added.
6. Add only the editorial summary to `pkg/business/introspection.go:rpcDescriptions`, then run `go generate ./pkg/business`, `go generate ./pkg/adapters`, and `go generate ./pkg/cataloggen`. This refreshes the normalized catalog, authorization catalog/matrix, auth-gateway policy lookup, Connect registration, REST registration/allowlists, filtered OpenAPI, and target-neutral gateway route artifacts. HTTP, authz, scopes, resource bindings, MFA, audit, rate, and sensitivity must come from descriptors; do not introduce another policy map or service list.
7. Frontend: a `useX` hook in `src/features/<feature>/service/{queries,mutations}.ts`, called from a UI in `src/features/<feature>/ui/`.

### Adding a new RBAC permission

1. Pick a `resource:action` pair. Add it to `pkg/business/introspection.go:servicePermissions` with description + which built-ins hold it.
2. Update `migrations/4_create_roles_permissions.up.sql` (or a follow-up migration) to grant it to the relevant built-in role.
3. If the FE displays a permission matrix, add the pair to `frontend/code/src/lib/permissions.ts`.

### Common pitfalls

- **Forgetting `WithOrgTx`** on a per-tenant table → fail-closed (zero rows). Loud test failures, not silent leaks. The `BeforeAcquire SET ROLE app_tenant` hook makes this fail-closed by default.
- **Using `pgx.BeginTxFunc` inside a Store method** → opens a fresh pool tx that ignores the WithOrgTx context. Always use `s.getQueryExecutor(ctx)` instead. See `postgres_org.go:CreateOrganization` and `postgres_permissions.go:CreateRole` for the context-tx-reuse pattern.
- **Test fakes that embed `business.Store` but don't override `WithOrgTx`/`WithUserTx`/`WithControlPlane`** → nil panic. Add pass-through implementations (see `sso_admin_test.go`).
- **Custom-role assignment without UI** — Use the `<ManageMemberRolesDialog>` shipped at `src/features/roles/ui/manage-member-roles-dialog.tsx`. Don't bypass via direct SQL.
- **Assigning a built-in role to a user in a NEW org** — `RegisterUser` only auto-assigns to the resolver-bootstrapped personal org. Explicit orgs need an explicit `AssignRole` call.

## Tests

```
pkg/business/rls_*_test.go              — 22 RLS cross-tenant blocking tests
pkg/business/module_test.go              — capabilities introspection smoke
pkg/infra/tenant_tx_test.go              — empty-orgID guard
```

Run from `module/services/accounts`:
`codefly test service --target ./pkg/business --filter TestRLS`.
Codefly owns the dependency graph and Docker lifecycle.

## Compatibility

- api service version: see `pkg/business/introspection.go:ServiceVersion` (`0.2.0` at time of writing). Module-level versioning is owned by `module.codefly.yaml`, not by any single service.

## Known issues

### Codefly daemon flake when tests run in parallel

`go test ./...` (the default `-p N` parallel mode) occasionally fails with:

```
WithDependencies failed: sdk.SetEnvironment: failed to get dependencies network mappings:
  rpc error: code = Unavailable desc = error reading from server: EOF
```

Cause: each test package (`pkg/business`, `pkg/infra`, `pkg/auth/pg`, `pkg/billing/pg`) calls `sdk.WithDependencies` which spawns the codefly CLI; under parallel mode several of those races on the codefly daemon's gRPC socket. The naming-scope mechanism gives each test its own DB/cache/vault, but daemon connection races still happen.

**Workaround:** run sequentially:

```
go test -p 1 -count=1 ./pkg/business ./pkg/infra ./pkg/adapters
```

Each package passes individually; the flake only appears when several processes start at once. Tracked upstream in `codefly-dev/core` — when the daemon serializes connection setup this issue goes away.

### LSP shows phantom `go mod tidy` errors on generated `pb.gw.go` files

Editor diagnostics may flag `github.com/oklog/run`, `go.opentelemetry.io/proto/otlp`, etc. as "not in your go.mod file" on generated grpc-gateway files. Those deps ARE present (`go build ./...` succeeds — verify with `cd module/services/api/code && go build ./...`). The diagnostics are stale LSP cache from a previous proto-gen iteration; they clear after restarting `gopls` or running `go mod tidy` in the api/code dir. No real build issue.
- Bump on proto-breaking changes (renamed RPCs, removed fields).
- Compatible-additive (new RPC, new field, new built-in role): patch-bump.
