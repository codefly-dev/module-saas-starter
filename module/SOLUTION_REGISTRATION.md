# Solution registration: trust model, authority, and compatibility

A **solution** is an independently deployed module this host has no build-time
knowledge of. It registers itself at runtime in two places — a gateway upstream
for `/solutions/{id}/…`, and a Module-Federation remote the frontend loads on
`/s/{id}`. This document states what registering a solution is *permitted to
mean*, who may do it, and what the host checks before any of it takes effect.

## 1. The supported trust model

**A registered solution is host-trusted.** Its remote is loaded as a
Module-Federation remote and executes as JavaScript **in the host origin**, and
the host explicitly hands it the viewer's capabilities: `getAccessToken`,
`refreshAccessToken`, and an `authedFetch` that stamps the viewer's bearer. Code
in that position can read anything the viewer's session can read and act as the
viewer against every API the viewer can reach.

It follows that registering a solution is approximately equivalent to deploying
first-party code into this application. Only publish a solution you would be
willing to merge into the host. This is the vetted first-party tier
([ADR-0002](docs/adr/0002-remote-ui-tier-boundary.md), "Tier 2"); untrusted
remote code ("Tier 3") is **not supported** and would need real isolation —
iframe or worker — which the host does not implement.

Three mechanisms are routinely mistaken for a sandbox. None of them is one:

- **Module-Federation singleton sharing** is a *cooperative* deduplication so
  the host and a remote agree on one React and one kit instance. It relies on the
  remote's own build config and constrains nothing a remote chooses to do.
- **CSP origin inclusion** decides where a script may be fetched *from*. Once
  fetched, the script runs in the host origin with full DOM and same-origin
  network access.
- **Row-level security** constrains what the *database* returns for a given
  tenant and role. It cannot constrain in-origin code acting with the current
  user's own credentials, because that code is indistinguishable from the user.

## 2. One owner-bound authority contract

Registration is authorized by a short-lived credential bound to the publisher and
to the single solution id it may act on. The gateway and the frontend accept the
**same** credential and verify it independently; neither holds a secret every
registrant shares, and neither trusts the other's decision.

The handshake mirrors the composed-module one
(`services/auth-gateway/AGENTS.md`, "Composed-module REST federation") and
reuses its issuer rather than adding a second one:

1. `POST /solutions/_registration-token` on the auth-gateway, carrying the
   cluster-internal token in `X-Codefly-Internal-Token` (a perimeter check, not
   the authorization) **and** the solution's own registration secret in
   `X-Codefly-Solution-Secret`, body `{"id": "<solution-id>"}`.
2. The gateway brokers to accounts over the internal listener
   (`ModuleCapabilitiesService/MintSolutionRegistration`, `EXPOSURE_INTERNAL`, so
   the generated mesh policy admits the gateway's service account and denies
   everyone else). accounts compares the presented secret against the digest
   declared for **that id** in the `federation` group's
   `SOLUTION_REGISTRATION_SECRETS`, and on a match mints a 5-minute Ed25519
   token — `aud=solution-registration`, `sub=solution:<id>`, `solution=<id>`,
   with a `jti` — emitting a `saas.solution.registration_minted` audit event.
   Unset means **no solution may register**.

   That declaration is read **on every exchange**, not held from startup, so
   accounts never has to restart to see an edit: authorizing a publisher takes
   effect on its next exchange, and withdrawing one stops its next renewal —
   after which the gateway's 120-second lease runs out and the registration
   stops serving. A declaration that stops parsing denies every solution, with
   the same refusal a wrong secret gets and the reason in the operator's log.
   `MODULE_REGISTRATION_SECRETS` beside it is deliberately not treated this way:
   a module is composed into this host's build, so its declaration cannot change
   without a new deployment anyway.

   **What that does not yet give you, stated plainly because a revocation
   depends on it.** accounts reads the declaration through the Codefly SDK,
   which resolves it from the process environment and the runtime's injected
   carrier. Both are live reads — an edit is observed the moment it reaches
   them — but **no runtime component updates either one under a running
   process** (`codefly-dev/cli#740`). So in a deployment that delivers this key
   as a container environment variable, an edit does not reach accounts at all,
   and withdrawing a publisher still requires restarting the service. Until that
   issue closes, treat a revocation as taking effect on restart, not on edit,
   and do not rely on removing a digest to lock out a publisher whose secret you
   believe is compromised — delete its registration and restart accounts.
3. The solution presents that token in `X-Codefly-Solution-Registration` to both
   `POST /solutions/_register` (gateway, `{id, upstream}`) and
   `POST /api/solutions/register` (frontend, the manifest). `DELETE` on either
   withdraws it.

The exchange keeps three outcomes apart, because a registrant acts differently
on each: `401` means accounts refused the secret — the only answer that means
"check `SOLUTION_REGISTRATION_SECRETS` and the provisioned secret"; `503` with
`Retry-After` means accounts could not be reached or did not answer in time (a
rollout, a restart) and the next attempt may succeed; `502` means accounts
answered with an error that is neither. The frontend likewise answers `503`, not
`401`, when it cannot reach the key set it verifies the token against. A
registrant must not read any 5xx — including a `502` from an ingress or mesh in
front of a restarting gateway — as a provisioning fault.

`SOLUTION_REGISTRATION_SECRETS` is declared **separately** from
`MODULE_REGISTRATION_SECRETS`, and the two credentials carry different
audiences. A module credential federates a REST prefix; a solution credential
additionally publishes host-origin code. Holding one must never confer the
other, so neither the configuration nor the token is shared.

### What each half enforces

| Check | Gateway | Frontend |
| --- | --- | --- |
| Ed25519 signature against the published JWKS, alg-locked to EdDSA | yes | yes |
| Issuer, `solution-registration` audience, expiry (with skew leeway) | yes | yes |
| `solution` claim equals the id in the request | yes (403) | yes (403) |
| First registration binds the id to `sub`; a different `sub` is refused | yes (409) | yes (409) |
| Same publisher may move its own endpoint | yes | yes |
| Delete is owner-bound, and a non-owner's delete is indistinguishable from an unknown id | yes | yes |
| `jti` burned on use, so a captured credential cannot be replayed | yes | yes |
| Request body bounded before parsing | yes (256 KiB; the separate token exchange bounds at 4 KiB) | no bound of its own — the manifest is bounded where the gateway accepts it |
| Id is one catalog-identity segment, so reserved `_…` sub-paths cannot be shadowed | yes | n/a (id comes from the manifest and must equal the claim) |
| Upstream host must be composition-local; resolved address re-checked at dial time | yes | n/a |

The gateway deliberately allows a solution to **move** its upstream, which
module federation does not. That is safe only because the publisher is proven on
every call: an endpoint legitimately changes on a redeploy or a new dev port,
while a credential holder for one solution can neither claim another's id nor
redirect it.

### Upstream constraints and the development exception

A solution upstream receives forwarded user bearers, so it is held to the same
rule as a federated module upstream: loopback and private ranges (the dev and
in-cluster cases), mesh short names and cluster suffixes — and nothing globally
routable, no link-local, no cloud-metadata address. The register-time host string
is only half of it: the **resolved address is re-checked at dial time**, so a
mesh-looking name whose DNS answer later points off-mesh never receives a
forwarded bearer. Loopback is an explicit development exception and is tested as
such.

## 3. Runtime compatibility is enforced, not stored

A declared requirement the host does not check is not a compatibility contract.
Every requirement is checked **before activation**, so an incompatible remote is
never handed to a browser. An undeclared major defaults to `1` — the major in
force when the field was introduced, which is what silence actually asserts — and
never to the host's current major, which would make every silent manifest
compatible by definition at exactly the upgrade this check exists for:

| Manifest field | Checked against | Default when absent |
| --- | --- | --- |
| `schemaVersion` | `SOLUTION_MANIFEST_SCHEMA_MAJOR` | `1` |
| `frontend.hostContract` | `SOLUTION_HOST_CONTRACT_MAJOR` (the `SolutionPageProps` the host injects and the `./Page` default it expects) | `1` |
| `frontend.reactRange` | the host's real React version | unconstrained |
| `frontend.shared` (per package) | the versions the host publishes into the sealed scope | unconstrained |
| `frontend.exposedModule` | must be a `./Name` key | — |

Ranges use a deliberately small semver subset (`||` alternatives of
space-separated `^ ~ >= > <= < =`, a bare version, or `*`). A range outside the
grammar is **refused**, never approximated: a requirement the host cannot
evaluate is not one it can honour. The host values live in
`services/frontend/code/src/solutions/host-runtime.ts`, which the register route
and the Module-Federation host both read, so the numbers cannot diverge.

A refused registration is refused **whole**. If the id already had a valid
registration, that registration keeps serving — the last known valid one is never
replaced by a rejected update. The reasons are returned to the registrant (HTTP
409, `{"error":"incompatible_runtime","reasons":[…]}`) and logged for the
operator. If the id has no valid registration to fall back on, its page renders a
plain "temporarily unavailable" panel instead of a 404, and the technical cause
stays in the server log.

## 4. Registration, installation, and entitlement are three different things

They are frequently conflated. In this module they are not the same, and the
boundary is deliberate:

- **Deployment registration** (this document) is cluster-wide and governs
  **UI and API availability**: whether `/s/{id}` renders a remote and whether
  `/solutions/{id}/…` proxies. It is per-deployment, not per-organization.
- **Per-org installation** (`InstallationService/InstallSolution`) governs
  **agent authority only**. Installing composes an agent principal, a
  `kind='solution'` scope node, a least-privilege standing grant with an
  audience/scope ceiling, an accountable owner of record, and an installation
  row. The solution's declared consumes are materialized into durable
  subscriptions *after* that transaction commits — idempotent and best-effort,
  so a failure is logged and recovered by a reinstall or a runtime `Subscribe`
  rather than failing the install. Uninstalling removes the standing grant and
  revokes the agent principal, which is what withdraws the authority; it does
  **not** reverse the whole composition, because the scope node is retained for
  a reinstall to reuse and the subscriptions are not deleted.
- **Entitlement** (plans and grants, `BillingService`) governs feature
  availability within the product and is independent of both.

**The limitation, stated plainly:** uninstalling a solution in one organization
revokes that organization's agent authority — the principal and its standing
grant — and does **not** remove the solution's nav entry, page, or gateway route,
for that organization or any other. A registered solution's UI and API surface is
visible to every tenant of the deployment; its ability to *act* on a tenant's
behalf as an agent is what installation confers and uninstall withdraws. Other
tenants are unaffected either way, because installation state is per-org.

If a deployment needs per-tenant admission before route or page exposure, that is
an additional gate on top of this model — a tenant check in the solution page and
proxy route — and is not implemented here.

## 5. Rollout

The shared cluster-internal token is no longer accepted for either half of
solution registration; there is no permanent bypass. A deployment upgrading to
this contract must declare `SOLUTION_REGISTRATION_SECRETS` for every solution it
expects to register and provision each plaintext to its solution, exactly as
`MODULE_REGISTRATION_SECRETS` is provisioned today. Until it does, registration
fails closed and no solution is served — which is the intended direction of
failure for a surface that decides what executes in the host origin. Adding an
entry afterwards is enough on this side; what remains is for the deployment's
own configuration carrier to deliver the new value to the running process (see
§2, and `codefly-dev/cli#740`).

Registrations that already exist at upgrade time are carried over, not locked
out. Before this contract the gateway stored a registration's publisher as the
bare solution id (the caller named none); the verified subject is
`solution:<id>`, and a write from a different publisher is refused. Store
migration `130_solution_registrations_verified_publisher` rewrites that
pre-contract default to the verified form, so the solution's own credential
keeps renewing its record after the upgrade. A pre-contract row whose publisher
was self-asserted as anything else was never authenticated; it stays as it is,
and who owns it is an operator's decision — delete the row (or `DELETE` the
registration) and let the credentialed publisher register it afresh.

## 6. The registration carries the solution's runtime boundary

A runtime task is reachable only under the boundary of the Work Context that
admitted it: the context's `task_id`. A solution's page therefore needs a
boundary that outlives one context — when a cached context renews, or a call
with another scope set mints its own, the next request carries a fresh
`task_id` and the run the page admitted is gone from view while it keeps
executing.

So the registration record carries one. `solution_registrations.runtime_boundary`
is an opaque id **the host assigns when the record is created**, and every Work
Context minted for that solution is sealed under it.

**It is assigned, never named.** The column defaults to `gen_random_uuid()` on
insert, the registry's upsert omits it from its update list, and no request
field anywhere reaches it. It is stable across both halves, across a lease
renewal, across a replaced half, and across a tombstone and a reactivation —
the record is the same solution under the same publisher, so the runs it already
admitted stay the ones it can read.

**Why it could not be the solution's to choose.** If a solution named its
boundary, solution B could mint for A's and read, answer and recover A's runs
for the same viewer: exactly the cross-boundary access the registration
contract exists to prevent. Naming it is the one thing a registrant may not do,
which is why this is the host's field and not a registration input.

**Who may learn one.** The solution, its own, through its registration: the
**backend half's** `POST /solutions/_register` response carries
`runtimeBoundary`. That half is authenticated by the solution's own
solution-bound credential and is the half that mints. The frontend half does
not mint and is not told; `GET /solutions/_registry` and every other registry
read carry no boundary at all, because a consumer holding the whole registry
would otherwise hold every solution's.

**How a mint proves which solution is asking.** The solution presents the same
registration credential on
`POST /saas.accounts.v1.WorkContextService/StartTask`. The gateway verifies it
exactly as it does on a registration — alg-locked EdDSA against the published
JWKS, issuer, `solution-registration` audience, expiry — and stamps the id its
`solution` claim names as `X-Codefly-Solution-Id`, a forwarded identity header
accounts believes only beside the gateway credential and strips from every other
caller. On such a mint accounts seals that solution's boundary and **refuses a
caller-supplied `task_id`**; the credential is accepted on no other procedure,
and one that does not verify is a `401` rather than a mint under some other
boundary. A mint presenting no credential is unchanged in every respect.

Two limits worth stating. The boundary is **per solution, not per viewer**: the
person is the capability's owner, and separating two people's runs under one
boundary stays the consuming module's own owner check. And the `jti` is not
burned on a mint (see `services/auth-gateway/AGENTS.md`), so one credential can
seal several mints inside its five-minute life.

Tracked as
[#1015](https://github.com/codefly-dev/module-saas-starter/issues/1015); the
page half — a solution's passthrough presenting its credential and no longer
naming a `task_id` — belongs to `codefly-dev/solution-runtime-go` and is not
done here.
