# Solution registration: trust model, authority, and compatibility

A **solution** is an independently deployed module this host has no build-time
knowledge of. Delivery declares its presence through one signed module manifest
per binding; the host reconciles that declaration into durable state.
The gateway reads that state for `/solutions/{id}/…`, and the frontend reads it
for `/s/{id}`. This document states who may declare presence, what the host
checks, and how presence differs from a tenant's authority to use a solution.

An optional **solution group** organises the catalogue and member navigation.
The composing platform supplies its stable `id`, display `title` and signed
32-bit `order` on the solution's signed manifest, independently of its frontend.
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

The applied manifest names every endpoint a component owns, with its address:
resolved at render in a cell, at delivery in a native run. The internal
applied-state read carries the applied manifest, and the gateway routes an
alias to the one HTTP-family endpoint the manifest declares for its own
workloads, at the address the signed manifest carries. Neither Accounts nor the
gateway reads a startup carrier for it, and the registry gains no address
column; a withheld desired generation never replaces the applied generation's
addresses.

## 2. Declared authority

Only an admitted delivery declaration creates or removes solution presence.
The signed carrier, trust anchor, ownership domains and generation rules are
specified in §7. Runtime credentials confer no authority to change presence.

The internal `SolutionRegistryService` exposes reads. The gateway reads its
snapshot; the frontend obtains the projection through `GET /solutions/_registry`
using the cluster-internal credential. Registry identity and the immutable target
are host facts, never assertions accepted from a browser or remote component.

An upstream — a solution's backend or a module's endpoint, reached over plain
HTTP inside the cell's mesh at the address its applied manifest declares for
the endpoint — receives forwarded viewer credentials.
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

## 7. Declared presence: one signed module manifest per binding

A module or composed solution is present on this host because a signed
**module manifest** declared it — never because its process announced itself.
One `codefly/module-manifest/v1` per binding (core's
`solutionhost.ModuleManifest`, documented in core's `docs/module-manifest.md`)
says which instance it is, where it runs, which workloads run as it, where its
endpoints are and what authority it asks for. It replaced the presence,
authority and endpoint-delivery documents, which this host no longer reads in
any version; a document of that family is refused with core's instruction to
re-render it with a current Codefly CLI.

| Fact | Manifest field | What the host derives from the applied generation |
| --- | --- | --- |
| That the instance is present, at which generation | `binding`, `generation`, `removed` | `solution_host_bindings.applied_*`, the installable target, the registration |
| The route it answers on | `alias` (one lowercase segment) | the registry key (`/solutions/<alias>`, `/v1/<alias>`) |
| Solution or module | `kind` | the registration's declared kind |
| The release it deploys | `release` | `applied_release` |
| The host and ownership domain it speaks for | `host.coordinate`, `host.domain` | `applied_domain`; who may change the binding |
| Its presentation | `group`, `frontend` | the catalogue group and frontend block |
| The workloads that run as its principal | `workloads[] {service, container}` | the module registry entry's workloads, by the cell's rule |
| Where its endpoints are | `endpoints[] {name, service, api, address}` | the gateway's route upstream |
| What authority it asks for | `authority` | intersected with the administrator's envelope grant |

The manifest carries no credential, no observation, no health and **no build
approval**: no image digest is compared when a module mints. "Only approved
images run" is deploy-time admission, outside this host.

### The delivery endpoint

`POST /platform/_delivery/manifest` takes one carrier — core's
`MarshalSigned` output, `{"document": <canonical JSON>, "envelope": <DSSE>}` —
and answers, in this order:

1. **Parse** the carrier with core's strict `ParseSigned`.
2. **Verify** it with `VerifyDelivered` against the pinned keys of the trust
   anchor: the envelope's one signature under a pinned key id, the payload's
   canonical encoding of a valid manifest, and the signer's pin for the
   manifest's `host.domain`.
3. **Authorise the carrier**: a TokenReview of the bearer token (audience
   `accounts`) — or, in a native run, core's native attester under the run's
   anchor — must name the `delivery` ServiceAccount of the namespace the cell
   runs the manifest's module in — for a withdrawal, the module of the binding's
   previously applied manifest.
4. **Admit** it with core's `Admit` against the binding's applied manifest, and
   its alias with core's `CheckAlias` against every other binding as it stands
   once its newest delivered generation applies.
5. **Hold it to the host's own rules** (below) against every other binding as
   it stands once its newest delivered generation applies.
6. **Persist** it in the append-only inbox and ask for a reconcile pass.

| Answer | Body | Meaning |
| --- | --- | --- |
| 200 | `{"disposition":"accepted","binding","generation","digest","signer"}` | new and durable; a pass is requested |
| 200 | `{"disposition":"replayed",…}` | this exact generation is already durable; nothing written |
| 400 | `{"error":…}` | not a carrier, not a canonical valid manifest, or a replaced document schema |
| 401 | `{"error":…}` | no key this host pins signed it, or the API server refused the token |
| 403 | `{"error":…}` | not the module's `delivery` account, the signer or binding belongs to another ownership domain, or it claims a principal or publisher another domain's binding holds |
| 409 | `{"error":…}` | a stale generation, a rewritten one (applied or only delivered), a withdrawn binding brought back, or a workload or principal another binding holds (named) |
| 422 | `{"error":…}` | another host's manifest, a reserved or held route alias, or an endpoint address that is not its workload's in-cluster Service |
| 503 | `{"error":…}` | the API server could not be asked, or this host's applied record is unusable: retry |

**The signing input is the canonical JSON** (`SignedPayload`, the manifest's
`CanonicalBytes`), never the YAML a delivery repository holds: core refuses a
payload that is not the canonical encoding of the manifest it decodes to, even
under a genuine signature. **An envelope must be present and be a JSON object**
carrying exactly one signature under a pinned key id; No carrier may nominate
its own key. **What the host stores is derived from the attested bytes**, never
from a value read earlier, so the generation and digest in a row are over the
bytes the signature covered.

Every refusal names its cause in the body and in the host's log. Verification
comes before carrier authorisation, so a forged carrier is reported as a signing
problem and a captured carrier replayed by an authorised caller as what it is.

### The namespace rule

A manifest declares no namespace. A cell runs a module's workloads in the
namespace named after the module and as the ServiceAccount named after the
service (core's `workloadidentity` rule); a cell composing several modules
prefixes the module with its environment's namespace,
`<environment namespace>-<module>`. The host reads which spelling applies off its
**own** placement — the namespace it runs in and the module it was composed as
(`CODEFLY__MODULE`) — and refuses to boot, naming both, when that placement
follows neither. The module of a manifest is its `alias`.

So a carrier for module `reports` on a host running as `saas` in
`platform-acme-staging-saas` is `platform-acme-staging-reports/delivery`, and the
workload `{service: api, container: api}` mints from
`platform-acme-staging-reports/api`.

### The host's own rules

Core judges a manifest on its own and against its binding's history. Three
rules need what only this host knows — the other bindings it holds and where its
cell runs things — and are checked at receipt, so a second claim is refused with
the binding that holds it, and again by every pass against the bindings as they
stand:

- **One workload, one manifest.** A workload is one (namespace, ServiceAccount)
  by the namespace rule. Two live manifests on this host never both declare it,
  so a reviewed token matches at most one manifest. The second is refused
  (409) naming the binding that holds the workload; of two first deliveries
  admitted by one pass, the first in delivery order keeps it. A withdrawal holds
  nothing.
- **A principal and a publisher stay with their domain.** A signer pinned for
  domain D speaks for D's bindings only: a manifest may not ask for an
  `authority.principal`, or deploy a `release.publisher`, that a live binding of
  another ownership domain holds (403, naming the holder). The first domain to
  hold one keeps it; a binding keeps its first domain (`Admit`). Within a domain
  a principal is still one live binding: the registry would omit both, so a
  second claim is refused (409) naming the holder.
- **An endpoint is its workload's in-cluster Service.** Every
  `endpoints[].address` is `http(s)://<service>.<namespace>.svc.cluster.local:<port>`,
  where `<service>` is the endpoint's own service and `<namespace>` the module's
  by the rule — no path, query or user info, never a free host. The metadata
  server (`169.254.169.254`), another module's namespace and another workload's
  Service are refused (422). In a native run an endpoint is the run's own
  process on the loopback (`127.0.0.1`, `::1`, `localhost`) and nothing else.

### Prepare, then activate

Every pass reads the newest generation per binding from the durable inbox and
verifies each carrier again under the current pins, so a key unpinned since
stops keeping its manifests in force. Each manifest is admitted with `Admit`
against its binding's applied record; aliases are judged with `CheckAlias`
against the applied manifests, an applied manifest whose binding this pass
admits a withdrawal or a new alias for holding nothing, so an alias moves between
bindings in one pass. Desired state is recorded for every delivered binding
before any generation is applied, so what delivery wants and why it was withheld
are visible even when nothing passed. A refusal is attributed to its binding and
leaves the running generation untouched.

Two replicas converge without applying a generation twice: each apply re-decides
under the binding row's lock, and a replay writes nothing. An unreadable inbox is
an **error**, never an empty desired set. Removal is a withdrawal generation
(`removed: true`), and a withdrawal is **terminal**: a later generation of the
binding is refused, so installations and grants never re-attach to a new instance
that reused the id. A replacement instance gets a new binding.

### The ownership domain

`host.domain` is the slice of this host's binding space a manifest speaks for.
The signer must be pinned for it (verification), and a binding keeps the domain
its first applied generation declared (`Admit`). The host persists that domain
as `first_domain`, written once and kept when the live applied manifest is not —
migration 41 cleared every live binding's applied state and kept it — so a
manifest for the binding under another domain is refused (403) even while no
applied manifest remains, at receipt and by the pass. `SOLUTION_HOST_OWNERSHIP_DOMAINS` bounds the domains any pin
may grant: a pin naming another refuses the boot by name.

### Module authority: the manifest asks, an envelope narrows, an installation consents

The module registry every capability decision reads is derived each pass from
the applied, live manifests whose stored carrier still verifies and, where one
is written, the administrator's module envelope. A manifest's
`authority.principal` is the module's principal. With an envelope, its entry is
the envelope's grant for that principal intersected with what the manifest asks
for — queues, namespaces, resource kinds, and per operation binding the
`invoke`, `lookup` and `headless` scopes — never more than either. With no
envelope, the verified signed manifest is the ceiling on its own.

**What an organisation holds is bounded by what it consented to.** An
installation made with no explicit scope records the consented snapshot of the
ceiling it was installed under, and a later ceiling can only narrow it. A
module installation follows its grant as it moves only while an envelope sets
that grant — a ceiling a platform administrator, or the composition's signed
envelope, approved for every organisation. With no envelope, a later manifest
generation that **widens** the ceiling reaches no organisation that installed
an earlier one: taking it is an explicit lifecycle action by that
organisation's administrator (installing again answers a conflict that names
it), while a fresh installation consents to the current generation. A
narrowing reaches every installation at once. An installation that already
follows its grant (recorded under an envelope, or before the signed manifest
became the ceiling) holds **nothing** on a host with no envelope — its
capabilities answer `FAILED_PRECONDITION` with reason
`INSTALLATION_CONSENT_REQUIRED` — until its administrator installs again. A binding the envelope does not allow, or allows on another
audience, is omitted by name. Two manifests asking for one principal, or two
principals sharing a workload, are omitted. The envelope document is
`{"revision": N, "principals": {"<principal>": {"grant": {…}}}}`; it names no
workload and approves no build.

### The delivered module envelope

The envelope is written two ways, through one writer: interactively by a
platform super_admin (`PlatformAdminService/PutModuleEnvelope`, fresh MFA), or
**delivered** from the composition, which declares each environment's envelope
and signs it at publish with the cell's release key. Without an envelope a
host grants each module what its verified signed manifest asks for, and each
organisation holds what it consented to at installation (above): a later
generation's widening waits for that organisation's administrator.

`POST /platform/_delivery/envelope` takes one carrier of core's
`codefly/module-envelope/v1` (`{document, envelope}`, the same carrier shape,
1 MiB bound and pinned keys as a manifest) and answers, in this order:

1. **Parse** the carrier with `ParseSigned`.
2. **Verify** it with `VerifyDeliveredEnvelope`: the one signature under a pinned
   key, the canonical envelope, a signer pinned as a **release** key — a person
   key is refused (403) by name — and pinned for the envelope's `host.domain`.
3. **Authorise the carrier** as the `delivery` account of **this host's own**
   namespace (the module it was composed as, `CODEFLY__MODULE`, by the namespace
   rule): the envelope is the host's document, not any one module's.
4. **Hold it to the host's rules.** Every grant is first held to the rules the
   interactive write applies (400, naming the rule), through that same parse —
   and then to the one rule that is the host's and not core's. A delivered
   envelope **names no organization**: a grant whose parsed `tenant` is set is
   refused (422). An organization id is this host's own record; a delivered
   grant's tenancy resolves through each organization's installation (the shared
   tenancy, or `cross_tenant` as granted), and a pin to one organization is
   written interactively at a higher revision. The rule reads the **parsed**
   grant, never the raw key, so it sees the field the host will apply: a second
   decoder disagreed with the first about letter case, and a pinned tenant
   revokes that module's installation in every other organization.
5. **Admit** the revision with core's `AdmitEnvelope` against the latest
   delivered one: an exact replay answers `replayed` and writes nothing; a lower
   revision, a rewritten one, another host (422) or another ownership domain
   than the first delivered envelope's (403) is refused. Then the host's own
   rule: the revision must be above the committed one **whatever wrote it** — at
   or below a revision a platform administrator wrote interactively it is
   refused (409) naming that revision, and the composition must state the change
   and deliver above it. Revisions need not be contiguous.
6. **Write** it through the writer the interactive path uses: a narrowing is
   witnessed in the policy log (refused, writing nothing, on a host with none),
   installations the new ceiling no longer allows are revoked, the row and its
   `saas.platform.module_envelope_delivered` audit event (actor `system`;
   revision, previous revision, principals, signer, ownership domain, digest)
   commit together, and a registry pass is requested.

A `200` answers `{"disposition":"accepted"|"replayed","revision","digest","signer"}`;
refusals use the manifest's codes. The row records the signer, the ownership
domain, the canonical payload and the DSSE envelope, and no user; an
interactive row records its user and no delivery (one `CHECK`).

**A delivered revision is re-verified on every read**, against the host's
pinned keys, the way the same reconcile pass re-verifies every applied
manifest. Parsing proves the document is well formed, not that its signer may
still state it, so unpinning a compromised release key withdraws the ceiling it
stated: the read fails, and a reconciler reading it keeps what it last
installed rather than widening. An interactively written revision carries no
carrier — it is this host's own record — so there is nothing to verify, and a
host with no verifier at all still reads it.

A platform super_admin reads and writes it at `/admin/platform/module-authority`:
the current revision, who wrote it and when, and every principal the applied
manifests ask for that it does not allow. With no envelope, applied signed
manifests supply the ceiling directly, and installations hold their consented
snapshot of it. An existing envelope narrows that ceiling;
a failed read never removes its restrictions.

### Configuration

Every key is in the `federation` group.

| Key | Meaning |
| --- | --- |
| `SOLUTION_HOST_COORDINATE` | the coordinate this host answers for, exactly as the operator declared it. It is the one declaration that turns the surface **on**: empty means no reconciler and no delivery endpoint. |
| `SOLUTION_HOST_OWNERSHIP_DOMAINS` | the ownership domains this host accepts delivery under. Required with the coordinate. |
| *(no setting in a pod)* | the pinned-key policy is read from the constant `infra.SolutionHostTrustAnchorPath`; a composition cannot move it. |
| `SOLUTION_HOST_TRUST_POLICY` | `keys`, the only value; required with the coordinate. |
| `SOLUTION_HOST_BINDING_INTERVAL` | optional; how often the inbox is re-read without a trigger. Empty uses 30s. |

Declaring the coordinate refuses to boot without the ownership domains, without
a trust anchor, where no TokenReview can be performed, or where the host's own
placement follows no namespace rule. A process on a developer machine may
relocate the API server and the anchor (`SOLUTION_HOST_LOCAL_CLUSTER`,
`SOLUTION_HOST_LOCAL_TRUST_ANCHOR`); both are refused in a pod (§8).

#### The one trust policy

| Policy | What it checks | Where it is usable |
| --- | --- | --- |
| `keys` | a DSSE envelope over canonical manifest bytes, verified with a pinned public key whose pin also names the ownership domains it may deliver under | deployed hosts and local clusters use the same verifier. A person key requires the cluster-bound local profile in §8. |

The anchor holds one document, **`verification_policy.json`**. Each signer
declares `name`, `kind` (`release` or `person`), `keyId`, `publicKey` (PKIX PEM,
Ed25519 or ECDSA P-256) and `domains`. Unknown fields are refused. The attested
identity is **the key ID**, never the descriptive label. Verification
is offline: the host holds public keys only. Rotation means pin the new key id,
deliver with it, then remove the old pin.

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
| `POST /platform/_delivery/manifest` | Implemented; deployment qualification pending | one signed module manifest, `{document, envelope}` (§7) |
| `POST /platform/_delivery/envelope` | Implemented; deployment qualification pending | the host's module envelope, release-signed, `{document, envelope}` (§7) |
| `GET /platform/_delivery/applied/{binding}` | Implemented; deployment qualification pending | the binding's committed APPLIED manifest, readable only by the protected applied-state observer |

**The delivery path is served by accounts**, which performs the `TokenReview`
on the carrier's own token. The presence, authority and endpoints paths are
gone with the documents they carried, and answer 404. The credential mint remains **NOT YET
LIVE** and is brokered by the gateway when it lands; a consumer calling it today
gets a 404 on every deployment, not just one, which is the one mercy in the
situation. Keep an override until this table says live for it, and do not treat
a 404 as "my path is wrong".

The applied-state read uses the same Accounts delivery listener and reviews an
`accounts`-audience projected token. Only ServiceAccount `endpoint-publisher` in
namespace `istio-presenter` may read it. The ordinary delivery writer cannot use
that read; the publisher cannot deliver a manifest. Mesh policy must
independently admit only this GET from the protected publisher.

A `200` read returns `{host: {coordinate, domain}, binding, generation, digest,
release, removed, appliedAt, pending?}`. The digest is the APPLIED manifest's
(`ModuleManifest.Digest`); the time is RFC3339; `pending`, when present, is a
newer generation this host received and withheld, `{generation, reason}`. Identity comes from the saved
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
host that verifies.

**The carrier is two keys**, exactly core's `MarshalSigned` output:
`{"document": <canonical JSON>, "envelope": <DSSE envelope>}`, parsed with core's
strict `ParseSigned`.

**The carrier is the `delivery` ServiceAccount of the module's namespace** — the
namespace the cell runs the manifest's module in, by the rule in §7; for a
withdrawal, the module of the binding's previously applied manifest. One delivery
Job runs per module tree in that namespace. The namespace is derived from the
signed manifest's alias and this host's own placement, never from configuration a
deployer can edit.

**The projected token's audience is `accounts`, exactly that literal**, the same
audience the module mint reviews against. It is a constant, not a setting: a
configurable audience would be another composition-supplied input to a trust
decision.

**The perimeter is not this check.** The delivery controller's destination list
refuses a manifest's objects outside the namespaces it may write, at apply. The
host's SA-and-namespace check is the second layer.

**Verification is offline**, using core's standard-library cryptography and the
host's pinned public keys only. That matters in default-deny namespaces: an
external lookup would **hang rather than fail fast**.

**The authenticating container is the manifest's.** Each workload names the one
container that authenticates; a pod holding any other container that could read
the projected token — a sidecar, a running or restartable init container, an
ephemeral container — mints nothing.

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

Binding a credential to an execution means asking the environment's mechanism
which workload presented it: in a cell, `TokenReview` on its projected
service-account token, then the pod by the name that review returns (no image is
read: the mint is bound by rule, not by build); in a native run, core's native
attester under the run's anchor (below).

The `local` trust policy is deleted. A coordinate is an operator-declared
string and cannot justify skipping a signature check. Local runs use Kubernetes
and the same pinned-key verification as deployed hosts: one verifier, one policy
shape, with a release key in a deployed policy and a person's key admitted only
by a local policy bound to one cluster or one native run.

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
them. A local run delivers the same module manifest — signed with the developer's
pinned key, its `host.coordinate` the local host's — to the same
`POST /platform/_delivery/manifest`, and its endpoints carry the addresses the
run allocated. The local host is configured with its local coordinate and the
local profile above.

**A native run is attested by its launcher.** The Codefly CLI that renders
module manifests (codefly-dev/cli#1014) starts a native run as the launcher of
its processes and signs each one a credential of core's
`workloadidentity/native` mechanism, naming the declared workload it started,
instead of standing in for an API server. The host verifies those credentials
against the run's anchor, the way a cell's host reviews a projected token, and
the anchor is bound the way a cluster is: the CLI hands the host the anchor in
`CODEFLY_NATIVE_IDENTITY_ANCHOR` (core refuses it inside a pod) and pins its key
fingerprint in the local profile,

```json
{ "local": { "nativeAnchorSha256": "<core's native.Anchor.Fingerprint()>" }, "signers": [ … ] }
```

and the host refuses to boot unless the anchor it was handed is the one the
profile pins. A profile binds to a cluster or to a native run, never both; a
deployed policy, or one bound to a cluster, refuses a native anchor. Under a
native run:

- the carrier is the `delivery` workload of the manifest's module, and the
  applied observer is core's `AppliedObserver`;
- a minting process is the workload its credential names: its module is the
  namespace, its service the account, its instance the pod, and the containers
  are what the launcher vouched for about that instance;
- a module's namespace is its name (the bare rule), and an endpoint's address is
  the run's loopback.

A local run on a cluster (pods with projected tokens) works as a cell does.

### Two rules that do not bend locally

**Absence is never removal, under any policy.** A local run makes this tempting in
a way a deployment does not: stopping `codefly run` feels like removing what it was
running. It is not. Removal is a tombstone generation, delivered, locally included.
A reconciler that inverted this under a local flag would hold two opposite answers
to "the desired set is empty" selected by configuration, and one misdetection would
withdraw every solution on a deployed host silently, with the audit trail recording
that it was asked to. A local run withdraws with a withdrawal manifest. `TestSolutionHostBinding_AnEmptyMountRemovesNothing` and its
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

Codefly selects the source in the render-owned `artifact-cache` group, and
Accounts and the gateway read it through `frontendpayload.Configure`. A native
run creates its digest-named selection cache and injects `artifact-cache/ROOT`;
they open it read-only, and a Pod or nonlocal run refuses that source. A
deployed render builds each component's bundle from its release, publishes it
by digest to one repository beside the cell's images, and injects
`artifact-cache/REPOSITORY`; a Pod reads it over HTTPS as its own workload
identity (the metadata server's token, no stored credential), and outside a Pod
that value is refused. The two never coexist. Reader access to the repository
is the cell operator's grant to these two workloads; retention is the
repository's. A live cell read has not been qualified here.

### Admission with no consumed permissions

A manifest's `authority` can ask for no operation binding at all. The envelope
must still grant its principal; the registry entry then holds no consumed
operation, and uses the existing installation path. The applied manifest, its
workloads, the envelope revision and live organisation consent still have to
agree.
Installation-use consent and team grants remain required even for a no-op
installation. An empty permission list does not make a page public.

The `artifact-cache` configuration group belongs to the CLI: the native run
injects its actual retained root, a deployed render its bundle repository, and authored defaults (including empty files)
are refused by the render. Use the lifecycle CLI candidate for local validation;
released CLI 0.1.185 does not yet recognise this group as CLI-owned. The root and
module-tools configuration gates both enforce that no authored default ships.
Production artifact custody remains an independent deployment requirement.
