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
and per viewer. `GET /api/solutions/register` (the navigation menu) and
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

The authority answer is `SolutionEntitlementService.ListSolutionEntitlements`,
which reads the installation set and the scope grant + share union in **one**
transaction, so the two can never describe different moments. The join key is the
solution id: `installations.solution_identifier` is the registered manifest `id`.

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

## 6. Upgrading to per-viewer projections (issue #949)

**Every Solutions menu and every client's surface list is empty immediately
after this upgrade, for every user, admins included.** That is the consequence of
§4's rules, not a fault: before this contract a *registered* solution was listed
for everyone, and now a solution is listed only when the viewer's organization has
an active installation of it **and** one of the viewer's teams (or the viewer) holds
a grant that permits `(solution, use)` at or above that installation's scope node.
Registration never created installations, `(solution, use)` is a permission no role
carried before, and there are no implicit grants — so at upgrade no viewer
satisfies both. Registration, pages and the solution proxy are unchanged: a solution
still renders at `/s/{id}` for anyone the gateway authenticates.

To restore a solution for an organization, an org admin:

1. installs it with `InstallationService/InstallSolution`, passing the solution's
   **registered id** as `solution_identifier` (see below);
2. creates a role permitting `solution:use` (or grants a role that already carries
   `*:*`) with `PermissionService/CreateRole`;
3. grants that role with `PermissionService/GrantScope` to each team that should
   see it, at the installation's authority-root scope node — or at the
   organization root to give it to every team at once, which the scope tree's
   ancestor rule then carries down to every solution node.

The host ships no migration that writes these grants: the owner's decision is that
a solution reaches no team until a grant is written, and a backfill that granted
every registered solution to every organization would be exactly the implicit
grant that decision rules out.

**The solution identifier must be the registered id.** An installation's
`solution_identifier` is free text, because an installation also governs agent
authority and any identifier serves that purpose; the projections, though, match it
against the registered manifest `id`, a lowercase slug. An installation under any
other identifier (`acme.example/solution`, say) is valid for agent authority and can
never appear in a menu. The frontend reports each such identifier once, in its log
(`solution projections: an installation's solution_identifier … is not a
registered-solution id shape`), so the mismatch is visible rather than silent.

## 7. Declared presence: delivery says what runs, a heartbeat says how it is

Everything above describes presence that a solution **announces**. A solution
becomes present because a process is up and heartbeating, which means the host
cannot tell "this solution was never deployed" from "it was deployed and is not
answering" — it has the same nothing in both cases. Issue #952 adds the opposite
record: a `SolutionHostBinding` document that delivery **declares** and the host
reconciles.

Self-registration is **not** removed here. Both paths write the same durable
registry, and the rule that makes that safe is stated in the mixed window below.

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
| Whether the workload is alive | the heartbeat | the per-half lease |
| Where it answers | the heartbeat | `backend_upstream` |
| Which page it serves | the heartbeat | `frontend_manifest` |

The document deliberately carries no upstream address and no manifest. An
address written into a delivery document is a resolution result that was true on
one cluster until something moved; a manifest is what the running build actually
published. Both are observations, and observations are the runtime's to report.

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

Every pass reads the mount, then asks Core about the **whole** desired set —
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

An unreadable mount is an **error**, never an empty desired set, and an empty
mount removes nothing. Removal is a tombstone generation precisely so that a
volume that failed to mount, or a delivery tree that synced empty, can never be
reconciled as "withdraw every solution".

### The mixed window

Until the runtimes stop self-registering, both paths write
`solution_registrations`, and the rule is asymmetric.

A heartbeat for a **declared** record may refresh the lease, the upstream address
and the manifest. It may not create presence, replace the release, repoint the
route, or erase a tombstone:

- Naming a service alias other than the declared one is refused
  (`ErrSolutionRegistrationDeclaredRoute`).
- A declared removal refuses every heartbeat, including one naming the
  tombstone's own revision — the path an **undeclared** tombstone deliberately
  allows (`ErrSolutionRegistrationDeclaredWithdrawn`). Honouring it would serve a
  solution delivery declared absent for exactly one reconcile interval.
- Deregistering a declared solution is refused
  (`ErrSolutionRegistrationDeclared`): removal is a tombstone generation from
  delivery, not a `DELETE`.

All three reach the gateway as `FailedPrecondition` and are relayed as `409`,
which is the answer a registrant must not retry. The gateway needs no knowledge
of declarations to relay them.

A heartbeat for an **undeclared** record behaves exactly as it did before. Every
record in every existing deployment is undeclared, so nothing changes for a
solution until an operator declares one.

A record becomes declared when a generation **applies**, never when a document
merely arrives. That is what stops a document which has not passed every check
from taking a working self-registered solution offline — and it is what makes the
migration seamless in the other direction: declaring a solution that already
self-registered **adopts** its halves and leases rather than replacing them, so it
keeps serving across the instant its presence becomes declared. The publisher of
record stays `solution:<id>`, the subject its own credential proves, because
writing the release publisher there instead would make every subsequent heartbeat
fail the publisher check and the record would sit pending forever.

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
| `SOLUTION_HOST_BINDINGS_DIR` | the directory delivery places rendered documents in. Empty leaves the reconciler **off**, which is the default while runtimes migrate: nothing is declared and every solution is present because it heartbeats. |
| `SOLUTION_HOST_COORDINATE` | the coordinate this host answers for, exactly as the operator declared it on the environment the renderer read. Never derived here — an invented coordinate matches nothing delivery wrote, so every document would be refused. |
| `SOLUTION_HOST_OWNERSHIP_DOMAINS` | the ownership domains this host accepts delivery from. Required with the mount and refusing to boot when empty, for the same reason the coordinate is: it is the only thing bounding a binding's first generation. |
| `SOLUTION_HOST_SIGNER_DOMAINS` | which ownership domains each **attested signer identity** may deliver under: `<identity>=<domain>[\|<domain>]` entries, comma-separated. Required with the mount. An entry naming a domain `SOLUTION_HOST_OWNERSHIP_DOMAINS` does not accept is refused **by name**. |
| `SOLUTION_HOST_TRUST_POLICY` | how a delivered carrier's bundle is checked: `keyless` or `local`. Required with the mount, with **no default** — every default is wrong somewhere. |
| `SOLUTION_HOST_BINDING_INTERVAL` | optional; how often the mount is re-read. Empty uses 30s. |

Declaring the mount without the coordinate, or either without the ownership
domains, refuses to boot. A mount with no coordinate would leave this host unable
to refuse a document delivered to another host, and Core's target check is the
only thing standing between the two; a mount with no domains would leave it
unable to refuse a document claiming a binding it has never seen.

### What the host admits, and why it is now a signed carrier

Core `cd443989` removed every path from unattested bytes to a host judgement.
`Host.Admit` takes `*solutionhost.Delivered`, whose fields are unexported and
whose only constructor is `VerifyDelivered(carrier, BundleVerifier)`. So "this
document was attested" is held by the compiler rather than by a naming
convention, and a host that forgot to verify cannot compile rather than
discovering it in production.

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

#### The two trust policies

| Policy | What it checks | Where it is usable |
| --- | --- | --- |
| `keyless` | a Sigstore bundle against a trust root and an identity allowlist — repository, workflow path, ref pattern, issuer. **Never a key.** | production. **NOT YET AVAILABLE**: it refuses at boot, see below. |
| `local` | **nothing.** It attests every carrier as the identity `local`. | a **local coordinate only** (`local/…`, `localhost/…`), enforced in code. A policy that trusts whatever is in the mount is sound exactly where the mount and the host are the same person. |

`local` still goes **through** the signer-to-domain check rather than around it:
the operator grants the `local` identity the domains a developer may deliver
under, using the same key a workflow identity uses.

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

This is the existing precedent, not a new rule: `POST /solutions/_register` and
`GET /solutions/_entitlements` are gateway code constants published in this
document, and every registrant hardcodes those paths today.

### `/platform/*` is the host's own control namespace, and it is reserved

| Path | Status | What it is |
| --- | --- | --- |
| `POST /platform/_credential` | **Settled, NOT YET LIVE** | the module/solution credential mint. On the **gateway**, which forwards `Authorization` verbatim; accounts performs the `TokenReview` on the pod's own token. |
| `POST /platform/_delivery/presence` | **Settled, NOT YET LIVE** | a signed presence document, `{document, bundle}` |
| `POST /platform/_delivery/authority` | **Settled, NOT YET LIVE** | a signed authority document, same carrier |

**NOT YET LIVE means these paths do not exist on any deployment.** A consumer
calling one today gets a 404 — on every deployment, not just one, which is the
one mercy in the situation. Keep an override until this table says live, and do
not treat a 404 as "my path is wrong".

`/platform/` is **reserved for the host** and no delivered binding may claim it.
Route aliases live under `/solutions/<alias>/` and composed module routes under
`/v1/<as>/`, so nothing can shadow it today; it is reserved explicitly so that
stays true. An unknown `/platform/*` path must answer **404**, never fall through
to another handler — the same trap the adversarial review names for the
`/solutions/_…` segments, where deleting a dispatch case lets a request reach
generic solution routing and answer `502`.

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
Kubernetes locally, and ships no local attestation path at all.**

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
