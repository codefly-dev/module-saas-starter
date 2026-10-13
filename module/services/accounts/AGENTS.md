# AGENTS.md — accounts

Owns identity, tenancy, permissions, approvals, audit, jobs and the durable
records behind them. Authorization is the subject of [AUTHZ.md](./AUTHZ.md),
[AUTHZ_MATRIX.md](./AUTHZ_MATRIX.md) and
[../../AUTHORIZATION_CATALOG.md](../../AUTHORIZATION_CATALOG.md); every table's
scope and RLS posture is in
[../../DATABASE_AUTHORITY.md](../../DATABASE_AUTHORITY.md). This file covers the
registration and module-identity records that only accounts may write.

**Regenerating after a proto change** is one command from this directory,
`codefly generate proto --proto ./proto --path saas --output .. --template
accounts/proto/buf.gen.yaml` (Docker, Codefly CLI ≥ 0.1.160), then the
`go generate` steps. `codefly sync service accounts` also runs the agent-owned
regeneration; `buf.gen.yaml` limits its input to the owned `saas` tree so the
vendored Core options never become duplicate Go descriptors. The `--output . --local --template buf.gen.local.yaml`
spelling that older docs and the `generated-pins-gate` message still show no
longer works. The full procedure and why are in
[../../REST_SURFACE.md](../../REST_SURFACE.md#regeneration).

A composed module's REST routes belong to the gateway's generated and explicit
catalogs. Its pod's projected ServiceAccount token authenticates the Work
Context exchanges below; no secret does.

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

The principal directory lists stored identities. The module principal registry
below supplies declared authority to capability checks only.

See [../../SOLUTION_REGISTRATION.md](../../SOLUTION_REGISTRATION.md#7-declared-presence)
for the trust model, field ownership and delivery configuration.

## The solution's runtime boundary is derived from its delivered binding

A runtime task is reachable only under the boundary of the Work Context that
admitted it — the context's `task_id`. A solution's boundary is derived from the
**delivered presence binding** (`solution_registrations.declared_binding_id`), so
a run a page admits stays reachable across the mints of one session rather than
only under the one context that admitted it.

**The binding id is the seed because it has the lifetime a boundary needs.** It
survives a withdrawal and a re-approval, and it is terminal with its tombstone.
The seed used to be `solution_registrations.runtime_boundary`, a
per-REGISTRATION `gen_random_uuid()` from migration
`17_solution_runtime_boundary` — the wrong lifetime twice over: a solution
withdrawn and registered afresh drew a new value and orphaned every run filed
under the old one, while a binding that never moved could have its value replaced
by a write it did not make. **Migration 28 drops that column and its UNIQUE
constraint**, and `business.SolutionRegistration` carries no boundary field: a
field no store can populate would read as "this solution has no boundary"
wherever it was found empty.

**The boundary is derived per organization**, not stored: a UUIDv5 of a namespace
derived from the binding id, then of the org id
(`business.SolutionRuntimeBoundary`). A run is filed under (tenant, boundary), so
a single per-solution value would make every tenant of a solution share one. Two
steps, because a binding id is not a UUID — it may carry characters a path
segment may not, so `uuid.Parse` on it fails. `solutionBoundaryNamespace` is a
constant of this host and not a secret: unguessability comes from the per-org
derivation and from the boundary never appearing on a readable record.

**Nothing above this service may choose one.** `declared_binding_id` is written
only from the delivered declaration, never from a registrant's request, so the
boundary moves when the host's own reconciliation moves it and at no other time.

**No response carries a boundary.** `solutionRegistrationProto` sets no such
field on any path, and there is no field to set: field 9 is reserved on
`saas.accounts.v1.SolutionRegistration` and
`TestSolutionRegistrationResponsesCarryNoRuntimeBoundary` asserts over the
DESCRIPTOR, so reintroducing the field fails rather than only a value happening
to be empty.

**The mint** reads the binding through
`business.SolutionRuntimeBoundarySeedStore`, which also reports the publisher of
record and whether the backend half is serving, and refuses a missing record and
a tombstone separately. A row with no `declared_binding_id` answers the
tombstoned error: the cutover's own constraint makes an undeclared row a
withdrawn one. **Serving is delivered presence**, not an asserted lease — a
non-tombstoned declared row whose applied generation carries a backend half
(`backend_revision` and `backend_upstream`) — and the refusal names both columns.
Revocation reaches a capability through that per-mint read, under the existing
120 s cache bound: carrier deletion is not revocation, the applied tombstone is.

Which solution is asking comes from `auth.VerifiedSolution`, and **nothing on the
wire sets it**: a request carrying `X-Codefly-Solution-Id` or
`X-Codefly-Solution-Publisher` behind a valid gateway token is **refused by
name** (`errSolutionAttestationNotDelivered`) rather than believed, because this
host has no delivered attestation to check such a claim against; without a
gateway token the headers are stripped. Either header alone is a claim.
`WithVerifiedSolution` is kept as that attestation's landing site and has no
non-test caller, which `TestNoProductionCodeSetsAVerifiedSolution` holds. So
solution-scoped minting does not work on this host until the attestation lands —
stated as the follow-up rather than left to be discovered. Before deriving
anything the mint checks the publisher equals the credential's and the backend
half is serving — the half that mints — and a caller-supplied `task_id` is
**refused**, not ignored.

**And every other mint refuses a `task_id` that is any solution's boundary.** A
boundary is not a secret a consumer keeps: a consuming module that serves
durable runs reports, on a run it lets a person read, the Work Context task it
was admitted under, so one read any caller is entitled to would otherwise hand
them a stable value to name on an ordinary mint. `StartTask`'s ordinary path and
`StartInstallationTask` both run `refuseRegisteredBoundary`, which compares the
caller's `task_id` against every stored binding id and the boundary derived from
each for the organization named in the request — tombstones included, because a
removed solution's runs may still be executing. It fails **closed**: a registry
that cannot answer refuses the mint, because the capability cannot then be shown
not to be a solution's. Otherwise mints are unchanged: a request with no
verified solution must name its own `task_id`, exactly as the schema used to
require.

## The platform Catalogue is a projection, not a registry

`PlatformAdminService.ListPlatformCatalogue` (super administrators, page
`/admin/platform/catalogue`) lists every composed module and every solution
with what it declares, what runs and where it is installed. It **adds no store**:
modules come from the module principal registry (`Service.modulePrincipals`),
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
browser-facing projection of the registry does; there is no runtime-boundary
field for it to withhold.

## The composed-module service principal

A module consuming the module-facing capability surface
(`ModuleCapabilitiesService`: job enqueue/claim, notify, approvals, audit,
events, subject visibility) calls it as its own **service principal**, whose id
is derived from the
declared module prefix (`business.ModulePrincipalID`) — nothing is
hand-authored as an opaque id.

Its authority is its entry in the **module principal registry**, which no
configuration declares. The module principal reconciler derives the whole
registry on every reconcile pass (and on a delivery or an envelope write,
without waiting for the tick) and installs it tagged with the envelope revision
it was derived from — the only production writer — so installing, narrowing or
withdrawing a module takes effect with no restart. An entry exists only while
three inputs agree:

- **the module envelope** — the authority ceiling a platform super_admin writes
  through `PlatformAdminService/PutModuleEnvelope` (`requirePlatformRole`, so
  never through an impersonated session; audited as
  `saas.platform.module_envelope_written`; append-only and revisioned in
  `module_envelopes`; fresh MFA required, an unenrolled administrator refused).
  Per module prefix it lists the WHOLE authority units the host admits (id
  `<presence binding>:<binding>:<operation>`, revision, scope, queue, namespace,
  binding key, lookup method — the audience is the grant's), the workload
  identities the principal may run as (SPIFFE ID and container), the scope
  ceilings, and the grant ceiling below.
- **a live authority document inside that envelope** — signed, delivered,
  re-verified on every pass against the host's CURRENT signer-to-domain policy
  (a document that no longer verifies is refused by name), over a presence
  binding no ADMITTED tombstone in the whole inbox history ever withdrew — one
  addressed to this host and speaking for the binding's OWNER, the domain of
  the presence generation this host admitted and applied (never a delivered
  row core's admission refused); a withdrawal is terminal for the
  binding, whatever later generation or authority id follows, and a foreign
  tombstone is refused at receipt and ignored in history — and held by core's
  `ValidateAgainst` to the envelope's
  own units (exact containment: a unit that differs from the admitted one in any
  field puts the whole document outside, and it grants nothing).
- **the applied presence document** the authority is granted over — the workload
  running the approved build gives the entry's `workload` (namespace and service
  account from its SPIFFE ID, read by the one strict parser
  `ParseKubernetesSPIFFEID`, container from the presence), and only when the
  envelope allows that exact identity, so a later presence generation cannot
  move the principal to another workload. One workload (namespace and service
  account) maps to exactly one principal: the envelope refuses an overlap when
  written and the derivation omits it.

The entry is the envelope's grant bounded by the document: queues and namespaces
the document also declares, resources its scope ceilings name, and each
operation audience only for a binding whose `invoke` unit the document grants,
every operation's scopes intersected with the unit's scope. A principal granted
by two live documents, whose workload is not allowed or has no single answer,
or whose entry fails validation is **omitted by name, never widened**. Within an
authority id the newest generation is the answer (the inbox is append-only);
across ids a principal moves only to a document inside the envelope.

**The registry is fenced by the committed envelope revision.** Every module
authority decision — `AuthorizeModuleCapability`, the work-context and
headless operation-context mints, the source-delegation mint, and the
revision confirmations behind `CheckAuthorizationRevision` — takes its grant
from ONE registry snapshot, reads the committed revision from the store (once
per request under `WithModuleEnvelopeRevisionMemo`), and refuses with
`ErrModuleRegistryStale` (Unavailable, a pass requested) unless that snapshot
was derived from exactly it; a replica that has seen a newer revision serves
nothing from the older registry. Every mint, including the installer token route,
passes that one capture into `BindExecution` and the grant resolvers: principal,
workload owners, declared container, tenant and operation/source grants cannot
come from different generations. Source mints still take their tenant from the
person's delegation, checked against the captured module grant. After binding,
and again after signing and audit recording, `FenceModuleMint` reads the committed
revision afresh, bypassing the request's earlier memo. A registry replacement or
a different committed revision withholds the capability with `Unavailable` (HTTP
503 on the installer route); the final fence is the mint's release decision.

A pass reads the withdrawal history and the envelope first; when another input cannot be read it never combines fresh and
stale inputs, and publishes a DEGRADED registry — the last published entries,
kept only where still derived from the current revision, not withdrawn, their
presence not withdrawn by delivery, and their signer still admitted — or an
empty one when the history itself cannot be read. The credential's tenant must be the current
grant's (unless `cross_tenant`), so moving a principal's tenant revokes
credentials sealed for the old one at the next check. The grant ceiling's keys:

| Key | Grants |
| --- | --- |
| `queues` | enqueue and claim |
| `namespaces` | event publish |
| `resources` | the permission resource types its own content is governed by, bounding both the content reads this host authorizes for it and the records it may place at a scope node |
| `read_audiences` | installed read-only bindings from a retained parent to a fixed audience and canonical scopes |
| `operation_audiences` | installed operation bindings with fixed audience, canonical invoke scopes, a read-only lookup subset, optional `headless_scopes` (a subset of the invoke scopes) that alone may be minted with no person present, and optional `source_delegation_scopes` (a subset of the invoke scopes, on at most one binding) that a person's connect of a datasource source delegates |
| `tenant` | the org it is bound to |
| `cross_tenant` | an inbox worker serving every tenant |

**No envelope written, or no delivered authority inside it, means no module may
call the surface** — accounts starts with an empty registry and serves no module
until its first pass.

### A non-member person is not a refusal of the module

Every refusal of the **caller's authority** on this surface is
`Unauthenticated` or `PERMISSION_DENIED` and names no reason: an identity it
cannot prove, a prefix this host does not admit, a tenant it is not bound to, a
queue or namespace outside its grant, and a procedure the authority endpoint
does not serve — the last enforced twice, by the mesh `AuthorizationPolicy` and
again in-process by the interceptor (`grpc_auth_interceptor.go`, "RPC is not
exposed on the module authority endpoint"), which is also where a missing or
wrong internal credential is refused. A malformed request is `INVALID_ARGUMENT`
whatever the caller's authority (an unknown notification category or type, a
destination that is not a same-origin path). A **person** the surface is asked
to act on who is not a member of the named tenant is `FAILED_PRECONDITION`. That holds on each surface that takes a person:
`NotifyUser`, subject-scoped `EnqueueJob` and `ListSubjectVisibility`.

The split is what lets a module drive the surface from a journal with retries. A
departed person is final for that person and nothing a retry resolves, while a
composition this host does not admit as a module principal, or whose workload
it cannot bind to an approved build, is an operator's to fix and its work is
worth holding; conflated under
one code a consumer has to choose which of the two it silently gets wrong.

**The code alone is not the discriminator — the reason is.** This surface
answers `FAILED_PRECONDITION` to several unrelated facts, so a consumer reads
the `google.rpc.ErrorInfo` reason and never the code or the message text. On
the notify and subject-visibility methods every `FAILED_PRECONDITION` names
one, under domain `saas.accounts.v1`:

| Reason | Fact | Whose to fix |
| --- | --- | --- |
| `TENANT_MEMBERSHIP_MISSING` (`business.ModuleTenantMembershipMissingReason`) | the person named is not a member of the tenant | nobody's — final for that person |
| `NOTIFICATION_IDEMPOTENCY_CONFLICT` (`business.ModuleNotificationIdempotencyConflictReason`) | the idempotency key was used for different content | the calling module's; a retry never clears it |
| `SUBJECT_VISIBILITY_SET_TOO_LARGE` (`business.ModuleSubjectVisibilitySetTooLargeReason`) | the viewer's set exceeds `ModuleSubjectVisibilityMaxSet` | an administrator's, by changing the hierarchy |
| `LIVE_AUTHORITY_UNAVAILABLE` (`business.ModuleLiveAuthorityUnavailableReason`) | this host cannot re-read live module authority, so it exercises no capability at all — refused by `AuthorizeModuleCapability` before any membership read | the operator's; **retain** the work and retry once repaired |

Only `TENANT_MEMBERSHIP_MISSING` is final. **An absent or unrecognised reason is
retainable, never final:** the table is what the surface answers today, not a
promise about every future refusal, so a consumer that reads "no reason I know"
as "done" turns the next refusal added here into silently discarded work. That
is not hypothetical — `LIVE_AUTHORITY_UNAVAILABLE` carried no reason until
#1032, and an unconfigured host was therefore indistinguishable on the wire from
a departed person.

A reason is only meaningful together with the domain the same call answered
with: the `Mint*` exchanges' refusals carry theirs under
`accounts.saas.codefly.dev` (`adapters.SolutionRegistryErrorDomain`, see
[Source delegations](#source-delegations-a-sync-runs-with-the-connecting-persons-authority)),
so matching on a bare reason across the whole service is wrong.

## Minting a module Work Context

The identity itself is a **Work Context**. The auth-gateway brokers the exchange
to `ModuleCapabilitiesService/MintModuleWorkContext` (`EXPOSURE_INTERNAL`). The
module presents its pod's **projected ServiceAccount token** (audience
`accounts`) as the request's bearer, and `ModuleAuthorizeWorkContext` refuses a
prefix that is not a declared module principal, then runs `BindExecution` (see
"Establishing what a caller is running" below): the token is reviewed, the pod it
names is read, and the image that pod is RUNNING must equal the build the
principal's authority document approves. Only then is a capability minted, owned
and actored by the module principal (`aud=module-capabilities`), and a
`saas.module.work_context_minted` audit event (v2) records it with the execution
it was bound to — namespace, ServiceAccount, pod, pod UID, container, running
image, approved incarnation. The response carries
`{token, expiresAt, principalId, tenant}`.

- **No secret proves a module's identity (#1056).** `MODULE_IDENTITY_SECRETS`,
  `MODULE_REGISTRATION_SECRETS` and `SOLUTION_REGISTRATION_SECRETS` are deleted,
  with their parsing, their configuration reads and both registration mints.
- A refusal is a **verdict** (`PermissionDenied`/`Unauthenticated`, the same
  fixed message whichever check failed): a token the API server refused —
  another audience, another cluster, expired — a token for another ServiceAccount
  or namespace than the principal declares, a replaced pod, or a pod running an
  unapproved build. The cause is logged here, never returned.
- A host that could not establish the execution answers **`Unavailable`**: the
  API server is unreachable, no execution binding is wired, or the container
  reports no image yet. Execution binding is wired only with delivered authority
  (`configuredSolutionHostBindingReconciler`), so **a host with no delivery mints
  for no module**, and a module run outside Kubernetes has no token to present.
- A tenant-pinned module keeps the envelope's `tenant`; requesting a different
  organization is refused. A shared module must name `organization_id` and have
  `cross_tenant` in the envelope. Execution proves the principal first; the
  organization's live installation then authorizes the mint. A selector grants
  nothing by itself.
- **Every module credential is sealed.** Accounts mints and verifies Work
  Contexts with `core/workcontext` (`pkg/adapters/work_context_core.go`):
  `StartModuleTask` seals the installation the business mint resolved, its
  revision, the principal's producer epoch and the build the requesting pod is
  running, and `VerifyModuleWorkContext` returns a `ModuleCaller` whose `Sealed`
  carries the first three. So an uninstall revokes a module capability at its
  next use — core refuses it at verification, because its sealed installation is
  no longer active — and so does a reinstall under a new installation id, which
  an unsealed credential could not do. `checkAgainstSealed` still compares the
  live installation against the seal on every capability decision; its nil
  branch serves the host's own reads (a mint resolving what it is about to seal,
  a fence, a revision recheck), never a credential. So the sealed half of
  `min(sealed, live)` now exists for the installation, its revision and the
  producer epoch; a sealed GRANT ceiling on a module's own identity capability
  does not — that capability seals no scopes, and every use reads the declared
  grant live — and neither does a sealed operation binding (below).
  `TestSealedModuleCredentialDBLiveInstallationAtEveryEntryPoint` (`pkg/infra`)
  is the database evidence: enqueue, claim, heartbeat, ack, retry, dead-letter,
  blob and file fetch, both delegated bindings and both artifact decisions refuse
  after an uninstall and after a reinstall.
- The implementation resolves the caller organization's installation status,
  scope, revision, binding and envelope revision in `AuthorizeModuleCapability`,
  before `checkAgainstSealed`. Its use span, `BeginModuleAuthorityUse`, retains
  shared installation, target and envelope locks through the effect, including
  signing and exchange. The separate lock pool does not lend control-plane
  privileges to effects.
- **A module principal's epoch is its authorization revision in the
  organization** (migration 35). Installing a module and every change to its
  installation's revision advance it, so a capability sealed before a scope or
  status change is refused after it.
- Every mint calls `BindExecution`: the projected token is reviewed and its
  running pod/build checked against delivered approval, and the digest it read is
  the execution the capability attests — core holds it against the approved
  build again at the mint. The approved-build view is keyed by the module's
  PREFIX, the name the authority document gives it, never by its registry UUID.
- **The exact binding lookup exists; `moduleOperationContextStale` still
  searches** the current operation bindings for a compatible scope superset.
  Replacing this with `ExactOperationBinding` requires the sealed binding id.
  The required model is an **exact binding lookup**, never a scope search.
- Deferred work keeps producer provenance, and module claims separately stamp
  `job_messages.consumer_authority`, including installation id/revision and org.
  Narrowing/uninstall cancels pending, retrying and processing work carrying
  either authority for that installation and organization, closes attempts and
  clears leases. Consumer stamps survive retries and lease expiry. Ordinary
  replay cannot resurrect canceled work. Heartbeat/ack/nack also require the
  current caller's exact consumer stamp.
- Global datasource jobs retain their existing `datasource.org_id` attribution.
  Claims require every nonempty organization hint to equal the caller's bound
  org. Truly global jobs without an organization hint require the envelope's
  `cross_tenant` grant as well as the queue grant. Neither route admits another
  organization's jobs.

`TestEveryModuleCapabilityPathReReadsLiveAuthority` in `module/tools` holds the
remaining direct registry readers to a shrink-only baseline. Artifact policy
resolution and revocation are no longer exceptions. Database concurrency and
job lifecycle regressions live in `pkg/infra/installation_review_test.go`; the
actual signer/verifier regression is
`pkg/adapters/installation_credential_test.go`.

## Establishing what a caller is running

`Service.BindExecution` answers what a caller IS RUNNING, from sources the
caller does not control. The mint used to authenticate with a **shared secret**,
which is a bearer token — whoever holds it is the module — so it could not answer
this at all; that secret is deleted (#1056) and every module mint calls this.

The approval is looked up by the name the authority document gives the
principal — its **prefix** — never by the registry key
`ModulePrincipalID(prefix)`, a UUID no document carries. A token the API server
reviewed and refused is `ErrExecutionTokenRejected`, a verdict; a review that
could not run is `ErrExecutionUnbound`, not one.

**What a mint requires, in full** (review round 1 on #1056 found each missing):

- **The workload identifies exactly one principal, and it is the one asked
  for.** A token authenticates a ServiceAccount in a namespace, never a prefix.
  The reviewed `(namespace, ServiceAccount)` is resolved over the WHOLE captured
  registry, the same snapshot that supplied the container and tenant;
  zero or several owners is `ErrExecutionWorkloadAmbiguous` /
  `ErrExecutionIdentityMismatch`. Two principals sharing a ServiceAccount mint as
  neither — before, either one's pod minted as both.
- **The pod is one security boundary.** Every container in it can read the
  projected token, so the pod may hold only the authenticating container plus
  init containers that ran to completion. Any other regular container, an init
  container still running or restartable (a native sidecar), and an ephemeral
  (debug) container in ANY state is `ErrExecutionUnapprovedContainer`.
  **The stronger rule this falls short of, and why:** admit every container the
  applied, signed presence document declares for the workload, each RUNNING its
  declared digest. Presence cannot express it today: core's
  `solutionhost.Workload` (v0.17.2 through main) carries the authenticating
  container with its digest and every other container as a NAME only
  (`non_authenticating`), and the CLI renders exactly that. Admitting a sidecar
  by name would let it run any image, so every other container is refused.
  **The cost:** a module pod with a sidecar — a Cloud SQL Auth Proxy, or a
  namespace still on istio sidecar injection — mints nothing until presence
  carries a digest per container (a core field, the CLI render, this check).
  In the cell today the Cloud SQL proxy sidecars are on accounts (the host, which
  mints for no one) and on database Jobs, and delivered namespaces are ambient,
  so no module serving pod is known to carry one.
- **Only a live regular container supplies the digest.** The pod must be
  `Running` with no `deletionTimestamp`, and the declared container must be a
  regular container in state `running`; a stopped container keeps its approved
  `imageID` in status and a completed init container still reports one, and
  neither counts (`ErrExecutionNotRunning`). Not started yet — pod `Pending`,
  container `waiting` — is `ErrExecutionUnbound`: retryable, not a verdict.
- **The token lives at most 600 seconds** (`MaxExecutionTokenLifetime`,
  Kubernetes' minimum `expirationSeconds`), read from the iat/exp of a token the
  API server has just authenticated. A longer one, or one whose lifetime cannot
  be read, is refused.

**Replay, stated precisely.** A projected ServiceAccount token is a BEARER
credential. TokenReview proves the cluster issued it to a named pod and that the
pod still exists; it does not prove the pod is the party presenting it. So:

- **Closed here:** a token from another workload, another audience or another
  cluster; a replaced pod at the same name; a pod running an unapproved build or
  any unapproved container; a stopped container or a pod being deleted; a token
  older than 10 minutes.
- **Not closed — the residual:** a peer that has COPIED a still-valid token
  from an approved, running pod (and holds the broker's perimeter credential)
  can mint as that pod's principal for the rest of that token's lifetime, and
  the mint record attributes it to the victim pod. TokenReview cannot close
  this, and nothing on this host can tell the copy from the original.
- **The strongest closing option — an owner decision, not built:** bind each
  mint to request-origin identity. The mesh's pod-specific mTLS identity of the
  connection that carried the request is conveyed by the broker in a header only
  the broker can set, and accounts requires it to name the same pod UID the
  TokenReview did. A copied token is then useless from any other pod. It needs
  the mesh to issue per-pod (not per-ServiceAccount) identities, and the gateway
  to forward the verified peer, so it is a platform decision rather than a change
  to this mint.

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

**Wired (#1056):** every module mint calls `BindExecution`, and the approved
digests come from the delivered authority documents through
`ApprovedBuildReconciler`. `TestEveryModuleMintIsExecutionBound` (module/tools)
holds the call chain. `module_operation_context.go`'s binding search
still waits on a sealed operation binding: this host seals none into any
capability, and its seal source answers every binding id as absent.
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
`MintModuleWorkContext` — the same projected token bound by `BindExecution`, the
same refusal of an undeclared prefix, the same tenant existence check — and the
request adds a
`binding`, a key of that module's `operation_audiences`, and uses the same
`organization_id` selector as the module Work Context mint.

- The child is addressed to the binding's `audience` and carries **exactly** its
  `headless_scopes` as its authority scopes. It is owned by the module's service
  principal acting directly, with no actor hop (core models an owner acting);
  the tenant is
  the selected installation's organization. It lives 60 seconds (`business.ModuleOperationContextTTL`,
  the exchanged-operation ceiling) with the idempotent replay policy.
- A binding without `headless_scopes` is refused (`PermissionDenied`); the invoke
  scopes are never reused implicitly. An unproven module is `Unauthenticated`.
  A binding addressed to `module-capabilities` is refused at signing.
- Unlike a module Work Context the scopes **are** sealed: the audience is another
  service that decides from the token alone. Removing a binding's
  `headless_scopes` stops the next mint; an issued child outlives it by at most
  a minute.
- It is sealed with a non-zero authorization revision — a digest of the module's
  whole module registry entry — and `CheckAuthorizationRevision` confirms it
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
  delegation id and the build its pod runs), in the source's organization,
  addressed to the binding's audience with exactly its delegation scopes, for 60
  seconds, at the person's authorization revision. The delegation is sealed as
  the capability's core operation binding,
  `source-delegation/<delegation>/<module prefix>`
  (`business.SourceDelegationBindingID`), read live by every verifier through
  `WorkContextOperationBinding`: granted to that module at its installation,
  revoked with the delegation, and answered only for the delegating module or the
  module its binding names. `CheckAuthorizationRevision` — which recognises a
  person-owned context with a module actor — takes the sealed binding id
  (`operation_binding_id`) and confirms it from the delegation rather than the
  row-backed agent path, so it stops confirming once the delegation, its binding,
  the source, the person's role or their revision moves.
- **Delegation and installation are both required at use.** Minting, revision
  confirmation and exchange hold the actor's installation in the delegation's
  organization to the current envelope. `cross_tenant` permits consent in other
  organizations; it never lets one organization's installation grant another's
  data. An exchange's caller is bound to the same organization as its parent.
- **Exchange, by parent token.** `ExchangeDelegatedOperationAudience` of a parent
  whose actor hop is a declared module principal carrying a delegation id admits
  the parent's tenant by re-checking that delegation
  (`ConfirmSourceDelegationParent`) and the actor's installation; the parent
  must be in the caller's organization, addressed to the caller, and attenuated to the
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
  identical in shape to a mint's, except that the actor hop is the **caller's**
  principal — the module that uses the capability, whose build core attests at
  the mint — and the operation binding names the caller; the revision check
  confirms it the same way. A parent-token exchange re-derives the child the same
  way, with the caller on the hop.
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

## Unattended work: whose authority an occurrence runs under

Every admission carries a signed Work Context, and work no person started has
nobody to sign one from. The answer is not a platform identity: a release
declares WHOSE authority its occurrences borrow, in one of two arms, and this
host re-derives that authority from current facts on every single occurrence.
The field names and values are the INSTALLATION DOCUMENT's, not the read API
the same module answers an operator with: it decodes `source` / `binding` and
`"skip"` while reporting `source_delegation` / `installation_binding` and
`MISSED_RUN_POLICY_SKIP`, and only the first parses at install. This host
speaks the document's spelling everywhere, so there is one vocabulary here
rather than two that drift (`business.DeclaredTriggerIdentity`, and
`TestTriggerDeclarationMatchesTheContractFieldNames`, which pins the names AND
the values and records the other column beside them).

- **`source` is the grant reference, and the contract.** It names a
  connected source, and the host resolves the live delegation from it on every
  mint (`MintSourceOperationContext` by source). An identifier rather than a
  credential, re-checked against current facts each time — which is what lets an
  occurrence's run outlive the capability that admitted it, and makes a
  revocation land on the NEXT occurrence rather than behind something held from
  the one before.
- **`binding` is the installation-principal arm, and the first
  proof.** It names one of the release's bindings and mints from that binding's
  `headless_scopes` under the module's own principal
  (`MintModuleOperationContext`). An ordinary token admission, so the run is
  bounded by the capability's own short lifetime — which is exactly why it is the
  proof and not the contract.
- **A composition must declare one of them or neither arm can mint.** The
  binding needs `source_delegation_scopes` for the reference arm,
  `headless_scopes` for the principal arm, and the `audience` the executing
  module admits under. A composition that declares neither leaves both arms with
  nowhere to mint, and the host refuses rather than running the work as itself.
- **A binding may name its own module as its audience, and ONLY for unattended
  work.** `validateOperationAudiences` refused `audience == prefix` outright
  before #1081, because every other operation binding describes calling another
  service. Unattended work inverts that: an occurrence's capability is admitted
  by the module that executes the release — itself, verified against its own
  declared name as the audience. `selfAdmittingBinding` narrows the guard to a
  binding declaring headless or delegation scopes; it does not touch the one
  that matters for identity, since a capability addressed to
  `module-capabilities` is still refused at signing whatever a binding declares.
- **Binding a reference without reconnecting.** A delegation is recorded when a
  person connects or reconnects a source, so a release installed afterwards, or
  a module that declared its binding afterwards, finds no reference to name and
  nothing backfills one. `DatasourceService/DelegateSourceTriggers`
  (`TENANT_REQUIREMENT_ORG_ADMIN`) records it under the calling administrator —
  exactly what a reconnect does, in one transaction, through the same
  `recordSourceDelegationsTx`. It grants nothing new: the scopes are the
  binding's declared ones and every fact is re-checked on every mint.
- **What a reader sees.** `DatasourceService/GetSourceTriggerDeclaration` answers
  the declaration, the references an operator may bind to it, and the children
  minted against them. The children come from `saas.datasource.delegation.used`,
  which is the only record of one: a capability lives about a minute and nothing
  caches it, so a revoked reference shows up as the next occurrence having no
  child at all. The playground is `/admin/platform/unattended-work`.

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
  membership of that tenant.** There is no registry key for it: like
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

`cmd/connect-registration` also generates
`generated/api-contract-surfaces.json`, selected by the module's CLI-owned
`contracts.codefly.yaml` (`schema: codefly/module-contracts-config/v1`,
`surfaces: {accounts: generated/api-contract-surfaces.json}`). The path is
service-relative; no selector belongs in agent `spec`. Released CLI v0.1.185
consumes this inventory, an explicit stopgap until agent/Core metadata reports
the complete served set per listener. It uses Core's API contract service records:
authority is exactly `ModuleAuthorityProcedures`; Connect is the complete
registration catalog, including internal methods. The gateway route catalog
omits those internal methods and cannot supply this inventory. Contract export
retains complete descriptors for schemas but publishes each listener's served
procedures, so Runnable derivation only admits Invoke/Prune and their paired
Lookups on Connect. The cataloggen drift test compares both inventories with
their owners; the Connect registration test checks every catalog method reaches
its mux handler. Regenerate with `go generate ./pkg/business ./pkg/adapters
./pkg/cataloggen` from `code`, then the contract and Runnable commands documented
in [../../CALLABLE_SOURCES.md](../../CALLABLE_SOURCES.md#generation-and-qualification).

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

Two of those three are now the **same admission**: a person's "Sync now" and one
occurrence of the declared schedule both admit `ReconcileSource` under an effect
identity their caller minted (issue #1094).

- **The schedule is DECLARED here and FIRED somewhere else** (issue #1081).
  `business.DeclareSourceTrigger` turns a source's interval into a trigger an
  declared release carries — a five-field cron in UTC, `"skip"` for both missed
  occurrences and overlap, a deadline in nanoseconds, the input every occurrence is admitted with,
  and the identity occurrences run under. This host fires nothing on it:
  `TestNoHostTimerFiresTheReconcileSchedule` walks the AST of every non-test file
  in the service and fails on the names the sweep used, because a host that also
  fired this would be two schedules for one source with the undeclared one
  winning. What left with the sweep: `RunDatasourceReconcile`, its one-minute
  ticker in `work.go`, `enqueueScheduledReconcile`, `enqueueScheduledPullSync`,
  `scheduledReconcileKey` and the due query they read
  (`ListDatasourceSourcesDueForReconcile`).
- **One occurrence admits one host operation, and so does a person's press.**
  `DatasourceService/ReconcileSource` (marked `codefly.runnable.v0.operation`,
  idempotency required, paired with `LookupReconcileSource`) is what the declared
  trigger names, and since #1094 it is literally what "Sync now" calls as well —
  the published kit's `syncSource`, which reached these engines carrying no
  effect identity, is deleted. It routes each provider to the engine that
  provider syncs on: GitHub to a conditional reconcile on the delivery queue (the
  job resolves the head and snapshots only if it moved), everything else to the
  pull its connector has. The enqueue is keyed by the occurrence's
  effect identity, so two attempts of one occurrence are one job — which is what
  the schedule-derived key had to stand in for while two replicas could both
  sweep one due row, and which is now also what makes a person's double press
  one pass. **The host has no notion of an identity being spent**: it returns the
  same receipt for the same identity forever, so "when is my intent new" is the
  CLIENT's question, and the kit answers it from presses rather than from a
  clock. There is no enumerable list of past passes either — a receipt is read
  back by identity, never listed — so a surface claiming to show every pass a
  source ever took would be inventing one.
- **The interval is per provider, and it is the source's own configuration.** 30
  minutes for GitHub; a day for the pull providers, whose connectors re-send
  their whole content on every sync (the gap registered against each in
  `datasource_connectors.go`, detailed per clause in
  [pkg/datasource/connector/CONFORMANCE.md](./code/pkg/datasource/connector/CONFORMANCE.md)).
  `datasourceReconcileInterval` is the one function that answers it; editing it
  re-declares the trigger, because the declaration is derived from it and from
  nothing else, and the declaration digest moves with it.
- **Only intervals a cron can name are declarable, and the rest are refused by
  name.** Minutes dividing an hour, hours dividing a day, and exactly a day;
  anything else (25 minutes, 90 seconds, 5 hours, 2 days) reads back as
  `TriggerUndeclarableInterval` rather than being rounded to a schedule nobody
  asked for. The minute — and, for a daily interval, the hour — is derived from
  the source id, so every source of one provider does not come due in the same
  second.
- **A parked source declares no trigger.** Enablement is the stored
  `next_reconcile_at`'s PRESENCE, never its value: the park, restore and cursor
  paths maintain it, and one reconcile pass moves it so it cannot drift
  arbitrarily into the past. The declaration's own next occurrence is computed
  from the cron it declares; the authoritative cursor belongs to the module that
  fires the trigger, and this host holds none.
- **Every provider syncs at connect**, not a whole interval later
  (`startFirstSync`).
- **What is not proven.** No live occurrence exists. Nothing in this repository
  composes a module that executes declared work, so the declaration is
  built, held to the contract's field names by test, and never yet admitted by
  anything. The identity's two arms are qualified at the routes an executing
  module calls (`pkg/adapters/datasource_trigger_identity_test.go`); the walk
  that has not been run is a schedule coming due in a composed environment.

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
