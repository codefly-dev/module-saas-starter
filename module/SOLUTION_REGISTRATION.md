# Solution registration: trust model, authority, and compatibility

A **solution** is an independently deployed module this host has no build-time
knowledge of. Delivery declares its presence through a signed
`SolutionHostBinding`; the host reconciles that declaration into durable state.
The gateway reads that state for `/solutions/{id}/…`, and the frontend reads it
for `/s/{id}`. This document states who may declare presence, what the host
checks, and how presence differs from a tenant's authority to use a solution.

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

## 2. Declared authority

Only an admitted delivery declaration creates or removes solution presence.
The signed carrier, trust anchor, ownership domains and generation rules are
specified in §7. Runtime credentials confer no authority to change presence.

The internal `SolutionRegistryService` exposes reads. The gateway reads its
snapshot; the frontend obtains the projection through `GET /solutions/_registry`
using the cluster-internal credential. Registry identity and the immutable target
are host facts, never assertions accepted from a browser or remote component.

An upstream receives forwarded viewer credentials. The transport therefore checks
resolved addresses at dial time, refusing public, link-local and metadata
addresses. Composition-local addresses and the explicit loopback development
case remain supported. A changed DNS answer cannot bypass the transport check.

## 3. Runtime compatibility

<<<<<<< HEAD
The Module-Federation host checks compatibility before activating a remote.
The manifest parser validates the snapshot's shape. Runtime requirements include
`schemaVersion`, `frontend.hostContract`, `frontend.reactRange`,
`frontend.shared` and `frontend.exposedModule`; host versions live in
`services/frontend/code/src/solutions/host-runtime.ts`. A failed check leaves the
remote unavailable and reports the cause in the server log.
||||||| parent of a3090094 (fix(auth-gateway): a module or solution upstream receives the person's identity, not their session (SA-F-BEARER))
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
=======
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

A solution upstream is a host the registrant chose that receives requests made on
a person's behalf, so it is held to the same rule as a federated module upstream:
loopback and private ranges (the dev and in-cluster cases), mesh short names and
cluster suffixes — and nothing globally routable, no link-local, no cloud-metadata
address. The register-time host string is only half of it: the **resolved address
is re-checked at dial time**, so a mesh-looking name whose DNS answer later points
off-mesh never receives one. Loopback is an explicit development exception and is
tested as such.

A solution upstream does **not** receive the viewer's session credential
(SP-GW-07). The gateway removes `Authorization` and `Cookie` from every request it
forwards to a runtime-registered upstream, module or solution, and the upstream
receives the identity headers `ext_authz` stamped instead — the subject, the tenant,
the session, the credential kind and the scope ceiling, which name the person
without carrying their authority. A host access token carries one host-wide audience
and the person's full authority, so it is not a credential any single upstream
should hold.

The invariant's other half is **open**: an upstream should receive a host-minted
context bound to its own audience, and today it receives none. An upstream that
needs to act on a person's behalf needs that capability from the Work Context
surface.

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
>>>>>>> a3090094 (fix(auth-gateway): a module or solution upstream receives the person's identity, not their session (SA-F-BEARER))

## 4a. The host cannot yet require an installation at the proxy

The frontend's solution proxy admits a registered solution to any signed-in viewer
whose request is same-origin. It does not require that the viewer's organization has
INSTALLED the solution, so installation governs what an agent may do headlessly but
not whether a person's browser may reach a solution's endpoints through the host.

This is not a judgement that it should not; it is a missing contract.
`InstallationService` offers `InstallSolution`, `UninstallSolution`,
`TransferInstallationOwnership` and `GetInstallation` — and `GetInstallation` takes an
installation UUID. Nothing answers "does this organization have an installation of the
solution with this id", which is the question the proxy has to ask. Adding it is a
descriptor change (contract digests, published clients, a verified regeneration), and
it is the condition the solution/module lifecycle rework owns.

Until it lands, what bounds the surface is: a registered solution only (404
otherwise), a same-origin request against the configured public origin (403
otherwise), the per-viewer projection of the registry, and the gateway's own solution
admission. None of those is an installation check, and this document should not be
read as claiming one.

## 4. Registration, installation, and entitlement are three different things

They are frequently conflated. In this module they are not the same, and the
boundary is deliberate:

- **Deployment registration** (this document) is cluster-wide and governs
  **availability**: whether a solution exists on this deployment at all, and so
  whether `/s/{id}` can render a remote for it. It is per-deployment, not
  per-organization, and since issue #952 it is no longer sufficient to reach a
  solution: `/solutions/{id}/…` additionally requires the caller's organization
  to have installed it and a grant to reach the caller.
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

**What uninstalling does, stated plainly:** it revokes that organization's agent
authority — the principal and its standing grant — withdraws that organization's
viewers from the projections, and refuses that organization's traffic at the
solution proxy. It does **not** remove the solution's page for that organization,
because `/s/{id}` server-renders from the registry and this origin cannot read a
verified viewer server-side without rotating a refresh token per render; the page
discloses a nav title and a manifest URL the public asset surface already serves,
and every call it makes goes through the gated proxy. Other
tenants are unaffected either way, because installation state is per-org.

### What the projections answer (issue #949)

Registration stays deployment-wide, but the **projections** answer per organization
and per viewer. `GET /api/solutions` (the navigation menu) and
`GET /api/solutions/surfaces?client=<kind>` (the per-client surface listing) return
only the solutions the caller's organization has installed **and** the caller's
teams were granted. They used to answer the same registered set to everyone, so a
menu was a function of what was *deployed* rather than of what the viewer may
*use*.

Both are therefore **authenticated**, and answer `401` to an unauthenticated
caller rather than an empty list — `[]` would be indistinguishable from "your
organization installed nothing". The verified organization and viewer come from one
place only: the gateway's `ext_authz`, read through
`GET /solutions/_entitlements`. Nothing in a route handler may derive them —
`lib/auth-session.ts` decodes an access token without verifying it, so an
organization taken from there is one the caller chose.

`GET /api/solutions/<id>/installation` is the third projection, and the one
a solution PAGE consumes. It answers the verified viewer organization's active
installation of the routed solution (`{installationId, healthy}`), through the same
`entitledSolutions` join as the menu, and `404` for a solution that is not
registered or not entitled to the viewer alike. The solution outlet reads it with
the viewer's session and hands the id to the page as `SolutionBinding.installationId`.
A page never names an installation itself: one it chose could be another
organization's. The gateway's `/solutions/_entitlements` entries carry
`installationId` for this; the authority already returned it and the gateway used
to drop it.

The authority answer is `SolutionEntitlementService.ListSolutionEntitlements`,
which reads the installation set and the scope grant + share union in **one**
transaction, so the two can never describe different moments. The join key is the
immutable solution target: `installations.target_id` is the target a
registration record was **declared** under, which the registry projection carries
as `targetId`.

It used to be the route alias — `installations.solution_identifier` against the
registered manifest `id` — and that was a transfer of consent waiting to happen.
A route alias is deliberately **reusable**: a tombstoned one may be claimed by
another binding. So an organization that installed the binding behind `reports`,
and exposed it to a team, had that installation and that team grant follow the
alias to whatever claimed it next, with no administrator having acted. A rename
is the same defect from the other side: the installation kept the old text, the
organization silently lost access, and the next claimant of the old alias
inherited the installation.

A **solution target** is one continuous period of one binding's presence on this
host (`solution_targets`, migration 21). It is opened when a present generation
applies for a binding with no live target, closed by that binding's tombstone
generation, and **never reused** — so a replacement is a different identity and
inherits nothing. The alias lives on the target and moves with a generation that
renames it, which is precisely why it is not the identity.

Three consequences worth stating, because each is a behaviour rather than a
restatement:

- **Closing a target revokes every active installation of it, in the same
  transaction as the tombstone**, and records `installations.revoked_reason` so a
  withdrawal is distinguishable from an administrator's uninstall.
- **Presence nothing declared is admissible to nobody.** A registration record
  with no declaration carries no target, so no installation can name it and the
  proxy refuses it as a verdict (`403`), not an outage.
- **An installation names a target from `ListAvailableSolutions`**, which serves
  accepted *applied* state — never the diagnostic binding listing, which also
  reports desired generations that were refused. Consenting to a release this
  host never admitted would record authority over a presence that does not exist.

Four distinctions the projections keep:

- An installed, granted solution whose installation is **unhealthy** stays listed,
  marked unavailable. The organization installed it and the viewer was granted it,
  so hiding it would send someone looking for a grant that already exists; what it
  must not do is route as though it were serving.
- A **deployed but uninstalled** solution is invisible to viewers.
- A newly installed solution reaches **no team** until a grant is written.
  Installing writes a standing grant for the *agent principal* only. A grant at an
  ancestor node (the organization root) does reach a solution node, because that is
  the scope tree's own hierarchical rule and the same union `CheckAccess` resolves
  — narrowing tighter here would hide what an authority check permits.
- An unreadable registry is still `503`, and a missing or malformed `client` still
  `400`. Neither ever renders as an empty list.

Backend authorization is unchanged and remains the real boundary: a client holding
a stale menu still has every call denied by the authority it calls.

### What the proxy admits (issue #952)

#949 narrowed the projections and deliberately left route and page exposure
deployment-wide. That was a hole rather than a boundary: a viewer in **any**
organization could call the data endpoints of **every** solution the deployment
ran — with a real bearer forwarded to them — by typing the path the menu declined
to show. Available, installed and exposed are three layers and none is inferred
from another.

So **the solution proxy admits traffic through the same authority the
projections read.** `/solutions/{id}/…` consults
`SolutionEntitlementService.ListSolutionEntitlements` for the identity
`ext_authz` verified, and forwards nothing until that answer admits the
solution the request addressed. Three answers, kept apart:

| answer | response |
| --- | --- |
| installed and granted | proxied, as before |
| the authority answered and this organization has no installation of it, or no grant reaches this viewer | `403`, `X-Codefly-Entitlement-Refusal: not-entitled` |
| the authority could not be asked (outage, no client wired, a cursor that never terminates) | `503` — never `403`, and nothing forwarded |

Four properties worth stating, because each is a decision:

- **The refusal is named.** A `403` meaning "nobody granted your organization
  this" is acted on by installing and granting; a `403` from `ext_authz` by
  signing in again. The header is what separates them, and it is the same header
  the entitlement listing uses for the same reason.
- **An outage is not a verdict.** "I cannot ask" must neither route nor read as a
  missing grant, so it is a `503`. A gateway with no authority client wired fails
  closed for the same reason: the behaviour this check replaces was *serve every
  solution to every organization*, and that must never be the fallback.
- **Health is not admission.** An unhealthy installation is still installed and
  still granted; the registry's own `resolve` already answers `503` for a
  registration that is not serving. Reading health here would make "nobody
  granted you this" indistinguishable from "it is restarting".
- **Impersonation is decided on the impersonated viewer**, the effective subject
  accounts authorizes against — not the administrator acting. Otherwise
  impersonation would show more than the user can reach, which is the one thing
  it exists to check.

**The public Module-Federation surface is not gated, and cannot be.**
`/solutions/{id}/assets/*` and `/.well-known/*` are fetched by the browser's
module loader with no credential, so there is no viewer to ask about; what they
serve is the solution's own static bytes, which carry no tenant data. That is a
deliberate residual exposure, not a pending gate: gating it would make a remote
impossible to load same-origin.

**Backend authorization remains the real boundary** either way: a client holding
a stale menu, or reaching a solution through some path not listed here, still has
every call denied by the authority it calls.

## 5. Database cutover

Migration `23_delete_runtime_registration` requires declared target ownership on
all registry rows. It discards undeclared rows and previously reported runtime
observations. Delivery must supply the declared state that the host can reconcile;
no row is implicitly adopted into a new target. The migration is destructive and
cannot recover discarded data. Its down migration fails with that explanation.

## 6. Per-viewer access

An organization administrator installs the immutable target through
`InstallationService/InstallSolution`, then grants `solution:use` at the
installation's authority-root scope node to the intended people or teams.
Deployment presence supplies neither consent nor a scope grant. No migration
creates those grants implicitly. See §4 for projections and traffic admission.

## 7. Declared presence

A `SolutionHostBinding` records which solution delivery intends to run on this
host. The host records desired and applied generations separately, including the
reason an unaccepted generation remains pending.

### The document, and who owns which field

`SolutionHostBinding` is defined by Core (`github.com/codefly-dev/core/solutionhost`,
schema `codefly/solution-host-binding/v1`), rendered per solution instance by
`codefly-dev/cli`, delivered by the GitOps bundle, and reconciled here. It is
desired state and nothing else: it carries no observation, no health and no
credential. The split is the point.

| Fact | Owner | Where it lives |
| --- | --- | --- |
| That a solution should be present at all | the declaration | `solution_host_bindings.applied_*` |
| Which release runs | the declaration | `release`, recorded as `applied_release` |
| Which route alias it answers on | the declaration | `routes[].alias` |
| The workload identity to expect | the declaration | `workload` |
| Resolved upstream and frontend | host reconciliation | `backend_upstream`, `frontend_manifest` |

The document deliberately carries no upstream address and no manifest. An
address written into a delivery document is a resolution result that was true on
one cluster until something moved; a manifest is what the running build actually
published. Both are observations that the host resolves under declared authority.

### The registry key is the route alias

A binding ID identifies one **deployment instance** — the renderer's is
`<workspace>.<environment>.<instance>`, dotted — and a second instance of the same
solution inherits nothing from the first, its alias included. It is therefore not
the registry key: the key is the binding's route alias, because the alias is what
this host routes on (`/solutions/<alias>/*`, and the key a remote loads under).

This host needs **exactly one** route alias on a present generation, and it must
be a single lowercase path segment. Core's alias vocabulary is wider — it admits
dots and slashes — so a binding that declares none, several, or a dotted one is
refused with a reason naming the route rather than having one picked for it. The
renderer commits to exactly one single-segment lowercase alias per binding, so
this refusal is a guard, not a workflow.

A tombstone declares no route, which is why a binding's row keeps the key it last
applied: it is the only way a removal knows what to withdraw.

### Prepare, then activate

Every pass reads the durable inbox, then asks Core about the **whole** desired set —
route-alias uniqueness and "declared once per set" are properties of the set, not
of a document. Only then is anything written, and desired state is recorded for
every delivered binding before any generation is applied, so what delivery wants
and the reason it was refused are visible even when nothing passed.

A refused document leaves the running generation untouched. The refusal is
attributed to its binding rather than failing the pass, because the common
refusal is a stale render: the renderer derives a generation from the previously
delivered document, so a render from a checkout that cannot see the prior tree
emits generation 1 and is correctly refused as stale. If that held back the whole
set, one bad pipeline run would freeze every solution on the host. A refusal Core
raises that this host cannot attribute to a document withholds the **whole** set —
failing closed on a rule it does not recognise, rather than applying a subset Core
never approved.

Two replicas converge without applying a generation twice: each apply re-decides
under the binding row's lock, and `DecisionCurrent` — this generation is already
applied — writes nothing. A restart resumes from the recorded applied generation
rather than deriving it again.

An unreadable inbox is an **error**, never an empty desired set, and an empty
one removes nothing. Removal is a tombstone generation precisely so that a store
this host cannot read can never be reconciled as "withdraw every solution".
Nothing deletes from the inbox either, which is what keeps "delivery stopped
talking" and "delivery said remove it" different facts.

### Reading the three states apart

`SolutionRegistryService/ListSolutionHostBindings` (`EXPOSURE_INTERNAL`) answers
with desired, applied and observed state separately. That is the question the
registry snapshot alone cannot answer: a binding whose first generation was
refused has no registration record at all, so it is invisible there, and
"declared but never observed" and "never declared" look identical.

### The ownership domain, and why the host persists it

A document declares the **ownership domain** it speaks for: the slice of this
host's binding space that delivery may add to, change and remove within. It is
what lets a module-scoped render express a removal without "remove everything
else" being expressible at all.

Core requires the host to declare the domains it accepts whenever it knows its
own coordinate, and refuses a document from an unstated one — **first generation
included**. That is not belt-and-braces: the applied record cannot bound a
binding's *first* generation, because there is nothing yet to compare against, so
without the declaration any accepted delivery could claim an unseen binding ID
under a domain of its own choosing and own it from then on.

The host therefore **persists the domain each generation applied under**
(`solution_host_bindings.applied_domain`) and hands it back to Core on every
pass. Core refuses a later generation for a binding that arrives under a
different domain, which is what stops one delivery taking over a binding another
delivery owns. Two consequences worth stating, because neither is obvious:

- **It is not re-derivable from configuration.** The host's configured list
  answers "a domain this host accepts", not "the domain this binding was claimed
  under", and those differ exactly when it matters — a host accepting two.
- **An applied record without one is unusable, and unusable means the whole
  set.** Core refuses an applied state with no domain, and a pass judges the
  desired set as a whole, so a single domainless record does not degrade one
  binding: the reconciler applies its first generation and then refuses
  everything on every pass afterwards. The applied group's whole-or-absent CHECK
  and a non-empty constraint keep that state out of the table.

The core-owned half of the applied record is built by `solutionhost.AppliedFrom`
rather than assembled here, so the next field Core makes required is a loud
refusal at the apply instead of a silent omission that surfaces as a frozen
reconciler.

### Configuration

Every key is in the `federation` group.

| Key | Meaning |
| --- | --- |
| `SOLUTION_HOST_COORDINATE` | the coordinate this host answers for, exactly as the operator declared it on the environment the renderer read. Never derived here — an invented coordinate matches nothing delivery wrote, so every document would be refused. It is also the one declaration that turns the surface **on**: empty means no reconciler and no delivery endpoint, and no new declared presence can be reconciled. |
| `SOLUTION_HOST_OWNERSHIP_DOMAINS` | the ownership domains this host accepts delivery from. Required with the coordinate and refusing to boot when empty, for the same reason the coordinate is: it is the only thing bounding a binding's first generation. |
| *(no setting)* | WHERE the trust root and verification policy are read from is **not configurable**: it is a constant, `infra.SolutionHostTrustAnchorPath`. There is no environment value, no flag and no overridable default. Workspace environment is delivered by the **composition**, so an overridable path would let a composition point this verifier at a policy and root it wrote itself, sign its own presence documents, and pass every downstream check — against an anchor it chose. A deployment needing a different path changes the pod spec that mounts it, which the platform renders and the composition cannot supply. That document also carries the signer-to-domain mapping, which **replaced** an env var: a bare certificate SAN as the key is accepted from any issuer and the same workflow path exists in every fork, and the env var let whoever set it widen what an accepted signer speaks for without touching the independently-delivered policy. A signer granted a domain `SOLUTION_HOST_OWNERSHIP_DOMAINS` does not accept is refused **by name**. |
| `SOLUTION_HOST_TRUST_POLICY` | how a delivered carrier's bundle is checked. `keyless` is the only value. Required with the coordinate, with **no default** — every default is wrong somewhere. The trust anchor's **absence** is checked first and refuses the boot naming the anchor, so an unmounted anchor is never reported as a missing setting. |
| `SOLUTION_HOST_BINDING_INTERVAL` | optional; how often the inbox is re-read. Empty uses 30s. |

Declaring the coordinate without the ownership domains refuses to boot: this
host would be unable to refuse a document claiming a binding it has never seen.
So does declaring it with no trust anchor mounted at
`infra.SolutionHostTrustAnchorPath`, and so does declaring it where no
TokenReview can be performed — a delivered carrier is authorised by TokenReview,
so a host that cannot reach its API server has no input at all and starting
would leave it polling an inbox nothing can write to.

**There is no mount setting.** It was `SOLUTION_HOST_BINDINGS_DIR`, a projected
ConfigMap volume, and both it and the directory reader are deleted. A mount is
the CURRENT desired set and nothing more, so a document that arrived, was
recorded as desired, failed to apply and then disappeared from the mount was
never retried — the host had recorded that delivery wanted something and had no
way to want it again — and the mount dropped the carrier, so a restore could not
re-verify what it had accepted. While the setting existed the durable inbox was
dead in every configuration: unset meant no reconciler at all, and set meant the
source was the directory, so the table the delivery endpoint writes was read by
nothing.

### The authority half, and the one shape in which it is active

Delivery has two kinds and the host serves both on their own paths:
`POST /platform/_delivery/presence` and `POST /platform/_delivery/authority`.
Each is verified with the reader for **its** kind, and the kind in the path is
checked against the schema inside the attested bytes — which is a check rather
than a convention, because the schema is part of the canonical encoding the
signature covers, so a document's type is attested rather than asserted by the
carrier. A document sent to the other kind's path is **400**: the bytes are
wrong, not the signer, and a 403 would send an operator to look at a signing
identity that signed the document perfectly well.

An authority row is keyed by its **own** authority id, never by the presence
binding it is granted over. One binding may be granted authority under successive
authority ids, and the withdrawal fold is keyed on the binding precisely so that
re-signing under a new id cannot reinstate a withdrawal — which only works if the
inbox keeps the id each document was delivered as.

**Neither half activates alone.** Authority is active only as a matched
(authority, presence, build) tuple, which is what makes "approved for one exact
build" a property of the system rather than of a field: an authority document for
a build the host is not running activates nothing, and a presence document with
no authority behind it activates nothing. The build is asked **about** rather
than read out of either document — reading it out of the document that approves
it would make the question answer itself.

The **ceiling** is Core's `Envelope`: who may hold which binding, and which
builds are approved, at one revision. It is read from the same platform-owned
trust anchor as the trust root (`authority_envelope.json`), and never out of a
delivered document — "an envelope a document carried would be a document
declaring its own ceiling", and containment is tested against the envelope's own
grants, so an envelope assembled from the delivery tree answers itself. Absence
and damage are different answers: no envelope delivered is a complete deployment
that answers no authority question, while an envelope that is delivered and
cannot be read refuses the boot, because reading a damaged ceiling as "no
ceiling" turns a corrupted file into a silently narrower system and reading it
permissively turns it into a wider one.

**This host applies authority to nothing**, and that is stated rather than
defaulted. Core requires either the applied authority record or an explicit
marker that none exists, because "nothing applied" is the most permissive input
the call takes; this host holds no such record, so the marker is the truth. The
replay protection the fold would otherwise give comes from the inbox instead —
the desired set is the **newest** generation per document, so a genuinely signed
older generation re-delivered never becomes the answer. The one part the inbox
cannot do by itself is refuse an authority withdrawn under one id and re-signed
under another, so the host reads **every** delivered authority document over the
binding and refuses when a tombstone is among them. Nothing deletes from the
inbox, so a withdrawal stays visible forever.

Two live authority documents over one binding are refused by name rather than
resolved: whichever one a host picked would be a choice nobody reviewed.

### What the host admits, and why it is now a signed carrier

Core `cd443989` removed every path from unattested bytes to a host judgement.
`Host.Admit` takes `*solutionhost.Delivered`, whose fields are unexported and
whose only constructor is `VerifyDelivered(carrier, BundleVerifier)`. So "this
document was attested" is held by the compiler rather than by a naming
convention, and a host that forgot to verify cannot compile rather than
discovering it in production.

Core `67ee7220` closed the gap that left: a `Delivered` used to hand out the
parsed document it would itself admit, so a caller that adjusted a field changed
what was admitted while the attestation still covered the original bytes. It now
stores the attested payload and re-derives per read.

The host side of that:

- **The mount carries signed carriers**, `{schema, document, bundle}`, not bare
  documents. A bare document is refused — it is not a carrier — and that refusal
  is pinned by a test.
- **The signing input is the canonical JSON** (`CanonicalBytes()`), not the YAML
  `Marshal` writes for a delivery repository. Core refuses a payload that is not
  the canonical encoding of the document it decodes to *even when the attestation
  over those bytes is genuine*, because a signer and a host that disagree about
  which bytes represent the document disagree about what was approved.
- **The verifier returns a signer identity, not a yes.** The host maps that
  identity to the domains it may speak for, and Core refuses a document whose
  asserted domain its attested signer may not. This closes the hole that a
  document states its own ownership domain: without the policy, any signer the
  host accepted at all could deliver under any domain it accepted and take over
  bindings in it.
- **A bundle must be present and be a JSON object.** Core looks inside no
  bundle, but "there is one" is checkable, and absent, empty or `null` is
  refused as unsigned on the verify path — not only on the parse path, as it
  was before `67ee7220`. A delivery that writes the field as `null` is refused
  rather than admitted as a document nobody signed.
- **What the host stores is derived from the attested bytes**, every time, and
  never from a document the reconcile pass has been holding. The generation in a
  row and the digest beside it are therefore over the bytes the signature
  covered, which is the property that makes the digest worth storing at all. A
  carrier whose attested payload will not re-read is refused like any other,
  rather than admitted from a value read earlier.

#### The one trust policy

| Policy | What it checks | Where it is usable |
| --- | --- | --- |
| `keyless` | a Sigstore bundle against a trust root and an identity allowlist — repository, workflow path, ref pattern, issuer. **Never a key.** | everywhere, including a laptop. **The trust root is not yet pushed to the config plane, so it refuses at boot**, see below. |

There is no second row. `local` was deleted — §8 argues why at length — and this
table listed it until the code had already stopped accepting it. A configuration
table is the one place a reader goes to find out what a value may be, so a
deleted value left in it is not a stale sentence but an instruction to write a
host that refuses to boot.

**`keyless` refuses at boot, and that is deliberate.** Keyless verification needs
a trust root and an identity allowlist provisioned **independently of both
delivery writers** — a deployer who can edit the allowlist does not need to forge
a signature, because it can replace the verifier. Nothing on this deployment
provisions either yet. A verifier written against them could not be exercised
against a single real bundle before shipping, in the one service that owns
identity, so it is not written: the refusal names what is missing. It refuses at
**boot rather than per document**, because "this host cannot perform its
configured policy" and "delivery is shipping something bad" are different facts
and only one of them is the operator's to fix.

A refusal recorded against a binding is deliberately **coarse** — not a carrier,
a bundle that did not verify, and a verified payload Core will not parse all read
the same in the row. Which of the three it is tells an attacker which half of the
door it got past; the full reason goes to the log.

The host reads a **directory of files** and knows nothing about how they got
there: a projected ConfigMap volume in a deployment, a plain directory under
`codefly run` locally. Kubernetes' atomic writer is honoured — every dot-prefixed
path element (`..data`, `..2026_…`) is skipped, so one document is never read
twice under two names, which Core would refuse as a binding declared twice.

## 7b. The host's own control paths, and which part a consumer may hardcode

Three repositories independently invented the string `/platform/_credential`
from a design note, and nothing failed at build time. This section exists so the
next one reads a contract instead of guessing, and so the hardcoding question has
a stated answer rather than a convention nobody wrote down.

### The rule: a path is a contract, an origin is a resolution result

"Never hardcode what the system resolves" governs **resolution results** —
addresses, ports, credentials, injected environment: things that are true on one
cluster until something moves. A URL **path** on a published API is not one of
those. It is a contract, like an RPC method name or `/v1/users`, and making it
discoverable would only move the hardcode to the discovery URL.

So a consumer:

- **resolves the origin.** The gateway address comes from whatever already hands
  the workload its environment — Codefly service discovery for a composed
  module, the injected configuration for an independently deployed runtime. A
  runtime that can already call the host has it.
- **may hardcode the path**, because it is published here and pinned by test.
  What it must not do is *invent* one.

`GET /solutions/_registry` and `GET /solutions/_entitlements` are gateway code constants published in this
document, and every registrant hardcodes those paths today.

### `/platform/*` is the host's own control namespace, and it is reserved

| Path | Status | What it is |
| --- | --- | --- |
| `POST /platform/_credential` | **Settled, NOT YET LIVE** | the module/solution credential mint. On the **gateway**, which forwards `Authorization` verbatim; accounts performs the `TokenReview` on the pod's own token. |
| `POST /platform/_delivery/presence` | **LIVE** | a signed presence document, `{schema, document, bundle}` |
| `POST /platform/_delivery/authority` | **LIVE** | a signed authority document, same carrier |

**The two delivery paths are now served by accounts**, which performs the
`TokenReview` on the carrier's own token. The credential mint remains **NOT YET
LIVE** and is brokered by the gateway when it lands; a consumer calling it today
gets a 404 on every deployment, not just one, which is the one mercy in the
situation. Keep an override until this table says live for it, and do not treat
a 404 as "my path is wrong".

Delivery answers on this taxonomy, and it is organised by **what a retry would
change** rather than by severity:

| code | meaning | retry? |
| --- | --- | --- |
| `202` | verified, new, and now durable desired state — applied or not | no, it worked |
| `200` | an exact replay of a `(document, generation, content hash)` already held | no, it worked |
| `400` | the carrier does not parse, or the payload is not the canonical encoding of the document it decodes to | terminal |
| `401` | the carrier's token was REVIEWED and refused | terminal — the token is kubelet-projected and re-read per request, so a refusal is a configuration fault and a retry asks the same question |
| `403` | authenticated, but not the authorised writer for this kind; or no accepted signer; or an accepted signer not granted the asserted ownership domain | terminal |
| `409` | this generation is already held with DIFFERENT bytes | terminal — publish never rewrites a delivered generation, so this is a hand-edited delivery tree |
| `422` | the document parses and `Validate` refuses it | terminal |
| `503` | the host could not REACH the API server or its trust root | **retryable** — nothing was verified, so nothing may be concluded |

**The receipt's field names are part of the contract**, and they are
`camelCase`:

```json
{
  "disposition": "accepted",
  "documentId":  "acme.test.example-solution",
  "generation":  3,
  "contentHash": "sha256:…",
  "signer":      "<the ATTESTED signer identity, never the caller>"
}
```

`disposition` is `accepted` on a `202` and `replayed` on a `200` — the same
distinction the status codes carry, repeated in the body because a pipeline that
reads only the body should not have to infer it. A refusal carries `error`
instead. `signer` is the identity the BUNDLE attested, not the service account
that handed the carrier over: those are different parties, and conflating them in
a receipt would make an authorised carrier look like the author of whatever it
delivered.

These names are pinned by
`TestNewGenerationIs202AndAnExactReplayIs200` in `pkg/adapters`, which declares
them independently of the handler's own struct — a cross-repo contract that only
one side spells out is a contract that side can rename silently. (The first draft
of that test guessed `snake_case` and failed, which is how the names came to be
written down here at all.)

The `401`/`503` split is the one that matters and the one an earlier draft got
wrong by grouping on HTTP class. "The review ran and refused" and "the review
could not run" are different facts with opposite retry answers, and they come
from different branches of the same call. A `404` stays retryable too: a
reachable host without the route is a version behind the Job, which is ordinary
during a roll.

`/platform/` is **reserved for the host** and no delivered binding may claim it.
Route aliases live under `/solutions/<alias>/` and composed module routes under
`/v1/<as>/`, so nothing can shadow it today; it is reserved explicitly so that
stays true. An unknown `/platform/*` path must answer **404**, never fall through
to another handler — the same trap the adversarial review names for the
`/solutions/_…` segments, where deleting a dispatch case lets a request reach
generic solution routing and answer `502`.

### The delivery carrier contract, settled across three repositories

Published here because three repositories implement against it — the renderer
that signs and posts, the infrastructure that provisions the identities, and this
host that verifies — and because every item below was something at least one of
them had wrong at some point.

**The carrier is three keys**, exactly the renderer's `MarshalSigned` output:
`{"schema":"codefly/solution-host-signed/v1","document":<canonical JSON>,"bundle":<bundle>}`.
Parse it with core's `ParseSigned`, which requires `schema` — not by unmarshalling
two of the three, which is how a hand-built carrier claiming any schema used to
reach the bundle verifier before core's `Signed.validate()` closed it.

**Carrier authorisation is the SA and the namespace, and the two halves differ:**

| half | ServiceAccount | namespace |
| --- | --- | --- |
| authority | `delivery` | `platform-authority` — a fixed pair |
| presence | `delivery` | **the namespace the document's own workloads declare** |

The presence namespace is deliberately not a constant. A presence Job runs one
per module tree in that module's own namespace, so a fixed value would refuse
every genuine carrier. It is read out of each workload's `identity.spiffe_id`
(`spiffe://<trust domain>/ns/<namespace>/sa/<account>`), which makes it a property
of the **signed document** rather than of host configuration a deployer can edit
— the same reason the ownership domain lives inside the canonical bytes. A
presence document whose workloads declare two namespaces is refused by name: the
renderer uses one namespace per render and cannot emit such a document, so one
that exists was hand-built.

A presence generation that declares **no** workload declares no namespace, and
there are two of those: a tombstone, which declares absence and carries nothing,
and a present generation that renders only a frontend surface. Such a carrier is
authorised against the namespace this host **last applied** for that binding —
its own recorded state, from a generation it already verified and admitted,
rather than anything the arriving document asserts. Refusing the empty case
outright made **removal through this endpoint impossible**: removal is a
generation, so the one document delivery must be able to hand over to withdraw a
binding was the one document the check could never authorise. It is still refused
when neither answers — a first generation for a binding nothing has applied,
declaring no workload — because then there is nothing to authorise it against at
all.

Authority is the **only** half with a hardcoded pair, which is why it is written
down rather than derived: get it wrong and the `TokenReview` *succeeds*, the
identity is *genuine*, and the host refuses the real carrier for a name — which
reads as an attack rather than a typo.

**The projected token's audience is `accounts`, exactly that literal.** It is
published here because the renderer renders whatever this document publishes, and
an audience the two sides guess at separately is a `TokenReview` that succeeds for
a token minted for something else. It is the same audience the credential mint
reviews against, deliberately: accounts is the service performing the review in
both cases, and a second audience would be another projected token to mis-mount.

It is a constant in this host rather than a setting, and that is the point. A
configurable audience is another composition-supplied input to a trust decision —
whoever could set it could have a token minted for a service of their choosing
reviewed here as if it had been minted for this one.

**The perimeter is not this check.** A manifest claiming a namespace other than
the one its delivery tree declares is refused by the delivery controller's own
destination list, at apply. The host's SA-and-namespace check is the second
layer. Both are worth having; neither is described as the only one.

**Verification is OFFLINE, and there is no egress to a transparency log.** The
delivered namespaces are default-deny egress, so an online Rekor or Fulcio lookup
would **hang rather than fail fast** — the worst failure shape available, because
it surfaces as a staging timeout instead of a permission error in a test. A
bundle verifies offline because it carries its own evidence: the renderer signs
with `sigstore-go` and the bundle carries **both** the signed entry timestamp
(`inclusionPromise`) and the inclusion proof with its signed checkpoint
(`inclusionProof`), media type
`application/vnd.dev.sigstore.bundle.v0.3+json`. The host therefore verifies the
SET and the inclusion proof against the Rekor key in a **mirrored trusted root**
delivered from the config plane, with the transparency-log and observer-timestamp
thresholds set and online verification deliberately off.

The trusted root must carry the public-good Fulcio chain, the Rekor key and the
CT log keys, because the signer is the public-good instance with no TSA — so the
observer timestamp is the SET's integrated time, and a Rekor v2 log would
additionally need its base URL on the log entry in that root.

**A bundle with no transparency evidence is refused BY NAME**, distinct from a
signature failure. The two have opposite causes — a signer configured without
transparency logging is a misconfiguration to fix, and a bad signature is an
attack to investigate — so collapsing them makes the first read as the second.
Both this host and the renderer refuse it by name, so a refusal means the same
thing wherever it is seen.

**Check it BEFORE the verifier runs, on the decoded bundle, not by classifying
the verifier's error.** This ordering is the whole of it, and the obvious
implementation is the wrong one: the verifier reports a bundle with no log entry
as *"not enough verified log entries from transparency log: 0 < 1"* — **the same
words** it reports for a bundle whose log key the trusted root does not hold.
One is a signer that never logged; the other is a root that cannot check the log
it did. A classifier reading that string cannot tell them apart, so the test is
`HasInclusionPromise() || HasInclusionProof()` on the decoded bundle, ahead of
verification.

**An RFC 3161 signed timestamp does not stand in for transparency evidence.** It
attests *when* something was signed, not *that it was logged*, and a bundle
carrying one still has nothing a mirrored root can check an inclusion against.
The refusal must hold with a timestamp present, which is the case a "has it got
any evidence at all?" check quietly admits.

**The authenticating container is derived, not declared.** The renderer names the
container matching the service, or the sole container, and refuses at render a
multi-container workload where neither holds — rather than guessing.
`non_authenticating` is always a non-nil list of every other container, init
containers included, so "there are none" and "nobody said" stay distinguishable.

What neither the renderer nor this host can check is that the projected
`accounts`-audience token is mounted by **exactly** the authenticating container:
a pod-bound token is the pod's. That is closed by the deployment's
execution-admission policy, not by a document field, and until it is in place this
host's container designation is a **record** rather than attestation — stated that
way deliberately, because "the host knows which container may authenticate" would
be a claim the mechanism does not yet support.

### Why the mint is brokered by the gateway rather than called on accounts

A solution runtime is an independently deployed workload, not a composed module
on the internal tier, so letting it reach accounts directly means widening
accounts' internal listener to arbitrary workloads — worse than one more
brokered route on an edge every solution already reaches.

Brokering stays safe because the carrier check is a `TokenReview` of the **pod's
own** token: the gateway forwards `Authorization` verbatim, never substitutes its
own identity, and is a transport hop rather than an audience. A pod token for
audience `accounts` only ever authenticates that pod. The gateway→accounts
brokering pattern is the same one `GET /solutions/_entitlements` already uses.

## 8. Local runs: one attestation path, not a local mode

Binding a credential to an approved execution means reading what the workload
actually runs: `TokenReview` on its projected service-account token, then the pod
by the name that review returns, then the application container's `imageID`. That
is Kubernetes. `codefly run` on a laptop has none of it, and the adversarial
review is right that the delivery API does not preserve the old laptop behaviour —
a host that reads pods is not a Kubernetes-free host any more.

Two paths were allowed: an equally authoritative local attestation the host
verifies under a `local` trust policy, or Kubernetes locally. **This host takes
Kubernetes locally, and ships no local attestation path at all** — and as of the
keyless verifier landing, that is now true of the CODE and not only of this
paragraph. The `local` trust policy is deleted.

It is worth saying why it existed and why its deletion is a fix rather than a
tightening. `local` performed no cryptography and attested every carrier as the
identity `local`, gated on the host's own coordinate beginning `local/`. Both
adversarial reviews named the same concrete failure: the gate was a string
comparison against an **operator-declared** value, so a deployed host configured
with a local coordinate admitted any file any mount writer dropped — and because
the coordinate is self-asserted on both sides, the misconfiguration was not
self-defeating. The rest of this section had already argued at length
that there should be no local trust *mechanism*; §7 shipped one anyway, because
keyless was unavailable and a laptop had to run. Keyless exists now, so the argument and the code agree:
one verifier, one policy shape, two sets of listed identities.

### Why the local path is not built, rather than built carefully

A `local` trust policy can be made *argably* safe. Key it to a reserved coordinate
namespace, refuse it whenever the host's own coordinate is not in that namespace,
and the attack needs a deployed host configured with a local coordinate — which
would also make it refuse every real presence document and serve nothing. The
misconfiguration is self-defeating.

That argument is sound and it is still the wrong thing to rely on, for three
reasons that compound:

1. **It is a property of configuration, not of the artifact.** "A local path must
   not be usable against a deployed host" is a promise a check keeps, and a check
   can be wrong. With no local path, the promise is kept by absence. Under the
   maximum-security rule the structural answer wins and the resulting prerequisite
   is a tool to build, never a reason to soften.
2. **It is a second implementation of a trust decision**, which is the mistake the
   Work Context single-implementation rule exists to stop, in a different place. A
   wire contract has one implementation in the repo that owns the type; an
   attestation contract is no different, and the second copy is what drifts.
3. **It would put signing keys back on laptops** at exactly the moment delivery
   went keyless to eliminate key custody. Whatever signs a local attestation is
   material a host is configured to trust, held on every developer's machine. That
   is a new custody problem adopted to avoid a dependency.

There is also a gain, not only a cost. Under a local attestation the laptop
exercises a path production never runs, so what a developer verifies is a
simulation of the mechanism. Under Kubernetes locally, `codefly run` drives the
real `TokenReview`, the real pod read, the real `imageID` comparison and the real
incarnation record. The local run becomes a test of the thing that ships.

The cost is plain and is not hidden here: every developer needs a local cluster,
and `codefly run` has to provision or attach one and run each service as a Pod
with a projected service-account token for audience `accounts`. That is work for
the tooling, which is where the rule puts it.

### The delivery signature has the same gap, and it closes without a local mode

The options as posed cover the mint. They do not cover the other half: delivery
documents are signed **keyless, over the CI provider's OIDC identity**, and a
laptop has no such identity either. A local run therefore cannot produce a
delivery signature any more than it can produce a pod.

It does not need a second mechanism. Keyless signing is not limited to a CI
workflow identity — a person has an OIDC identity too. So a local run signs with
the **developer's own identity**, through the same Sigstore flow, verified by the
same verifier against the same kind of policy. **What differs between local and
deployed is the identity allowlist, which is data, not code.**

That is what makes the boundary structural rather than promised. A deployed host's
allowlist names the reviewed-change workflow identity and nothing else, so a
locally signed document is refused there by the **ordinary** check that refuses any
unlisted identity — not by a mode flag, not by a coordinate comparison, and not by
a code path that exists only to be disabled. There is no `local` trust policy
because there is no local trust *mechanism*: one verifier, one policy shape, two
sets of listed identities.

A deployed host that somehow listed a developer identity would be misconfigured in
exactly the way an allowlist is designed to make visible — it is a reviewable line
in the policy, which is the property the signing decision was taken for.

### What a local run must therefore supply

The host's requirements do not change for local; what changes is who provides
them. A local run needs: a presence document whose `host.coordinate` is the
coordinate the local host is configured with; an `execution.image.manifest_digest`
read from the **locally built** image rather than a registry, which moves on every
build and is fine because the generation moves with it; workloads running as Pods
with projected service-account tokens for audience `accounts`; and the documents
signed with the developer's keyless identity and POSTed to the same
`/platform/_delivery/*` endpoints. The local host is configured with its local
coordinate and an allowlist containing that developer identity.

Nothing on the host is conditional on any of it. That is the point.

### The three fields a local run looks unable to fill, and why it can

The renderer's objection to this decision was precise and worth recording with its
answer, because it is the objection anyone will raise again: a present generation
must declare at least one rendered artifact with a SHA-256 digest, and v2 adds a
required OCI manifest digest and a SPIFFE ID — so a laptop appears to have to
**invent digests**, which it must not.

It does not, once the local run uses pods:

- **`artifacts[].digest`** — to run pods the renderer must produce Kubernetes
  manifests, and those are real bytes with a real digest. That is a rendered
  artifact in the honest sense; it lives in a local tree rather than a delivery
  repository, which the document never contradicts because there is deliberately
  no artifact URI.
- **`execution.image.manifest_digest`** — the locally built image's manifest
  digest. Real, and it moves on every rebuild, which is correct rather than
  awkward: the generation moves with it, so each rebuild is an ordinary apply.
- **`execution.identity.spiffe_id`** — the one that looks impossible and is not.
  Core validates this field for **well-formedness only**: the parsed scheme must
  be `spiffe` and nothing more. It does not require an issuer to exist, a trust
  bundle to be present, or an SVID to be obtainable. So a local run populates it
  with the service-account-derived identity, which *is* the identity the pod runs
  as, and whether anything verifies it remains the conditional-mTLS rule — require
  and verify a peer certificate only where a trust bundle is projected. No local
  SPIRE, no invention, and no schema exception.

So presence v2 **can** describe a local run honestly. What it cannot describe
honestly is a run of bare *processes* — which is an argument for running pods
locally, not an argument for a second trust model.

### Two rules that do not bend locally

**Absence is never removal, under any policy.** A local run makes this tempting in
a way a deployment does not: stopping `codefly run` feels like removing what it was
running. It is not. Removal is a tombstone generation, delivered, locally included.
A reconciler that inverted this under a local flag would hold two opposite answers
to "the desired set is empty" selected by configuration, and one misdetection would
withdraw every solution on a deployed host silently, with the audit trail recording
that it was asked to. `TestSolutionHostBinding_AnEmptyMountRemovesNothing` and its
unreadable-mount sibling exist for that reason.

**The envelope stays an independent upper bound.** Locally there is no reviewed
envelope, and the tempting shortcut is to let the delivered authority *be* the
ceiling. That collapses two of the three terms in `binding ∩ envelope ∩
installation`, so a developer would exercise a different authorisation shape than
production computes. A local envelope file in the host's `local` configuration
profile — same schema as the reviewed one, authored by the developer — keeps the
structure identical at the cost of one more file.

### If the local-cluster requirement is judged too expensive

The honest alternative is **not** a second trust model. It is **no capability
surface locally**: a local run gets presence and routing so a developer sees their
UI, and module credentials are simply not minted. This host already has that shape
— an absent `MODULE_IDENTITY_SECRETS` denies every module identity exchange, and
the fake-auth fixture is the established local-authority mechanism.

That is a *reduction* in local capability, which is honest and leaves one trust
path, rather than an *addition* of a parallel one. "Lighter laptop" against "two
trust models" is a false choice while "lighter laptop, less local capability"
exists, and it is the option to weigh against this section rather than the local
attestation.
