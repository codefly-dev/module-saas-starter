# Solution registration: trust model, authority, and compatibility

A **solution** is an independently deployed module this host has no build-time
knowledge of. Delivery declares its presence through a signed
`SolutionHostBinding`; the host reconciles that declaration into durable state.
The gateway reads that state for `/solutions/{id}/…`, and the frontend reads it
for `/s/{id}`. This document states who may declare presence, what the host
checks, and how presence differs from a tenant's authority to use a solution.

An optional **solution group** organises the catalogue and member navigation.
The composing platform supplies its stable `id`, display `title` and signed
32-bit `order` on the solution's signed presence, independently of its frontend.
Headless solutions therefore appear in catalogue groups without declaring a page.
The host copies this metadata only when that generation applies, and clears it
when omitted or withdrawn. A group owns no deployment, installation, consent,
grant or permission. Each member remains a separate solution with its existing
target and URL. Member navigation groups only the solutions already admitted
for that viewer; an empty group is hidden. Group changes arrive through the
existing registry refresh and do not restart the host.

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

## Live route delivery

The applied presence names the endpoints a component owns. Codefly supplies
endpoint values separately through the signed delivery inbox. The internal
applied-state read carries those bytes opaquely; the gateway passes them to
`ResolveDeliveredNetworkInstance(applied, raw)`. Core validates the complete
presence digest and the SDK resolves the selected endpoint. Neither Accounts
nor the gateway interprets configuration carrier keys. Endpoint admission and
subsequent applied-state reads use the host's existing signer-to-domain policy;
a valid signature alone does not authorise the asserted ownership domain.

The delivery inbox retains the signed bytes; the identity registry gains no
address column. An exact historical lookup keeps a withheld desired generation
from replacing the endpoint value of the applied generation. Missing, withdrawn
or mismatched values cannot fall back to startup carriers. The existing registry
refresh and guarded transport consume the result without a second registry or
a configuration mount.

This replacement is being integrated across the delivery producer, Core, SDK
and host. It is not yet qualified end to end. Credential delivery remains a
separate production requirement; endpoint values do not replace pod/build
admission. Generation-versioned asset URLs still do not establish immutable
JavaScript or retained lazy-loaded chunks.

## 2. Declared authority

Only an admitted delivery declaration creates or removes solution presence.
The signed carrier, trust anchor, ownership domains and generation rules are
specified in §7. Runtime credentials confer no authority to change presence.

The internal `SolutionRegistryService` exposes reads. The gateway reads its
snapshot; the frontend obtains the projection through `GET /solutions/_registry`
using the cluster-internal credential. Registry identity and the immutable target
are host facts, never assertions accepted from a browser or remote component.

An upstream — a solution's backend or a module's endpoint, reached over plain
HTTP inside the cell's mesh at the address the Codefly SDK resolves for the
endpoint its applied presence declares — receives forwarded viewer credentials.
The transport therefore checks resolved addresses at dial time, refusing public,
link-local and metadata addresses. Composition-local addresses and the explicit
loopback development case remain supported. A changed DNS answer cannot bypass
the transport check.

## 3. Runtime compatibility

The Module-Federation host checks compatibility before activating a remote.
The manifest parser validates the snapshot's shape. Runtime requirements include
`schemaVersion`, `frontend.hostContract`, `frontend.reactRange`,
`frontend.shared` and `frontend.exposedModule`; host versions live in
`services/frontend/code/src/solutions/host-runtime.ts`. A failed check leaves the
remote unavailable and reports the cause in the server log.

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
- **An organization administrator writes that grant by installation and team**
  (`InstallationService.GrantInstallationToTeam`, issue #1061), never by scope
  path: the host resolves the installation's authority-root node in the same
  transaction that writes the grant, refuses a team of another organization, and
  refuses a role that does not permit `(solution, use)` — a grant of one would be
  written and reveal nothing. That permission is the minimum, not a ceiling: the
  team holds everything the role permits beneath the installation's root, as a
  `GrantScope` of that role at that node would give it. The resolution holds a
  share lock on the installation row to commit, and uninstall locks the same row
  before it touches any grant, so both take the installation lock before any
  trigger-acquired lock: a grant and an uninstall serialize, without a lock cycle,
  and a grant never lands on an installation already uninstalled. The
  write is `GrantScope`'s own body, so it emits the same `scope.granted` event
  and audit record. `RevokeInstallationFromTeam`
  is `RevokeScope` after the same resolution: a narrowing under the policy log,
  refused in the host's words when the log cannot witness it.
  `ListOrganizationInstallations` is the administrator's read of the same state —
  each active installation with its health, installed target, authority-root path
  and the teams granted there or inherited from above. It is org-admin and bound to
  the caller's own organization (a platform super administrator, who passes
  every organization-admin check, is the inherited exception), unlike the
  internal `ListInstallations` the gateway reads for a projected tenant. It reads
  the installations and their exposure on one repeatable-read snapshot.
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
| The workload identity to expect | the declaration | `workload` (the SPIFFE ID is the workload's mesh principal) |
| The page a module or solution serves — title, order, exposed module, host contract, dashboard, client surfaces | the declaration | `frontend`, copied onto the record as `frontend_manifest` when the generation applies |
| Where its backend is reached | the composition, through the Codefly SDK | nowhere: the gateway resolves it from the endpoints the document declares |

The document carries no address. An address written into a delivery document is
a resolution result that was true on one cluster until something moved, so the
document declares endpoints by NAME and the Codefly CLI writes this gateway a
`CODEFLY__ENDPOINT__…` carrier for each one a composed workload is reached
through, locally and in a render. **Nothing is reported by a running process**:
the mint carries no origin and no manifest (the two fields are reserved on
`ModuleMintWorkContextRequest`), and the registry holds no address of any kind.

**The gateway resolves where every declared alias is reached, solution and
module alike.** On each refresh it reads the endpoints the APPLIED presence
declares for the service that runs its workload — the presence's own endpoints,
HTTP-family only — and asks the Codefly SDK for each; exactly one answers, and
that is where `/solutions/<alias>/*` and `/v1/<alias>/*` go, over plain HTTP.
None, or more than one, leaves the alias unrouted (`503 … not active`). The
install step is delivered to the same address over the same transport. A
module or solution is **active** once its presence is applied, independently of
its optional `frontend` block (`SolutionRegistrationStatus`). Accounts copies
that block in the same apply transaction; the gateway builds the common manifest
from it plus the host's constants — asset path, backend alias and capability
path. Either kind can serve a page, and either can be headless. Each
organisation's installation readiness and consent remain separate checks.


**The registry stops authorising routes it cannot confirm.** A replica serves
from its snapshot and refreshes it every 10 s; a snapshot the registry has not
confirmed for 120 s — the revocation lease — authorises no solution or module
route (`503 solution registry snapshot is stale`), so an outage never serves a
withdrawn presence indefinitely. A tombstone already held keeps answering as
the registry would. While a module's declaration is live its alias is also a member of the
host's closed audience vocabulary, so a solution can mint a viewer's Work
Context for it; the tombstone that withdraws the module withdraws the audience.
Addressability is all a presence grants: authority for the module's own calls is
still the authority document's alone.

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
| *(no setting in a pod)* | WHERE the pinned-key verification policy is read from is **not configurable in a deployment**: it is a constant, `infra.SolutionHostTrustAnchorPath`. In a pod there is no environment value, no flag and no overridable default; the one relocation, for a host started as a process, is refused in a pod and described below. Workspace environment is delivered by the **composition**, so an overridable path would let a composition point this verifier at a policy it wrote itself, sign its own presence documents, and pass every downstream check — against an anchor it chose. A deployment needing a different path changes the pod spec that mounts it, which the platform renders and the composition cannot supply. That document also carries the signer-to-domain mapping, keyed by `keyId`, so workspace environment cannot widen a key's authority independently of its pin. A signer granted a domain `SOLUTION_HOST_OWNERSHIP_DOMAINS` does not accept is refused **by name**. |
| `SOLUTION_HOST_TRUST_POLICY` | how a delivered carrier's envelope is checked. `keys` is the only value. Required with the coordinate, with **no default** — every default is wrong somewhere. The trust anchor's **absence** is checked first and refuses the boot naming the anchor, so an unmounted anchor is never reported as a missing setting. |
| `SOLUTION_HOST_BINDING_INTERVAL` | optional; how often the inbox is re-read. Empty uses 30s. |

Declaring the coordinate without the ownership domains refuses to boot: this
host would be unable to refuse a document claiming a binding it has never seen.
So does declaring it with no trust anchor mounted at
`infra.SolutionHostTrustAnchorPath`, and so does declaring it where no
TokenReview can be performed — a delivered carrier is authorised by TokenReview,
so a host that cannot reach its API server has no input at all and starting
would leave it polling an inbox nothing can write to.

**A process on a developer machine may relocate both, and a pod never can.**
`codefly run` starts the host as a process, where the kubelet's paths do not
exist and writing them needs root. Two settings name a local stand-in, and each
is **refused at boot when the process runs in a pod** (`infra.InCluster()`), so
neither is a way for a composition to move a deployed host's trust:

- **`SOLUTION_HOST_LOCAL_CLUSTER`** — an absolute directory holding `server`
  (the https origin of the local stand-in for the API server), `ca.crt`,
  `token` and `namespace`. TokenReview and pod reads go there. It does not make
  `InCluster()` true: the process is still not in a pod, and everything keyed
  on that stays local.
- **`SOLUTION_HOST_LOCAL_TRUST_ANCHOR`** — an absolute directory holding
  `verification_policy.json`. A relocated anchor must be a **local** profile —
  one bound to a cluster CA by its `local` block — so it can only ever admit a
  developer's key for the one local cluster whose CA it names.

Neither is a workspace setting and neither is in the table above: both are
read from the process environment by the code that would otherwise read the
kubelet's paths, never through a workspace configuration group.

In a pod both constants stand exactly as described above.

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
`POST /platform/_delivery/presence`, `POST /platform/_delivery/authority`, and
`POST /platform/_delivery/endpoints`. Endpoint delivery follows committed
presence: the producer retries the signed endpoint value if reconciliation is
still pending (503). It needs no access to the separate protected observer. Its storage identity is Core's host/binding key plus the applied
presence generation. Different endpoint bytes at that generation are a rewrite,
not a refresh; a route address change requires a new presence generation.
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

The **ceiling** is the module envelope: a durable, append-only, revisioned
record (`module_envelopes`) a platform super_admin writes at runtime through
`PlatformAdminService/PutModuleEnvelope`, audited and never through an
impersonated session. Per module principal it admits whole authority units,
the workload identities the principal may run as, scope ceilings and the grant
ceiling, and lists the approved builds. Every reconcile
pass and every activation reads the CURRENT revision and hands Core's
`ValidateAgainst` its projection for the document being judged, so containment
is Core's exact rule and a narrowing takes effect at the next check with no
restart. It is never read out of a delivered document — "an envelope a document
carried would be a document declaring its own ceiling".

**Only a pure grant skips the policy-log witness.** A revision is a pure grant
when, against the revision in force, every approved build is still approved,
every principal is still there with a section that is the same JSON value,
every other top-level key is the same value, and no installation loses its
organization. What may differ is the revision, approved builds that were
added, and whole principals that were added. Everything else is written under
`WithPolicyLoggedNarrowing`, so a host restored from an older database cannot
silently give it back.

A principal's section is compared for equality and never for "at least as
much", because in this schema more JSON does not mean more authority: a
resource list that appears restricts an audience that was unrestricted, a
required kind that appears refuses artifacts that passed, a tenant that is set
removes every other organization, and a unit that gains a field is a different
unit. So a changed section needs the witness, whatever changed. The
classification reads installations on the control plane, and an apply that was
classified a pure grant and then finds an installation to revoke stops and is
taken again under the witness. A host with no witness configured therefore
accepts pure grants and refuses everything else. Absence and damage are
different answers: no revision written is a complete deployment that grants no
module anything, while a revision that cannot be read leaves what the last pass
installed serving, because reading a damaged ceiling as "no ceiling" withdraws
every module on a transient error and reading it permissively widens them.

The **module principal registry** every capability decision reads is derived
from this ceiling each pass: for every live authority document inside it, each
principal's entry is the envelope's grant bounded by what the document grants,
with the workload read from the applied presence the document names
([services/accounts/AGENTS.md](services/accounts/AGENTS.md#the-composed-module-service-principal)).

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
whose only constructor is `VerifyDelivered(ctx, carrier, verifier)`. So "this
document was attested" is held by the compiler rather than by a naming
convention, and a host that forgot to verify cannot compile rather than
discovering it in production.

Core `67ee7220` closed the gap that left: a `Delivered` used to hand out the
parsed document it would itself admit, so a caller that adjusted a field changed
what was admitted while the attestation still covered the original bytes. It now
stores the attested payload and re-derives per read.

The host side of that:

- **Delivery carries signed carriers**, `{document, envelope}`, not bare
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
- **An envelope must be present and be a JSON object.** Core verifies the DSSE
  envelope with exactly one signature, a pinned `keyid`, a `payloadType` matching
  the document's schema, and decoded payload bytes identical to `document`.
  Missing, empty or `null` envelopes, extra signatures and carrier-supplied trust
  material are refused. No carrier may nominate its own key.
- **What the host stores is derived from the attested bytes**, every time, and
  never from a document the reconcile pass has been holding. The generation in a
  row and the digest beside it are therefore over the bytes the signature
  covered, which is the property that makes the digest worth storing at all. A
  carrier whose attested payload will not re-read is refused like any other,
  rather than admitted from a value read earlier.

#### The one trust policy

| Policy | What it checks | Where it is usable |
| --- | --- | --- |
| `keys` | a DSSE envelope over canonical document bytes, verified with the platform-owned pinned public key; core also checks the key's ownership domains. | deployed hosts and local clusters use the same verifier. A person key requires the cluster-bound local profile in §8. |

The former signing mechanism was removed under the handbook decision
`delivery-signed-with-platform-owned-keys`. An obsolete policy value is refused
at boot with the instruction to use `keys`; there is no fallback or default.

The anchor contains one document, **`verification_policy.json`**, provisioned
independently of both delivery writers. A missing or invalid policy refuses boot:
"this host cannot verify" and "delivery supplied an invalid signature" are
separate facts an operator must be able to distinguish. The host never reads
additional trust material beside this document.

Each signer declares `name`, required `kind` (`release` or `person`), `keyId`,
`publicKey` (PKIX PUBLIC KEY PEM, Ed25519 or ECDSA P-256), and `domains`. Names and
key IDs are unique. Unknown fields are refused. Core validates the keys, kinds,
IDs and domain lists; the host additionally requires a local profile for a person.
The attested identity is **the key ID**, never the host's descriptive label.

A refusal recorded against a binding is deliberately **coarse** — not a carrier,
an envelope that did not verify, and a verified payload Core will not parse all read
the same in the row. Which of the three it is tells an attacker which half of the
door it got past; the full reason goes to the log.

The host reads delivery from its durable inbox, preserving the canonical payload
and DSSE envelope for verification on every reconciliation. The existing `bundle`
storage column holds the envelope; it does not change the carrier's wire shape.

## 7b. The host's own control paths, and which part a consumer may hardcode

The current solution mint is `POST /modules/_work-context` on the gateway's
`workload` endpoint (`api: http`, plain HTTP on a port of its own). Codefly
resolves its address and port; a workload reads it from the workspace value it
already reads (`platform/gateway-endpoint`, a solution's gateway value), which
the composition points at `${endpoint:<host>/auth-gateway/workload}`. Past the
gateway every hop inside a cell is plain HTTP: the cell's mesh carries
transport security and admits the workload port only from composed workloads
(an `AuthorizationPolicy` the cell's applier compiles from the edges the CLI
delivers), and a native run binds every service but the gateway to loopback.
No workload serves or presents an application-level SVID. A solution presents
its projected ServiceAccount token as `Authorization: Bearer` and nothing else
on this endpoint; accounts' TokenReview/BindExecution path judges it. The
three module credential exchanges are served on the workload endpoint and
nowhere else — the public `rest` endpoint answers them `404`.

A principal the registry does not hold is refused, with one exception: while
the delivery inbox holds a document the registry was derived without, the
answer is `503` ("not established yet") and a pass is requested. A workload
that starts beside its own delivery asks in exactly that moment. Once the pass
has read the inbox, a principal it still does not hold is refused as before.

A workload that is admitted and **installed nowhere** is answered `412`
(`FailedPrecondition` from accounts, on the module's own mint only — a
capability's use and a headless operation still hear the refusal), not `403`: nothing is wrong with the
workload, there is simply no installation to seal a credential to. It serves
its install endpoint and asks again; the install step is delivered to the
address the Codefly SDK resolves for its declared endpoint, exactly as its
traffic is.

Solution proxy traffic is plain HTTP to that address, through the same guarded
transport a module's is: the registry read joins matching registration and
applied-generation records, and dial-time address revalidation rejects off-mesh
and metadata destinations. There is no `workload-identity` configuration group,
no trust bundle and no caller allowlist: the mesh is the only party that
authenticates a hop inside the cell.

**Production mints bind the presenter.** The protected workload listener accepts
one canonical waypoint XFCC assertion and forwards its SPIFFE principal only with
the gateway's own provenance credential. Accounts compares it with the applied
workload and reviewed token's namespace/ServiceAccount, then keeps the pod UID,
running-build and live installation checks. The expected waypoint is supplied
as `gateway/WORKLOAD_PRESENTER_PRINCIPAL`; an empty value prevents production
startup. Infrastructure must pin that value, HTTP sanitization and enrollment,
and prevent direct or alternate-Service access around the waypoint. The same
ServiceAccount still means the same mesh identity, even for different pods.

Native stand-in construction outside a pod selects the sole exemption; a
production Kubernetes client never accepts missing presenter evidence. Legitimate
repeated exchanges with one projection remain valid. Source tests do not qualify
the real mesh: header overwrite, direct PodIP/HBONE refusal and repeated exchanges
must still be exercised together before a production rollout.

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
| `POST /platform/_delivery/endpoints` | Integration in progress | a signed Codefly endpoint value for the exact applied presence; no configuration mount |
| `GET /platform/_delivery/applied/{binding}` | Implemented; deployment qualification pending | committed APPLIED presence, readable only by the protected endpoint publisher |

**The two delivery paths are now served by accounts**, which performs the
`TokenReview` on the carrier's own token. The credential mint remains **NOT YET
LIVE** and is brokered by the gateway when it lands; a consumer calling it today
gets a 404 on every deployment, not just one, which is the one mercy in the
situation. Keep an override until this table says live for it, and do not treat
a 404 as "my path is wrong".

The applied-state read uses the same Accounts delivery listener and reviews an
`accounts`-audience projected token. Only ServiceAccount `endpoint-publisher` in
namespace `istio-presenter` may read it. The ordinary delivery writer cannot use
that read; the publisher cannot write presence or authority. Mesh policy must
independently admit only this GET from the protected publisher.

A `200` read returns `{host: {coordinate, component}, binding, generation,
presenceDigest, release, removed, appliedAt}`. The digest covers the complete
canonical APPLIED presence; the time is RFC3339. Identity comes from the saved
applied document, even when a newer desired generation is withheld. No applied
generation is `404`, including a desired-only binding; a committed tombstone is
`200` with `removed: true`. Missing/refused credentials are `401`, a different
authenticated identity is `403`, and unavailable or inconsistent durable state
is `503`. The response is never cached. A publisher must compare host, binding,
generation, digest and removal with its expected declaration before publishing
endpoints. Neither the write endpoint's `202` nor its replay `200` proves apply.

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

**Verification is OFFLINE**, using core's standard-library cryptography and
only the host's pinned public keys. The verifier has no HTTP client and calls no
third-party signing service. That matters in default-deny namespaces: an external
lookup would **hang rather than fail fast**, although the signature is entirely
checkable from the local pins and envelope.

The release signer uses the platform's Vault Transit through the CLI; a developer
uses an Ed25519 key held locally by the CLI. The host holds public keys only and
never reaches either signer during verification. Rotation means pin the new key
ID, deliver with it, then remove the old pin. Reconciliation verifies stored
carriers under the current pins, so removing a pin also affects older deliveries.

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

Local solution traffic follows the same contract as deployed traffic: plain
HTTP past the gateway, the gateway's resolved `workload` endpoint for every
mint, and the solution proxy forwarding to the address the Codefly SDK resolves
for the solution's declared endpoint. What the mesh does in a cell — admit the
workload port from composed workloads alone — a native run does by binding
the native services to loopback. Native Accounts uses its explicit non-pod
stand-in profile; this provides no mesh presenter and cannot be selected inside
a real pod. No application serves or presents a local SVID.

Binding a credential to an approved execution means reading what the workload
actually runs: `TokenReview` on its projected service-account token, then the pod
by the name that review returns, then the application container's `imageID`. That
is Kubernetes. `codefly run` on a laptop has none of it, and the adversarial
review is right that the delivery API does not preserve the old laptop behaviour —
a host that reads pods is not a Kubernetes-free host any more.

The `local` trust policy is deleted. A coordinate is an operator-declared
string and cannot justify skipping a signature check. Local runs use Kubernetes
and the same pinned-key verification as deployed hosts: one verifier, one policy
shape, with a release key in a deployed policy and a person's key admitted only
by a cluster-bound local policy.

### The local profile's trust anchor

A local host reads **`verification_policy.json`** from the same constant anchor,
`infra.SolutionHostTrustAnchorPath`. The CLI writes this local anchor with the
cluster profile; the composition cannot redirect the verifier. A developer's
private key stays in the OS keychain where available, otherwise in the CLI's
restricted local key file. Only its public key is mounted into the host.

```json
{
  "local": {
    "clusterCertificateAuthoritySha256": "<lowercase hex SHA-256 of the cluster CA certificate's DER>"
  },
  "signers": [
    {
      "name": "developer",
      "kind": "person",
      "keyId": "dev-0123456789abcdef",
      "publicKey": "-----BEGIN PUBLIC KEY-----\n<public key bytes>\n-----END PUBLIC KEY-----\n",
      "domains": ["<host.domain>"]
    }
  ]
}
```

A deployed policy omits `local` and lists `release` keys. A local policy may also
list release keys. `kind` is required; a person without `local` refuses boot.
`name` is a descriptive label, while `keyId` is the verified signer identity and
the key of the signer → ownership-domain map. Each listed domain must also appear
in `SOLUTION_HOST_OWNERSHIP_DOMAINS` or boot refuses the disagreement by name.
Admitting a person changes which key is pinned, never how much is verified.

**The `local` block binds the profile to one cluster**, by the SHA-256 of the
certificate authority the cluster's API server serves under — the certificate
Kubernetes projects into every pod as the service account's `ca.crt`. For a k3d
cluster:

```bash
kubectl --context <cluster.context> -n default get configmap kube-root-ca.crt \
  -o jsonpath='{.data.ca\.crt}' | openssl x509 -outform der | shasum -a 256
```

At boot, before a carrier is read, the host proves it runs in that cluster, using
only what the kubelet puts in its pod — the CA bundle and its own token:

1. **The projected CA bundle is exactly the pinned certificate**: one
   certificate, that fingerprint, nothing else. A bundle carrying any other root
   is refused outright — the developer's authority beside a deployed cluster's
   own is the shape every confusion between two clusters needs, so the case is
   removed rather than argued with.
2. **The API server authenticates this pod's own token.** Over a transport whose
   only root is the pinned certificate and which follows no redirect, the host
   submits a TokenReview of its own projected token and requires the answer to
   name exactly this pod — the namespace, service account, pod name and pod UID
   the token's own claims name (the namespace also matching the
   kubelet-projected namespace file); an answer about any other identity, or a
   token naming no pod, is refused. A genuine API server accepts
   only tokens its own cluster minted, so a server that merely holds a
   certificate under the pinned authority — the developer's real cluster, relayed
   to a deployed pod — refuses the deployed pod's token.
3. **The binding lasts for the life of the process.** That pinned,
   redirect-refusing transport becomes the one the host's cluster client uses for
   every later call — each TokenReview that authorises a delivery, each pod read.
   A backend or DNS switch after boot to a server under any other authority breaks
   authentication instead of continuing under a proof made against someone else.

Until the proof succeeds a local profile admits nothing, so a boot path that
skipped it would refuse developers rather than admit them elsewhere. A host that
cannot prove it — a deployed host handed a local profile, or a local host whose
cluster was recreated — refuses to start, naming the fingerprints its cluster does
project. Every client this host has for the cluster follows no redirect, local or
not.

**Why the cluster's authority and the pod's own token, and not anything
simpler.** The decisive question is how a host that is not the developer's refuses
a local profile **without trusting anything the composition controls** — and the
composition delivers this process's workspace environment, which is why the
anchor's path is a constant.

- *A setting, a coordinate prefix, any environment value* — refused. The
  composition could declare itself local; the deleted `local` trust policy died of
  exactly this, its gate a string an operator declared.
- *A cluster's self-description* — a `+k3s` server version, a node's provider
  ID — is a convention, not a property. A deployed cell may run k3s.
- *The `kube-system` namespace UID* — unforgeable through an authenticated API
  server, but it needs a cluster-scoped read this host does not otherwise hold,
  and the own-token review already binds the pod to the cluster with the
  TokenReview permission the host has.
- ***The projected CA, held exactly and for the process lifetime, and the
  cluster's acceptance of this pod's own token*** — chosen. Both are put in the
  pod by the kubelet, not by the composition.

**What is guaranteed.** A host whose pod carries the CA bundle and the
service-account token of any cluster other than the pinned one refuses the
profile at boot — whatever its workspace environment, its coordinate or its
`KUBERNETES_SERVICE_HOST`/`PORT` say. A steered API address does not change that:
the server it reaches must present a chain to the pinned authority (which a
deployed pod's own bundle does not even contain) and must accept a token only the
pod's real cluster minted.

**What is not guaranteed, precisely:**

- *The binding is to one cluster, not to the word "local".* Nothing a host can
  observe distinguishes a developer's cluster from a production one by kind, so
  whoever writes a deployed host's trust anchor could deliberately pin *that*
  cluster's authority and admit a person there. That grants nothing new — the
  anchor's writer can already pin any signing key — and it cannot happen
  by accident: a profile copied to another cluster names the wrong authority.
- *An attacker holding three things at once defeats it:* the power to replace
  this pod's projected CA bundle with exactly the pinned certificate (pod-spec
  control, or a write to the namespace's `kube-root-ca.crt` ConfigMap the kubelet
  projects, which the control plane reconciles back), the power to steer
  `KUBERNETES_SERVICE_HOST`/`PORT`, **and** the pinned cluster's CA private key, to
  run a fake API server that accepts any token. Such an attacker already decides
  every Kubernetes answer this host relies on — every TokenReview, every pod
  read — so the local profile gives it nothing it lacked. The composition holds
  none of the three by itself: it delivers workspace environment, and neither the
  pod spec, the projected CA nor the developer's cluster key.
- *A CA is not a cluster boundary when it is shared or cloned.* Clusters sharing
  one authority are one cluster to this check, and anyone holding that
  authority's key can run a server the check accepts and answer its TokenReview
  as it likes — whether or not the genuine servers accept each other's tokens.
  The own-identity comparison checks that an honest API server answered about
  this pod; it cannot stop a control plane that is itself hostile and copies the
  expected claims back.

**The cost:** a local profile is valid in exactly one cluster. Recreating the
cluster mints a new authority and the profile must be rewritten — one copy of the
fingerprint the boot refusal prints. And the host needs nothing new: it already
creates TokenReviews.

### What a local run must therefore supply

The host's requirements do not change for local; what changes is who provides
them. A local run needs: a presence document whose `host.coordinate` is the
coordinate the local host is configured with; an `execution.image.manifest_digest`
read from the **locally built** image rather than a registry, which moves on every
build and is fine because the generation moves with it; workloads running as Pods
with projected service-account tokens for audience `accounts`; and the documents
signed with the developer's pinned key and POSTed to the same
`/platform/_delivery/*` endpoints. The local host is configured with its local
coordinate and the local profile above: a policy bound to its cluster that lists that developer as a person.

Nothing on the host is conditional on any of it but the policy document on its
trust anchor, which the composition does not write. That is the point.

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
  be `spiffe` and nothing more. It names the workload's **mesh principal** — the
  SPIFFE ID the cell's mesh issues to its ServiceAccount — never an SVID the
  workload serves or presents, so a local run populates it with the
  service-account-derived identity, which *is* the identity the pod runs as,
  and nothing on the host verifies a certificate against it. No local SPIRE, no
  invention, and no schema exception.

So presence v2 **can** describe a local run honestly. What it cannot describe
honestly is a run of bare *processes* — which is an argument for running pods
locally, not an argument for a second trust model.

### A run of processes: the CLI answers as the cluster

The argument above ends at "run pods locally". A run of processes is still the
first loop a developer has, so `codefly run` supplies what the host reads from
Kubernetes instead of the host learning a second path. The host's code is the
same in both: TokenReview on the projected token, the pod by the name the review
returns, the build that pod runs, pinned-key verification of every delivered
carrier, the envelope as an independent ceiling.

What the CLI stands in for, per run:

- **the API server** — TokenReview and pod reads over TLS under a CA created for
  that run, with projected tokens issued for the same audiences and rotated as
  the kubelet rotates them. The host reaches it through
  `SOLUTION_HOST_LOCAL_CLUSTER` (§7, Configuration).
- **the trust anchor** — a local profile whose `local` block pins that run's CA,
  read through `SOLUTION_HOST_LOCAL_TRUST_ANCHOR`.

What is weaker than a cluster, stated rather than implied: the build a pod is
said to run is the SHA-256 of the executable the CLI started, and the CLI is
both the party that says so and the party that signs the presence naming it. No
kubelet is a second witness. That is the authority a developer already has over
their own machine, and it is bounded to it — the profile admits a person's key
only for the one CA it names, and both relocations are refused in a pod, so no
deployed host can be pointed at a stand-in.

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
production computes. A local run writes its own envelope revision through the same
`PutModuleEnvelope` a deployed host uses — same schema as the reviewed one,
authored by the developer — which keeps the structure identical at the cost of
one more write.

### If the local-cluster requirement is judged too expensive

The honest alternative is **not** a second trust model. It is **no capability
surface locally**: a local run gets presence and routing so a developer sees their
UI, and module credentials are simply not minted. This host already has that shape
— a host with no delivered authority wires no execution binding, so every module
mint answers that the execution could not be established (#1056), and the
fake-auth fixture is the established local-authority mechanism.

That is a *reduction* in local capability, which is honest and leaves one trust
path, rather than an *addition* of a parallel one. "Lighter laptop" against "two
trust models" is a false choice while "lighter laptop, less local capability"
exists, and it is the option to weigh against this section rather than the local
attestation.

### Frontend generation changes

The gateway stamps an opaque `assetVersion` from the applied binding, target,
generation and release. Browser assets use
`/api/solutions/<id>/versions/<assetVersion>/proxy/assets/...`; the manifest,
remote entry and relative chunks share that directory. Remote builds must use
`publicPath: "auto"` and relative asset paths. The gateway serves a historical version only while its admitted history and
live target permit it; unknown or expired versions receive 410. This is a cache
identity, not an access credential. The shared
viewer navigation poll carries it, removes withdrawn pages and refreshes an open
page when the version changes. The old remote is unmounted during the change,
and a previous loading failure cannot poison the new generation.

This does not by itself make a rolling backend serve immutable assets: delivery
must also bind the resolved endpoint and packaged frontend to the same build.
The browser cache tests do not qualify that producer/deployment boundary.

### Prepared frontend payloads and historical assets

A newly delivered frontend must name its signed payload. Accounts verifies the
complete immutable bundle before applying that generation; a missing, malformed
or unavailable bundle keeps the candidate pending and leaves the previously
applied generation intact. A headless declaration needs no payload. Preparation
uses the shared `frontendpayload` reader, the signed digest, Core's canonical
archive extractor and the complete declared frontend. Both wire bytes and total
expanded content are limited to 64 MiB. The manifest must use `publicPath: auto`,
a named global entry, the declared expose and a complete local asset inventory.

Migration 40 indexes new applied-history decisions by the gateway's existing
opaque asset version. It does not backfill older history: those decisions
predate mandatory immutable preparation and confer no retained serving identity.
`SolutionRegistryService/GetRetainedSolutionFrontend` is an internal, targeted
read. It verifies the exact applied document, digest, binding and still-live
target and returns only the requested frontend declaration. History is never
attached to the shared all-bindings response. Retention includes the target's
last ten applied generations and every applied generation within
`federation/FRONTEND_LAZY_LOAD_WINDOW` (a positive duration, default `24h`).
A rejected candidate is not retained authority. Closing the target revokes every
retained identity; reusing its route alias cannot recover those identities.

Versioned GET/HEAD assets use this authority even when the current generation is
headless. Every versioned request requires an admitted immutable bundle; a
version string alone cannot open the backend asset proxy. A rollback receives a
new version. Assets grant no installation, team membership or API permission.
Already-applied legacy frontends retain only their unversioned compatibility
path; new frontend delivery cannot enter that path without a signed payload.

The gateway bounds bundle preparation to one acquisition at a time and reuses
four verified bundles (at most 256 MiB of cached content). Route snapshots keep
metadata, not additional copies of current bundles. At most eight asset handlers
hold reads concurrently; excess requests receive 503. Each acquisition is bounded
by the existing ten-second registry deadline. Preparation never renews the
120-second registry lease; withdrawal or a replaced target fences cached bytes.
The browser proxy preserves immutable Cache-Control on versioned responses and
preserves withdrawal/unavailability errors from the gateway.

The implemented runtime source is native development only: Codefly owns and
creates its digest-named selection cache, then injects `artifact-cache/ROOT` as a
workspace value into Accounts and the gateway. They open it read-only; a Pod or
nonlocal run refuses that source. The registry reader is separately tested but
production repository, protected reader identity, custody and retention have not
been bound to a deployed composition. These changes do not qualify production
artifact acquisition or production mountless credential delivery.

### Admission with no consumed permissions

A signed authority document can explicitly declare `bindings: []`. The operator
envelope must then admit the exact principal and presence binding using
`admitted_bindings`. Omitted or null binding lists are refused. This admission
uses the existing principal registry and installation path; it grants no
consumed operation. Applied presence, workload identity, container, approved
build, envelope revision and live organisation consent still have to agree.
Installation-use consent and team grants remain required even for a no-op
installation. An empty permission list does not make a page public.
