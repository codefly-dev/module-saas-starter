# AGENTS.md — accounts

Owns identity, tenancy, permissions, approvals, audit, jobs and the durable
records behind them. Authorization is the subject of [AUTHZ.md](./AUTHZ.md),
[AUTHZ_MATRIX.md](./AUTHZ_MATRIX.md) and
[../../AUTHORIZATION_CATALOG.md](../../AUTHORIZATION_CATALOG.md); every table's
scope and RLS posture is in
[../../DATABASE_AUTHORITY.md](../../DATABASE_AUTHORITY.md). This file covers the
registration and module-identity records that only accounts may write.

**Regenerating after a proto change** is one command from this directory,
`codefly generate proto --proto ./proto --output .. --template
accounts/proto/buf.gen.yaml` (Docker, Codefly CLI ≥ 0.1.160), then the
`go generate` steps. The `--output . --local --template buf.gen.local.yaml`
spelling that older docs and the `generated-pins-gate` message still show no
longer works. The full procedure and why are in
[../../REST_SURFACE.md](../../REST_SURFACE.md#regeneration).

A composed module's REST routes belong to the gateway's generated and explicit
catalogs. Its identity secret authenticates the Work Context exchanges below.

The principal directory lists stored identities only. Declared module authority
is read by the capability checks and does not create synthetic directory rows.

## Declared solution presence and the registry

`solution_host_bindings` stores desired and applied `SolutionHostBinding`
generations and pending reasons. Core owns document admission; each apply
re-decides under the binding row lock. A refused or unreadable delivery does not
withdraw the last applied generation. Withdrawal requires a tombstone generation.

`solution_registrations` is the durable read projection, keyed by the route alias.
Every row names its declared binding, generation, release and immutable target.
A binding ID identifies a deployment instance, while an alias identifies a route;
consent names the target and cannot follow an alias to a replacement instance.
Only observations for the same declared target may survive a re-declaration.

The internal `SolutionRegistryService` exposes the registry and host-binding
reads. The gateway reads the snapshot and brokers it to the frontend. Both cache
briefly: gateway reconciliation is approximately 10 seconds with a refresh on
cache miss; frontend snapshots expire after 5 seconds and a failed refresh
reports unavailable. An incomplete record stays pending and is not served.

Migration 26 destructively removes runtime-only state and makes declaration
ownership mandatory. Its from-zero proof is `TestMigration25FromZero` in the
store module; `TestMigration22FromZero` separately proves immutable installation
targets and withdrawal serialization using a freshly created package database.

Audit type admission remains available through
`ModuleCapabilitiesService.DeclareAuditEventTypes`. Its validator and namespace
admission use declared module authority. Namespace ownership, additive schema
changes, immutable visibility and the operator's `external_namespaces` grant
remain enforced. `ModuleEmitAuditEvent` can emit only a type owned by the caller's
solution scope and an authorized namespace. `business.AuditEventResolver` resolves
stored schemas for payload validation and PII redaction.

The principal directory lists stored identities. The `MODULE_PRINCIPALS` map
below supplies declared authority to capability checks only.

See [../../SOLUTION_REGISTRATION.md](../../SOLUTION_REGISTRATION.md#7-declared-presence)
for the trust model, field ownership and delivery configuration.

## The record carries the solution's runtime boundary seed

A runtime task is reachable only under the boundary of the Work Context that
admitted it — the context's `task_id`.
`solution_registrations.runtime_boundary` (migration
`17_solution_runtime_boundary`) is the **seed** that boundary is derived from,
so a run a page admits stays reachable across the mints of one session rather
than only under the one context that admitted it.

**The boundary is derived per organization**, not stored: a UUIDv5 of the seed
and the org id (`business.SolutionRuntimeBoundary`). A run is filed under
(tenant, boundary), so a single per-solution value would make every tenant of a
solution share one. The seed never leaves this host.

**The host assigns it and nothing else can.** The column's `gen_random_uuid()`
default fires on insert; the registry's upsert deliberately omits the column
from its `ON CONFLICT … DO UPDATE`, and `RETURNING` makes the stored value the
one the caller gets back. A UNIQUE constraint keeps two registrations from
sharing a seed. So no request field reaches it, no write replaces it, and it
survives a tombstone — a reactivated registration keeps naming the runs it
already admitted.

**No response carries a seed.** `solutionRegistrationProto` never sets the
field, on any path: not a listing, not a deregistration, not either half's own
write. Nothing above this service needs one, because accounts derives and seals
the boundary itself. `TestSolutionRegistrationResponsesCarryNoRuntimeBoundary`
holds every response to that.

**The mint** reads the seed through
`business.SolutionRuntimeBoundarySeedStore`, which also reports the publisher of
record and whether the backend half is serving, and refuses a missing record and
a tombstone separately. Which solution is asking comes from
`auth.VerifiedSolution`, stamped from the `X-Codefly-Solution-Id` and
`X-Codefly-Solution-Publisher` the gateway proved from that solution's
registration credential (`../auth-gateway/AGENTS.md`); both are forwarded
identity headers, so they are stripped from any caller arriving without a valid
gateway token. Before deriving anything the mint checks the publisher equals the
credential's and the backend half is serving — the half that mints — and a
caller-supplied `task_id` is **refused**, not ignored.

**And every other mint refuses a `task_id` that is somebody's boundary.** A
boundary is not a secret a consumer keeps: a consuming module that serves
durable runs reports, on a run it lets a person read, the Work Context task it
was admitted under, so one read any caller is entitled to would otherwise hand
them a stable value to name on an ordinary mint. `StartTask`'s ordinary path and
`StartInstallationTask` both run `refuseRegisteredBoundary`, which compares the
caller's `task_id` against every stored seed and the boundary derived from each
for the organization named in the request — tombstones included, because a
removed solution's runs may still be executing. It fails **closed**: a registry
that cannot answer refuses the mint, because the capability cannot then be shown
not to be a solution's. Otherwise mints are unchanged: a request with no
verified solution must name its own `task_id`, exactly as the schema used to
require.

## The platform Catalogue is a projection, not a registry

`PlatformAdminService.ListPlatformCatalogue` (super administrators, page
`/admin/platform/catalogue`) lists every composed module and every solution
with what it declares, what runs and where it is installed. It **adds no store**:
modules come from the `MODULE_PRINCIPALS` registry (`Service.modulePrincipals`),
solutions from `solution_registrations` (read with tombstones, so an
installation of a deregistered solution sits beside its tombstone rather than
reading as a solution nobody registered), and installations from
`InstallationStore.ListCatalogueInstallations` — every organization's active
installations under the control plane, with the organization's name, the
`publisher/name:version` of the agent principal each was installed as, and the
team grants at its authority root or above (the same labelled-grant query
`ListCollectionAccess` reads). An installation names its solution by its free-text
`solution_identifier`, so that is the join; an identifier no registration carries
becomes a solution entry known only by its installations.

**Each entry is placed in the registry's state model** — desired, authorized,
applied, observed, withdrawing, physically retired — one message per state,
carrying the public status fields that model names: `declared_revision`,
`authorized_revision`, `applied_revision`, `observed_revision`,
`observation_freshness`, `withdrawal_state`, `credential_revocation_state`,
`retirement_state`. Never one "installed" flag: an entry desired but not
authorized, or observed but no longer authorized, must read as exactly that.
The solution's self-registration status is shown under Desired, as the only
record of intent this host holds today.

**Absent facts are gaps, never values.** Every fact is a `oneof` of its value and
a `CatalogueGap{reason, detail}`. Today no record on this host carries a
declaration or an applied generation (they arrive with applied presence
documents), an approval or a withdrawal (the deployment contract's signed
approval, not built yet), an observation or a retirement (nothing observes the
cluster or reports stop/fence evidence), an installation revision, or a build
size (the presence document's `build_size` section, codefly-dev/core#708,
computed by the CLI at build, codefly-dev/cli#901): the host counts no lines
itself. So each reads `NOT_RECORDED` or `NOT_OBSERVED`.

**Authorization has three cases, not two**: an approval, `not_authorized` — a
known absence the approval record states — and a gap, where the host cannot
tell. An unrecorded approval is never shown as a refusal.

**What an approval approves is the deployment contract's document, not a host
description of it.** The execution inventory (`codefly/execution-inventory/v1`)
has one definition, the Go module `github.com/codefly-dev/cli/contracts/deployment`
(stdlib-only, enforced by its own boundary test). An approval in the Catalogue
names the approved inventory by the digest approval signs, plus which member of
that delivery aggregate the entry is, plus the host's own `build_incarnation`,
which is not an inventory field. The inventory's canonical bytes travel once per
response in `approved_inventories`, and the host reads them only through the
contract: `Check` accepts the inventory's intrinsic form, its digest must be the
approved one, and the containers are the contract's own projection, selected by
member binding and workload name (`Projections`, codefly-dev/cli#902) — never
paired by position, and no traversal of workload templates is written here. No
proto here re-describes an inventory field; a second description would be the
first thing to disagree with what is deployed.

**The observed verdict judges against the authorization, never the
declaration** (`judgeCatalogueObserved`). An observation is each container's image
digest, keyed `<workload id>/<container name>` in the approved inventory's names,
plus the presented incarnation:

- `RUNNING_AUTHORIZED` only when every container of the approved member (init
  containers included) runs its approved digest, nothing else runs, the
  incarnation is the approved one, **and the observation that says so is dated
  and current**;
- `RUNNING_DIFFERS` for any other digest, an extra container, or another
  incarnation;
- `RUNNING_UNAUTHORIZED` for an observation with no current authorization — the
  row the retirement sweep exists to stop;
- `NOT_OBSERVED` with no observation, one missing an approved container (an
  incomplete observation is never counted as approved), or a match the host
  cannot date: no `observed_at`, a stamp older than
  `catalogueObservationValidity`, or one further ahead of this host's clock than
  `catalogueObservationSkew`;
- `NOT_RECORDED` when there is no authorization to read, the approval carries no
  `build_incarnation`, or the approved inventory is not held, does not hash to
  its digest, is refused by the contract, or has no such member.

**Only the affirmative verdict carries that burden, and deliberately so.** It is
the one cell a reader acts on by doing nothing, so it needs an approval complete
enough to judge against — a non-zero `build_incarnation`, since this host assigns
incarnations from one and zero therefore means *none*, which would otherwise match
an observation that also carries none — and an observation that is complete and
current. The two verdicts that report trouble need neither: a stale report of
something running unauthorized is still worth showing, while "Running authorized"
beside "Not observed" is worse than nothing. Withholding the alarming verdict for
want of freshness would hide the risk the page exists to surface.

The window is this host's own, named once in `catalogueObservationValidity`
(`pkg/business/platform_catalogue.go`), because the observing producer declares no
validity of its own yet; when its record carries one, that bound replaces the
constant and this host stops choosing.

**The invariant is enforced twice and asserted in the contract.**
`judgeCatalogueObserved` is the only author of a verdict, and
`enforceCatalogueVerdictEvidence` re-checks every affirmative verdict at the read
boundary before the response leaves, replacing one its own evidence does not
support with a gap and logging it. Nothing should reach the second check — but the
state messages are to be filled from records that do not exist yet (#953's applied
presence, the signed approval, cluster observation), and a filler that set a value
case without re-judging would otherwise publish a green cell. `platform_admin.proto`
states the rule on the `verdict` oneof so a consumer reads it from the contract; it
is Go, not protovalidate, that enforces it, since nothing validates a response on
the wire. The browser does not re-derive the judgement either: `facts.ts` shows the
affirmative cell only when the evidence is present beside it, and never reproduces
the validity window, which would drift from the host's.

Withdrawal, credential revocation, retirement and build size have gap-only
`oneof`s: their value vocabularies are later spec items' and core#708's to define,
and each value case joins its `oneof` when that record exists, read in
`newCatalogueEntry`.

**The registration it returns withholds topology**: `catalogueRegistrationProto`
blanks the frontend manifest, backend upstream and service alias, as every other
browser-facing projection of the registry does, and the runtime-boundary seed is
withheld as on every response.

## The composed-module service principal

A module consuming the module-facing capability surface
(`ModuleCapabilitiesService`: job enqueue/claim, notify, approvals, audit,
events, subject visibility) calls it as its own **service principal**, whose id
is derived from the
declared module prefix (`business.ModulePrincipalID`) — nothing is
hand-authored as an opaque id.

Its authority is declared in the `module-capabilities` group's
`MODULE_PRINCIPALS`, a JSON map keyed by that prefix:

| Key | Grants |
| --- | --- |
| `queues` | enqueue and claim |
| `namespaces` | event publish |
| `resources` | the permission resource types its own content is governed by, bounding both the content reads this host authorizes for it and the records it may place at a scope node |
| `read_audiences` | installed read-only bindings from a retained parent to a fixed audience and canonical scopes |
| `operation_audiences` | installed operation bindings with fixed audience, canonical invoke scopes, a read-only lookup subset, optional `headless_scopes` (a subset of the invoke scopes) that alone may be minted with no person present, and optional `source_delegation_scopes` (a subset of the invoke scopes, on at most one binding) that a person's connect of a datasource source delegates |
| `tenant` | the org it is bound to |
| `cross_tenant` | an inbox worker serving every tenant |

**Unset means no module may call the surface.**

## Minting a module Work Context

The identity itself is a **Work Context**. The auth-gateway brokers the exchange
to `ModuleCapabilitiesService/MintModuleWorkContext` (`EXPOSURE_INTERNAL`), which
authorizes the presented secret against the independent
`MODULE_IDENTITY_SECRETS` digest, refuses a prefix that is not a declared module
principal, and mints a capability owned and actored by the module principal
(`aud=module-capabilities`), emitting a `module.work_context_minted` audit event
once the capability exists. The response carries
`{token, expiresAt, principalId, tenant}`.

- An **empty or absent** `MODULE_IDENTITY_SECRETS` denies every module identity
  exchange. Compositions must provision identity digests and distribute their
  matching secrets before modules can obtain Work Contexts; registration
  credentials never substitute for missing identity digests.
- The tenant is **not requestable** — it is the one `MODULE_PRINCIPALS` declares
  for that principal, so a module cannot name a tenant by asking.
- The capability's effective authority **is, in a running deployment today, the
  LIVE half alone.** `min(sealed, live)` is the target — blocker decision **B1**,
  which *reverses* what this file said before it — and it is **not yet reached**.

  **In a running deployment this mechanism enforces NOTHING on a module
  capability, and that has to be said in those words.** The comparison against a
  sealed ceiling is written and tested, but nothing populates the seal — the
  module Work Context mint (`StartModuleTask`) passes only audience, tenant,
  owner principal, task, session, actor chain and TTL, and `sdk-go`'s token
  carries no installation id, producer epoch, binding revision, build
  incarnation or image digest to put there.

  `checkAgainstSealed` returns on its first line when the seal is nil. So for
  every real module credential the live read runs, fetches the epoch, and the
  result is **discarded**: `AuthorizeModuleCapability` is `moduleGrant` plus a
  database round trip whose answer nothing compares. The declared grant alone
  binds, which is the behaviour that preceded this work.

  The consequences, stated rather than left to be derived:

  - **Uninstalling a solution does not revoke a module capability in flight.**
  - **Advancing an organisation's or a principal's authority epoch does not
    revoke one.**
  - An earlier version of this paragraph said "the intersection has one operand".
    That was still too generous: an intersection of one operand would at least be
    bounded by the live read. Nothing is.

  Code that exists and a behaviour that holds are different facts, and a rules
  file other agents read as a statement of what is in place must lead with the
  second. This bullet has now been corrected three times, each time in the
  direction of claiming less, which is itself the thing to notice about it.

  **What moved, and what the move does NOT yet reach.** Two of the pieces this
  bullet described as absent now exist and run:

  - `ApprovedBuildReconciler` populates the approved-build view from the
    delivered authority inbox, in the same reconcile pass as presence. Before
    it, `MonotonicApprovedBuilds` was never written to, so every principal was
    UNKNOWN and `ApprovedBuild` refused every one.
  - `SetExecutionBinding` is called from `work.go`, with the Kubernetes client
    that reviews a delivery carrier and that approved-build view. Before it, the
    execution reviewer was nil on every running host and `BindExecution`
    answered ErrExecutionUnbound for every caller — the mechanism was built,
    guarded, unit-tested and unreachable.

  **The mint still does not call it, so the sentence above this one still
  holds.** `StartModuleTask` passes audience, tenant, owner principal, task,
  session, actor chain and TTL, and nothing else; the capability still carries
  no seal, and `checkAgainstSealed` still returns on its first line for every
  real module credential. `BindExecution` is now REACHABLE and is not REACHED.

  The reason is a contract, not an oversight: the mint authenticates with
  `req.GetSecret()`, a shared secret, and a secret cannot be execution-bound in
  principle — it says "I know the secret", never "I am this pod running this
  image". Reaching the check means replacing that credential with a projected
  service-account token on the request message, which is a proto change and a
  consumer-visible break. Until that lands, every consequence listed above is
  still a consequence.

  This bullet has now been wrong twice in opposite directions. It first asserted
  the whole model in the present tense while none of it existed. It was then
  corrected to "the live half is built, the sealed half is partly built" — true
  of the code and still misleading about the deployment, because "partly built"
  invites a reader to assume some sealing happens. None does.

  What follows separates what the code does from what it is still to do, and the
  separation is load-bearing rather than cautious.

  **Both halves bind, and neither alone is enough.** Re-reading alone means a
  widened grant, or a database restored to a broader state, retroactively widens
  a credential already in flight. Sealing alone means a narrowing does not take
  effect until the outstanding credential expires. So the effective authority is
  the intersection, which is strictly stronger than either.

  **What is built.** Every module capability path resolves its authority through
  `Service.AuthorizeModuleCapability`, which re-reads the live installation, its
  revision and the principal's producer epoch in **one** statement — three
  separate reads would let a revoke land between two of them and produce a
  decision describing a state that never existed. The revocation predicate is a
  conjunction and each term closes a different hole:

  - **installation freshness**, without which an uninstall-and-reinstall produces
    a new installation an old credential would satisfy;
  - **producer epoch**, without which a narrowing revokes only the credential
    being held and the replacement is reminted with the old authority — so the
    narrowing undoes itself after one credential lifetime;
  - **binding revision**, for an operation context, resolved by
    `Service.ExactOperationBinding`.

  A host that *cannot* re-read refuses every capability (`FailedPrecondition`)
  rather than falling back to the declared ceiling. A read that *fails* is
  `Unavailable` — neither a denial nor an allow, because a database blip is not
  mass revocation and an allow would be the check not running.

  **The exact binding lookup exists; one call site still searches, and it is
  named here rather than glossed.** `Service.ExactOperationBinding` resolves the
  one binding a credential named, by id, and refuses a capability that names none
  rather than falling back — an optional exact lookup is a search with extra
  steps, so the fallback is the whole defect.

  But `moduleOperationContextStale` (`module_operation_context.go`) **still
  searches**: it iterates `grant.OperationAudiences` and accepts if **any**
  binding's `headless_scopes` contain the presented set. So a context minted
  against a narrow binding is satisfied by any **wider** binding the principal
  also holds, and narrowing one binding achieves nothing while a broader one
  survives. That is the defect both reviews named.

  It cannot be routed through the exact lookup yet, and the reason is structural
  rather than effort: the staleness check has no binding id to look up, because
  the credential does not carry one. Sealing the binding id is the Work Context
  cutover, so this site is **gated on condition 2**, not pending on condition 1.
  Until then, `operationScopesSubset` there is bounding-by-search and the
  narrowing it misses is real.

  **Deferred work is re-checked when it runs**, not when it is enqueued, because
  "at use" has to mean at the moment of use. A producer stamps
  `DeferredWorkAuthority`; a worker re-checks it and revoked work fails
  **terminally** (`ErrDeferredWorkRevoked`) rather than retrying — the authority
  will not come back by waiting. An unreadable authority stays retryable, because
  nothing was decided.

  **Absence is not zero, and this is the trap.** `SealedModuleAuthority`'s fields
  are pointers, so a credential carrying no claim about a term is skipped rather
  than compared against zero. "Treat missing as zero" is the shorter
  implementation, it looks exactly like a check, and it admits every unsealed
  credential — which is all of them until the Work Context cutover. Core's own
  seal made the same change for the same reason: `build_incarnation` and
  `image_digest` became optional so "bears no execution" and "bears execution
  zero" stopped being the same bytes.

  `TestEveryModuleCapabilityPathReReadsLiveAuthority` in `module/tools` is what
  makes "every path" true rather than aspirational: it walks the AST for any
  function calling `moduleGrant` directly and holds the set against a
  **shrink-only** baseline, now empty. Its non-vacuity check is anchored on the
  enforcement rather than on the violations — zero direct callers is the goal, so
  deleting the wrapper must not satisfy it.

  **What is NOT built, and so must not be relied on.** The credential seals
  identity and tenant only: the mint does not yet populate the installation id,
  revision, epoch or binding fields, so in a running deployment every
  `Sealed` is nil and the intersection is the live half alone. That is sound for
  narrowing and silent about widening, which is strictly better than before and
  strictly weaker than the target. The intersection also does not yet cover
  queues, resources, namespaces, external publication and audiences; only the
  three authority terms above. Those are gated on the Work Context cutover to
  core's `workcontext`, tracked on #952 / PR #953 as Lane 3 condition 1.

## Establishing what a caller is running

`Service.BindExecution` answers what a caller IS RUNNING, from sources the
caller does not control. The mint authenticated with a **shared secret**, which
is a bearer token — whoever holds it is the module — so it could not answer this
at all. A secret cannot be execution-bound in principle: it says "I know the
secret", never "I am this pod running this image".

**Three digest types, and conflating any two defeats the check.** They are
separate Go types rather than three strings for exactly that reason:

| | what it says | who can write it |
| --- | --- | --- |
| `ApprovedDigest` | what the authority document approves | signed, out of band — the caller has no influence |
| `DeclaredDigest` | what the pod **spec** asks to run | whoever can create the pod |
| `RunningDigest` | what the container **status** reports, as `imageID` | the kubelet |

The check is **approved == running**. Comparing approved against declared is the
classic defeat: a pod spec can name an approved image and run something else if
the tag moved, and the comparison passes while the workload is unapproved.
Comparing declared against running proves only that the pod got what it asked
for, which says nothing about whether it was allowed to ask. `DeclaredDigest`
exists with nothing compared against it so that a reader reaching for "the pod's
image" has a name for the thing they must not use.

**Three independent sources, so no one of them can satisfy the check.** Filling
both sides from the same place — approved digest and running digest both out of
the authority document — compares a value to itself and passes for every caller.
Here the approved digest comes from the signed document, the running digest from
the Kubernetes API, keyed by a pod UID that came from a TokenReview of a token
the caller could not forge.

**The order is load-bearing:** authenticate, then read the pod *the token named*,
then compare. Reading the pod first would mean reading a pod the **caller**
named, which is a caller-supplied input; the token is what makes the pod
reference trustworthy. The audience is `accounts`, so a token minted for the API
server's own audience authenticates the same service account while having been
issued for something else.

**The UID is what is compared, not the name.** A pod name is reused across
generations of a workload and the UID is not, so a replacement pod at the same
name is refused.

**Two answers that must not be confused.** `ErrExecutionNotApproved` is a verdict
about the caller. `ErrExecutionUnbound` means the host could not *tell* — an
unreachable API, a container still pulling, no reviewer wired — and **nothing is
issued**, because a mint that proceeded would issue an unbound capability exactly
when binding was impossible. A legacy non-bound token is **refused**, not
skipped: skipping would make the check opt-out by presenting an older token,
which is the easiest possible bypass.

**A tag is never an approval.** The comparison is on the digest portion and is an
exact match on it — not a suffix test (`HasSuffix` passes when the approved
digest is a suffix of a longer hex string) and not a `sha256:` prefix check
(which would let any repository satisfy an approval granted for one image). A
value with no digest never matches, because an approval is only ever a statement
about immutable content.

### The monotonicity contract, which is this host's to keep

Core's `SealSource.ApprovedBuild` states the rule and says plainly that it
compares values for **equality** and cannot detect a source that moves
backwards. `MonotonicApprovedBuilds` enforces both clauses:

1. for one principal, the incarnation never **decreases**;
2. a change of **digest** comes with an **increase** in the incarnation.

Clause 2 is the one core's own `MemorySealSource` missed: approve B at
incarnation 5, then approve A again at 5, and every capability sealed to A at 5 —
which the move to B revoked — verifies again. A swap-back at a fixed counter is
the rewind the rule exists to prevent, reached through the field clause 1 does
not cover. A violating write is **refused**, not clamped: clamping would serve a
value the writer did not intend and the writer would never learn. A source that
genuinely must rewind is reconstructing state rather than recording it, and
belongs behind a fresh instance.

**Three states, and collapsing two of them is the dangerous direction:** a record
→ the approved pair; known but no record → `ErrNoApprovedBuild`, and the caller
may act unbound; unknown → `ErrUnknownExecutionPrincipal`, refused. Collapsing
unknown into "bears none" would mint an unbound capability for an identity the
host has never heard of, so an **empty** authority refuses everyone rather than
minting unbound capabilities for everyone.

A principal bearing no approved build binds with **neither** field set, because
core's seal makes `build_incarnation` and `image_digest` optional *and paired* —
a digest without an incarnation is refused by its schema.

**Not wired yet:** no mint calls `BindExecution`, and the approved digests are
not populated from the delivered authority documents, so this is exercised by
tests rather than by a running deployment. `module_operation_context.go`'s
binding search is unblocked by the sealed binding id, which the Work Context
cutover carries and which is still outstanding.
- The module presents that token in `x-codefly-work-context` on every capability
  call; accounts takes the calling principal and its bound tenant **from the
  verified token, never from request metadata**. Work Contexts cap at 15 minutes,
  so a long-running worker re-runs the exchange rather than holding one open.

See [../../WORK_CONTEXTS.md](../../WORK_CONTEXTS.md) and
[../../MODULE_INSTALLATION.md](../../MODULE_INSTALLATION.md) for the other
exchanges that produce one.

## Minting an operation context with no person present

`ModuleCapabilitiesService/MintModuleOperationContext` (`EXPOSURE_INTERNAL`,
brokered by the gateway's `/modules/_operation-context`) is the headless
counterpart of the delegated operation exchange: background work calling another
module's service when nobody is signed in. It authenticates exactly like
`MintModuleWorkContext` — the same identity secret, the same refusal of an
undeclared prefix, the same tenant existence check — and the request adds only a
`binding`, a key of that module's `operation_audiences`.

- The child is addressed to the binding's `audience` and carries **exactly** its
  `headless_scopes`, as both the authority scopes and the module actor's granted
  scopes. Owner and sole actor are the module's service principal; the tenant is
  the declared one. It lives 60 seconds (`business.ModuleOperationContextTTL`,
  the exchanged-operation ceiling) with the idempotent replay policy.
- A binding without `headless_scopes` is refused (`PermissionDenied`); the invoke
  scopes are never reused implicitly. An unproven module is `Unauthenticated`.
  A binding addressed to `module-capabilities` is refused at signing.
- Unlike a module Work Context the scopes **are** sealed: the audience is another
  service that decides from the token alone. Removing a binding's
  `headless_scopes` stops the next mint; an issued child outlives it by at most
  a minute.
- It is sealed with a non-zero authorization revision — a digest of the module's
  whole `MODULE_PRINCIPALS` entry — and `CheckAuthorizationRevision` confirms it
  from that declaration rather than the database: owner a declared module
  principal, its declared tenant, the current entry's digest, only the module
  principal as subject, scopes within one binding's `headless_scopes`. Anything
  else is `PermissionDenied`. Changing the entry revokes outstanding contexts.
- `saas.module.operation_context_minted` records every issuance with the
  binding, audience and each granted `kind:action[:resource]`, written after the
  capability exists and withholding it when the record cannot be committed.

The worked example and the configuration shape are in
[../../WORK_CONTEXTS.md](../../WORK_CONTEXTS.md#operation-contexts-with-no-person-present).

## Source delegations: a sync runs with the connecting person's authority

A datasource source's sync runs as a module task with nobody signed in, but it
must act in the source's organization with authority traceable to the person who
connected the source — never with the module's own. `source_delegations`
(migration `9_source_delegations`, tenant RLS, revoked never deleted) records
that authority (`pkg/business/source_delegation.go`):

- **Recorded at connect.** `AddSource`, `AddGitHubSource`, a replacement
  credential on `SyncSource`, and `MigrateGitHubSourceToApp` record, in their own
  transaction, one delegation per module whose one binding declares
  `source_delegation_scopes` — the declaration is the whole opt-in — only
  when the actor is an owner or admin *of the organization* (a platform
  operator's bypass records none). A reconnect revokes the previous one as
  `replaced` and records a new one under the reconnecting person, atomically.
  Existing sources are not backfilled.
- **Revoked on the event, re-checked on every mint.** `DeleteDatasourceSource`,
  `RemoveOrgMember` and a demotion through `AddOrgMember` revoke in their own
  transaction; the mint and the revision check re-read the source, membership,
  role, account status and binding anyway, and a mint that finds one false
  revokes the row with the reason and refuses.
- **The mint.** `ModuleCapabilitiesService/MintSourceOperationContext`
  (`EXPOSURE_INTERNAL`, brokered by the gateway's
  `/modules/_source-operation-context`) authenticates like
  `MintModuleWorkContext` and takes a `delegation_id` or a `source_id`. The child
  is owned by the person, actored by the module principal (the hop carries the
  delegation id), in the source's organization, addressed to the binding's
  audience with exactly its delegation scopes, for 60 seconds. Its revision
  (`business.SourceDelegationContextRevision`) binds the delegation, the binding
  digest and the person's authorization revision, so `CheckAuthorizationRevision`
  — which recognises a person-owned context with a module actor and confirms it
  from the delegation rather than the row-backed agent path — stops confirming it
  once any of them moves.
- **The delegation authorizes the organization; `cross_tenant` is never
  consulted.** Recording, the mint, the revision check and the exchange ignore a
  module's `tenant` and `cross_tenant`: a module bound to one tenant mints for a
  delegation in another, a `cross_tenant` module gains nothing, and a principal
  gains no cross-organization reach from a delegation beyond the one binding it
  names.
- **Exchange, by parent token.** `ExchangeDelegatedOperationAudience` of a parent
  whose actor hop is a declared module principal carrying a delegation id admits
  the parent's tenant by re-checking that delegation
  (`ConfirmSourceDelegationParent`), not by the caller's `authorizeTenant`; the
  parent must still be addressed to the caller and the child is attenuated to the
  caller's binding. A parent without such a hop, and every read exchange, keep
  `authorizeTenant` unchanged. Exactly one actor hop is accepted: a delegation
  grants to one actor, and an audience exchange appends none, so a deeper chain
  is a shape the delegation never granted and is refused.
- **Exchange, by grant reference — how work outlives a Work Context.** The same
  RPC takes `delegation_id` instead of a parent
  (`AuthorizeDelegationReferenceExchange`). Two fields, not a `oneof` — moving a
  published field into one is a breaking contract change `buf breaking` refuses —
  with a message-level validation rule requiring exactly one, so setting both or
  neither is `INVALID_ARGUMENT` before a handler runs. Nothing is presented and nothing need
  still be valid: the delegation is re-read and re-checked against current facts
  on every call, and a fresh 60-second child is minted from it. A task running
  for an hour therefore holds an identifier rather than a capability, and renews
  simply by exchanging again — there is no window in which it must still hold a
  valid token to obtain the next, which is what made long work impossible before.
  A revoke lands on the next call; work already in flight is not recalled.

  The reference is not a bearer capability because **only the module the
  delegating binding names as its audience may present it** — the delegating
  module itself cannot, and an id learned by anyone else authorizes nothing. That
  is the same link the parent-token arm enforces through the parent's audience.
  `binding_id` is the caller's own binding; the child's actor scopes are that
  binding's invoke scopes, or its read-only lookup subset, intersected with the
  delegation's scopes (`intersectOperationScopes`), and a lookup the delegation
  does not cover is refused rather than issued empty. The capability is otherwise
  identical in shape to a mint's — the actor hop is the *delegating* module's
  principal, not the caller's — so the revision check confirms it unchanged.
  `ExchangeDelegatedReadAudience` deliberately has no reference arm: a source
  delegation authorizes operation bindings only.
- **Codes.** `FAILED_PRECONDITION` + `DELEGATION_MISSING` (the source has no
  active delegation: reconnect), `PERMISSION_DENIED` + `DELEGATION_REVOKED` or
  `DELEGATION_INVALID` (indistinguishable from absent), `UNAUTHENTICATED` for an
  unproven module. The reasons are `google.rpc.ErrorInfo` under
  `accounts.saas.codefly.dev`, a wire contract the gateway pins too: it answers
  `412` with body `DELEGATION_MISSING`, `403` with body `DELEGATION_REVOKED` or
  `DELEGATION_INVALID`, and a bare `403 forbidden` for any other denial.
- **Administration.** `DatasourceService/ListSourceDelegations` and
  `RevokeSourceDelegation` (`TENANT_REQUIREMENT_ORG_ADMIN`, like the other
  datasource administration) show and end an organization's delegations.
- **Audit.** `saas.datasource.delegation.created`, `.used` (v2 — every mint,
  reference-backed or not, with `lookup` saying whether the capability could
  produce the effect or only recover its receipt) and `.revoked` (with `reason`).
  Every exchange attempt also emits `saas.module.delegated_audience_exchange`
  (v2), carrying `delegation_id` when a reference was presented. `owner_principal_id`
  is no longer required on that type: a parent that does not verify and a
  reference that resolves to nothing both name nobody, and requiring it would have
  dropped exactly those refusals.

The worked example is in
[../../WORK_CONTEXTS.md](../../WORK_CONTEXTS.md#operation-contexts-from-a-source-delegation).

## Subject visibility is a projection, not a module's own vocabulary

A module that enforces row visibility by owner — one row belongs to one subject,
and a viewer reads it only when some relationship says they may — has no subject
vocabulary and no hierarchy of its own. *Why* one subject may see another's rows
is a question about a hierarchy, and the hierarchy is the host's. A module that
grew one would be holding a permission vocabulary; two modules would hold two,
and a tenant would then have two answers with no way to tell which is
authoritative.

`ModuleCapabilitiesService/ListSubjectVisibility` is the host's answer:
`(tenant, viewer_subject_id)` in, the whole set of other subjects whose rows that
viewer may read out.

- **The projection is the team tree.** `teams` is the one strict tree the host
  keeps (`parent_team_id`, materialized `path`, unique per org, never
  re-parented). A viewer may see the rows of every subject in a team at or below
  a team the viewer belongs to. Visibility runs **down** the tree only, and a
  viewer in no team is granted nothing — fail-closed.
- **The whole set comes back from one transaction, and that is the contract.**
  It is deliberately not paginated. The consuming operation is a bulk replace of
  a viewer's whole set, so a set assembled from pages read in separate
  transactions can carry an entry revoked between two of them: the module would
  reinstate an authority an administrator had already withdrawn, and with no
  invalidation signal (below) the stale grant would stand until the consumer's
  next refresh. A tenant whose hierarchy puts more than
  `business.ModuleSubjectVisibilityMaxSet` subjects under one viewer is refused
  with `FailedPrecondition` — a legible failure an operator can act on — rather
  than answered with a set that was never true at any instant.
- **The viewer is never in their own set.** Seeing one's own rows is ownership,
  not a grant from the hierarchy, and the consuming module's own read predicate
  is what admits it. Including the viewer would also make the set's size depend
  on whether they happen to be in a team at all.
- **`expires_at` is the grant's own end, and today it is always unset.** Team
  membership carries no end of its own, so every entry is open-ended. The field
  is the contract, not a placeholder: a consumer evaluates the instant at the
  moment of the read — an as-of read travels in data time and never restores the
  authority that held then — so the host writes an instant rather than expiring
  entries on a timer.
- **Authority is the caller's principal, its bound tenant, and the viewer's
  membership of that tenant.** There is no `MODULE_PRINCIPALS` key for it: like
  notify, approvals and audit, it is a capability any declared module holds on
  the tenant it is bound to. The membership check is what stops a module bound to
  one tenant from using another tenant's subject as a probe.
- **The host publishes no subscribable hierarchy-change signal.** `saas.team.*`
  is in the event catalog but sits in the reserved platform audit namespace,
  which `Subscribe` refuses to a module principal, so a consumer chooses its own
  refresh — per read, or cached against a TTL it accepts.

## Mesh reachability is the composition's to grant

A composed module reaches the internal tier on the named `authority` endpoint
(gRPC, module visibility, a listener of its own), which serves only
`business.ModuleAuthorityProcedures` — see
[../../INTERNAL_TRANSPORT.md](../../INTERNAL_TRANSPORT.md). Widening that list
widens what every composed module can reach, so
`business.ValidateModuleAuthorityProcedures` runs in the catalog and deployment
generators and refuses an entry that is neither on the capability surface that
authenticates the calling module from its Work Context nor one of the two
declared read-only oracles. A method that authorizes on the shared perimeter
credential alone and mutates state therefore cannot reach every composed module
through a one-line edit to the list.

The generated `AuthorizationPolicy` allowlists accounts' internal surface to the
service accounts of services that **declare a dependency on one of accounts'
private endpoints** in the workspace topology, and admits a caller that declared
the `authority` endpoint and no private one to the module surface alone — a
tenant-surface edge beside it changes nothing. This module's own topology names
no composed module — it must
carry no build-time knowledge of its consumers — so a composed module reaches the
capability surface in a mesh-enforced deployment only when its own workspace
declares that dependency and regenerates the policy. A valid Work Context does
not substitute for it: mTLS refuses the call before any token is read.

The transport underneath — h2c rather than server TLS, the resolved origin, and
the key set a consumer verifies host-signed credentials against — is
[../../INTERNAL_TRANSPORT.md](../../INTERNAL_TRANSPORT.md).

## A datasource source is refreshed by three things, and one of them covers every provider

Ranked by latency, a source's content is refreshed by a push (near-live), by a
person pressing "Sync now", and by the **periodic reconcile**. Only the last of
the three is guaranteed to exist, so it is what makes "we sync on push" honest:
without it, a missed webhook is permanent and nothing ever notices.

- **The sweep is the whole schedule, not GitHub's.** `RunDatasourceReconcile`
  runs on a one-minute ticker in `work.go`, selects every active source whose
  `next_reconcile_at` has elapsed, and routes each to the engine its provider
  syncs on: GitHub to a conditional reconcile on the delivery queue (the job
  resolves the head and snapshots only if it moved), everything else to the same
  sync request "Sync now" produces. It used to select GitHub alone, and a pull
  source was never given a `next_reconcile_at` either — so an api, crawler or
  object-storage source synced when a person pressed the button and at no other
  time. None of the three has a webhook receiver, so there was no push path to
  miss.
- **The interval is per provider.** 30 minutes for GitHub; a day for the pull
  providers, whose connectors re-send their whole content on every sync (the gap
  registered against each in `datasource_connectors.go`, detailed per clause in
  [pkg/datasource/connector/CONFORMANCE.md](./code/pkg/datasource/connector/CONFORMANCE.md)).
  `datasourceReconcileInterval` is the one function that answers it, and
  migration `14_datasource_pull_reconcile_schedule` backfilled the rows written
  before there was a schedule to write.
- **The sweep holds no lease and runs in every replica.** What makes that safe is
  the job's idempotency key, which is the source paired with the schedule instant
  it is due on (`scheduledReconcileKey`), so two replicas over one due row
  enqueue the work once. Pull syncs also carry a per-source FIFO ordering key, so
  a scheduled full re-send cannot run beside a manual one.
- **Every provider syncs at connect**, not a whole interval later
  (`startFirstSync`).

### Whether live delivery exists at all is a deployment fact, and the host reports it

`Datasource.webhook_configured` reports one thing: a signing secret is stored
against that source. It is not the answer to "does a change here reach us
quickly", and reading it as one has been actively misleading in both directions:
an App-backed source holds no secret of its own — its pushes arrive at the App's
single webhook URL — so every source on the recommended connect path read
"not configured", and a source that does hold a secret receives nothing where
`DATASOURCE_GITHUB_WEBHOOK_ENABLED` left the per-source receiver unmounted.

`Service.LiveDeliveryFor` composes the source fact with the deployment fact and
serves the answer as `Datasource.live_delivery`; `DatasourceProviderDescriptor.live_delivery_configured`
is its catalog-level companion ("has an operator wired this connector's push
endpoint here"), distinct from `supports_webhook` ("could this connector take
one at all"). `work.go` sets the receiver-mounted switch from the same place it
mounts the route, so the two cannot drift, and logs the whole posture once at
boot — a deployment with neither endpoint wired used to log nothing at all.

### A public source that stops being public

Connecting refuses a repository GitHub will not serve unauthenticated, so a
credential-less source is proof the repository was public at connect. Nothing
re-asked afterwards, and the generic classifier called the resulting 404 a
retryable failure "inaccessible to this PAT" — for a source that holds no PAT.
`parkUnreadablePublicSource` makes it terminal, parks the source with
`DatasourceReasonPublicRepositoryUnreadable`, and records
`saas.datasource.source.access_lost` with the `public_repository_unreadable`
cause (v2 of that type; `datasourceAccessLostCodes` is the whole vocabulary a
registry test holds the declaration to). Recovery is the ordinary one — a later
snapshot clearing the degrade — which a repository made public again, or
reconnected with a PAT or through the App, all reach.
