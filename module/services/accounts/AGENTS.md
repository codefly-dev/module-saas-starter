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

## The one diagnostic the binary answers

`./app diagnose token-exchange` (`diagnose.go`, dispatched first thing in
`doWork`) certifies the identity provider's client authentication: it builds the
provider stack with `buildProviderStack`, exchanges `exchangeprobe.SentinelCode`
through its exchanger — and, when the provider refuses only the code, again with
a random secret of the real one's shape as a negative control
(`WithClientSecretFrom` on both exchangers) —
and prints one JSON line — verdicts, status, OAuth error code — with file
descriptors 1 and 2 on the null device so nothing else reaches the caller.
Callers detect the diagnostic by the sentinel string in the binary, so keep
`SentinelCode`'s value. A check that execs into the pod uses it instead of an HTTP client in
the image; [FEATURES.md](../../FEATURES.md#certifying-the-client-credential)
lists the verdicts. Any `diagnose` argument exits without serving. The
exchangers report a refusal as `auth.TokenEndpointError` (status and code,
never the body), which is what the classification reads.

## Declared solution presence and the registry

A module or solution is present because one signed **module manifest**
(`codefly/module-manifest/v1`, core's `solutionhost.ModuleManifest`) declares it,
per binding. `POST /platform/_delivery/manifest` verifies the carrier against the
pinned keys, reviews the carrier's token — the `delivery` ServiceAccount of the
module's namespace by the cell's rule (`business.ModuleNamespaceRule`, read off
this host's own namespace and `CODEFLY__MODULE`), for a withdrawal the previously
applied manifest's module — admits it with core's `Admit` and `CheckAlias`,
holds it to the host's own rules (`checkManifestAgainstHost`: one workload runs
as one manifest; a principal and a release publisher stay with the ownership
domain that holds them; an endpoint address is its workload's in-cluster Service
by the rule, or the loopback natively), and persists it in the append-only inbox
(migration 41: one kind, `manifest`). It answers 200 `accepted`/`replayed`, 400,
401, 403 (also a principal or publisher another domain holds), 409 (stale,
rewritten, withdrawn, or a workload another binding holds, named), 422 (another
host, a reserved or held alias, an endpoint off its Service) or 503, with the
reason in the body and the log.

Under a native `codefly run` the CLI hands the host the run's anchor
(`CODEFLY_NATIVE_IDENTITY_ANCHOR`) and pins its fingerprint in the local
profile (`local.nativeAnchorSha256`); the host refuses to boot unless they
match, and then `infra.NativeWorkloadIdentity` — core's native attester —
answers the carrier, applied-observer and mint reviews TokenReview answers in a
cell, under the bare namespace rule. The full contract is in
[../../SOLUTION_REGISTRATION.md](../../SOLUTION_REGISTRATION.md#7-declared-presence-one-signed-module-manifest-per-binding).

`solution_host_bindings` stores desired and applied manifests and pending
reasons. Each pass re-verifies the newest delivered manifest per binding, admits
it against the applied one under the binding row lock, and applies it into the
same targets, registrations and generation history as before. A refused or
unreadable delivery does not withdraw the last applied generation; withdrawal is
a `removed: true` generation and is terminal. A withdrawal recorded before
migration 41 cannot be read as a manifest and still refuses every later
generation (core's `ErrWithdrawn`).

`GET /platform/_delivery/applied/{binding}` reads only the committed manifest,
including withdrawals, after an accounts-audience TokenReview of the protected
observer: `{host, binding, generation, digest, release, removed, appliedAt,
pending?}`. A desired delivery receipt is not proof of apply.

`solution_registrations` is the durable read projection, keyed by the route
alias. Every row names its declared binding, generation, release and immutable
target. A binding ID identifies a deployment instance, while an alias identifies
a route; consent names the target and cannot follow an alias to a replacement
instance.

New frontend candidates require complete signed payload preparation before
apply. A failed candidate preserves the applied predecessor. The targeted
retained-frontend read joins exact signed inbox history to a still-live target.
See the prepared-payload section in
[../../SOLUTION_REGISTRATION.md](../../SOLUTION_REGISTRATION.md).

The internal `SolutionRegistryService` exposes the registry and host-binding
reads. The gateway reads the snapshot, parses each applied manifest and routes an
alias to the HTTP endpoint the manifest declares for its own workloads, at the
address the manifest carries. The `endpoint_delivery` and `host_component` proto
fields are no longer populated; their removal is a contract-package change of its
own. Applied modules and solutions are active independently of their optional
frontend block; registry availability does not replace installation status and
consent checks.

An optional `group` on an applied solution manifest is copied to the
registration as presentation only (migration 38). Moving or clearing a group
preserves the installation target and grants; authority never reads it.

Audit type admission remains available through
`ModuleCapabilitiesService.DeclareAuditEventTypes`, under the derived module
authority below.

The module registry every capability check reads is **derived** each pass
(`business.ModulePrincipalReconciler`) from the applied, live manifests whose
stored carrier still verifies and the module envelope a platform super_admin
writes: the manifest's `authority` is the request, the envelope's per-principal
`grant` the ceiling, the entry their intersection, and its workloads the
manifest's `(service, container)` spelled by the namespace rule. The envelope
names no workload and approves no build.

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
record, and refuses a missing record and a tombstone separately. A row with no
`declared_binding_id` answers the tombstoned error: the cutover's own constraint
makes an undeclared row a withdrawn one. **Serving is delivered presence** and
nothing else — a non-tombstoned declared row; where the solution is reached is
resolved by the gateway through the Codefly SDK and lives in no column, and
nothing a running process reports is stored (#1118). Revocation reaches a
capability through that per-mint read, under the registry's 120 s lease:
carrier deletion is not revocation, the applied tombstone is.

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
declaration or an applied generation (they arrive with applied module
manifests), an approval or a withdrawal (the deployment contract's signed
approval, not built yet), an observation or a retirement (nothing observes the
cluster or reports stop/fence evidence), an installation revision, or a build
size (a module manifest carries none, and the host counts no lines itself). So each reads `NOT_RECORDED` or `NOT_OBSERVED`.

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
two inputs agree:

- **the module envelope** — the authority ceiling a platform super_admin writes
  through `PlatformAdminService/PutModuleEnvelope` (`requirePlatformRole`, so
  never through an impersonated session; audited as
  `saas.platform.module_envelope_written`; append-only and revisioned in
  `module_envelopes`; fresh MFA required, an unenrolled administrator refused):
  `{"revision", "principals": {"<principal>": {"grant": {…}}}}`. It names no
  workload, no unit and no build; a document that does is refused by name.
  The composition can also deliver it: `POST /platform/_delivery/envelope`
  admits a release-signed `codefly/module-envelope/v1` carried by the host's
  own `delivery` account, naming no organization, above the committed
  revision, through the same writer (audited as
  `saas.platform.module_envelope_delivered`).
- **an applied, live module manifest asking for the principal** — its
  `authority.principal` is the module's principal, and its stored carrier is
  re-verified on every pass against the CURRENT pins (one that no longer
  verifies grants nothing). Its workloads, spelled by the namespace rule, are
  the entry's `workloads`. A withdrawal is terminal for the binding. One
  workload (namespace and service account) maps to exactly one principal, and
  two manifests asking for one principal are both omitted.

The entry is the envelope's grant bounded by the manifest: queues, namespaces
and resource kinds the manifest also asks for, and each operation binding only
when the manifest asks for it on the envelope's audience with an `invoke`
operation, every operation's scopes intersected with what the manifest asks.
A principal whose entry fails validation is **omitted by name, never widened**.

**A withheld binding is a deployment defect with a surface.** An operation
binding an applied manifest asked for and the pass could not grant — the
envelope allows no such binding, allows it on another audience, leaves it with
no valid audience, or omitted the whole principal — is published beside the
registry it is missing from, and read by two callers rather than being a WARN
nothing reports. The reconcile pass records it as the asking binding's
`pending_reason` (`ListSolutionHostBindings`), the row that already carries why
this host could not take a delivery, and never as a refused generation: the
generation was applied. A connect or reconnect of a datasource source that
would record no delegation while anything is withheld refuses instead
(`FailedPrecondition`, naming the module and the binding), because recording
nothing there is indistinguishable from the legitimate "no composed module
accepts a delegation here" and the person is otherwise told, by the next sync,
to reconnect what they just reconnected.

**No envelope is said by name, every pass.** A pass that finds no envelope
(none written, or no envelope store) publishes an empty registry and, while any
applied manifest asks for authority, returns `ErrModuleEnvelopeMissing` naming
each principal, binding and generation — logged by the reconcile loop as
`solution host binding reconcile pass failed`, beside the omitted-principal
reasons. The mint's refusal of a prefix the registry does not hold logs
`module identity refused` with the prefix and why (the registry is empty and
derived from no envelope, empty, or holds no entry for it), as a refused
execution does; the caller still hears only the denial.

**The administrator writes it at `/admin/platform/module-authority`.**
`GetModuleEnvelope` answers, beside the current revision (`revision`,
`written_by`, `written_at`), what the applied manifests ask for
(`requested`: principal, binding, generation, alias, the manifest's
`authority` as JSON) and whether the returned revision declares a ceiling for
each principal (`allowed`) — `AppliedAuthorityRequests`, the pass's own reading
of the applied state, so the page and the log name the same principals. It is a
request, not a grant: a carrier the pass no longer verifies is listed and still
grants nothing. The page writes the next revision through `PutModuleEnvelope`,
fenced on the revision the draft was started from, and names each refusal (a
missing recent step-up, a revision written meanwhile, a document the parser
refuses). There is no silent step-up: an administrator whose second factor is
not recent is sent to the sign-in page with `step_up=1&next=<here>`, confirms
the passkey, and lands back on the same page to write again
(`features/auth/model/step-up.ts` — every sensitive screen turns `mfa_required`
into that, never into a bare error).

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
| `operation_audiences` | installed operation bindings with fixed audience, canonical invoke scopes, a read-only lookup subset, optional `headless_scopes` (a subset of the invoke scopes) that alone may be minted with no person present, and optional `source_delegation_scopes` (a subset of the invoke scopes, on at most one binding) that a person's connect of a datasource source delegates. A binding with no invoke scopes is **headless-only**: `headless_scopes` alone, no lookup or delegation scopes, minted only headless and refused by name on every person-delegated exchange (#1156) |
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
it cannot bind to a workload its manifest lists, is an operator's to fix and its work is
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

## Reading back a capability this host issued

A capability presented with a QUESTION is read back, never verified:
`CheckWorkContextRecordAccess`, `ListReadableSourceCollections` and the
module-parent reads (`GetCurrentInstallation`, the executable-artifact reads) go
through `WorkContextAuthorityServer.readBack`, which is core's
`Authority.ReadBack`. It makes every check a verifier makes against live state —
signature, issuer, audience, window, revision, seal, epochs, binding — and never
touches the replay store, so the consumer that verified and consumed a
single-use capability can still ask about it, on every page of a listing. A path
that ACTS on a capability or derives from it (an exchange, a renewal, a child)
verifies and consumes with `verifyIssued`; do not move one onto `readBack`, and
do not answer a question through `verifyIssued`.

**A read-back trusts only the current signing key.** `Authority.ReadBack` checks
the signature under the authority's own key id and nothing else, where a
verifier holds a key map that can also carry a previous key. So during a key
rotation, a capability signed under the PREVIOUS key is refused by every
read-back — fail closed, as an invalid capability — while a consumer whose JWKS
still lists that key accepts it. The capability's lifetime bounds the window:
its holder mints a new one under the current key. Rotating the Work Context key
therefore briefly refuses questions about capabilities already in flight.

## A person's capability is sealed to its audience's installation

A capability a person's session issues, or a module re-mints for that person,
is sealed to the organization's installation of the audience it addresses; a
module principal's is sealed to its own. That is an invariant every such mint
keeps, not a default: `StartTask` resolves it, `remint` re-resolves it for a
root session, an audience exchange, a renewal and a module's exchange through
its operation or read binding, and `StartChildSession` (whose core `Child` keeps
the parent's seal) refuses a person's child for an audience sealed elsewhere. It
holds a child of a service principal's capability as it is, and refuses a child
of any other owner kind.

Two person-owned kinds are sealed elsewhere by design, each behind its own gate:

- a **source-operation context** (`startSourceOperation`): sealed to the
  installation of the module using the person's source delegation, and minted
  only by that module's identity exercising the delegation
  (`MintSourceOperationContext`, or the delegation arm of
  `ExchangeDelegatedOperationAudience`);
- an **installation task** (`StartInstallationTask`): owned by the
  installation's owner of record, sealed to that installation, and reached only
  with an `EXPOSURE_INTERNAL` credential.

The gateway's bearer-less module arm depends on it: it admits a capability with
no bearer only when the seal names an active installation other than the
target's. That admits module operation contexts and the two kinds above, the
unattended-work family, and never a person's interactive capability.
`TestAPersonsCapabilityIsSealedToItsAudiencesInstallation`
(`pkg/adapters/work_context_seal_pin_test.go`) pins it over every person mint,
and `TestEveryWorkContextMintIsClassifiedBySeal` enumerates every mint RPC from
the generated descriptors and fails on one listed neither as person-sealed (with
its case in that table) nor as sealed elsewhere (with its reason). A mint that
copies a parent's seal across audiences reopens the arm to a person's credential
without its bearer.

The arm reads the tenant's installations through `InstallationService/ListInstallations`
(`EXPOSURE_INTERNAL`). Its contract comment in `installations.proto` names both
of the gateway's reads: the viewer projection, with the tenant projected from a
verified identity, and the arm, whose tenant is the one an edge-verified Work
Context names, a signed statement of this issuer's and never a value the request
chose. The messages the RPC takes and answers live in
`installation_listing.proto`, which `installations.proto` imports and the
gateway vendors byte for byte, as it does `module_registration.proto` and
`solution_entitlements.proto`.

## Minting a module Work Context

The identity itself is a **Work Context**. The auth-gateway brokers the exchange
to `ModuleCapabilitiesService/MintModuleWorkContext` (`EXPOSURE_INTERNAL`). The
module presents its pod's **projected ServiceAccount token** (audience
`accounts`) as the request's bearer, and `ModuleAuthorizeWorkContext` refuses a
prefix that is not a declared module principal, then runs `BindExecution` (see
"Establishing what a caller is running" below): the token is reviewed, the pod it
names is read, and its namespace and ServiceAccount must be a workload the
principal's applied manifest lists. Only then is a capability minted, owned and
actored by the module principal (`aud=module-capabilities`), and a
`saas.module.work_context_minted` audit event records it with the workload it
was bound to — namespace, ServiceAccount, pod, pod UID, container. The response carries
`{token, expiresAt, principalId, tenant}`.

- **No secret proves a module's identity (#1056).** `MODULE_IDENTITY_SECRETS`,
  `MODULE_REGISTRATION_SECRETS` and `SOLUTION_REGISTRATION_SECRETS` are deleted,
  with their parsing, their configuration reads and both registration mints.
- A refusal is a **verdict** (`PermissionDenied`/`Unauthenticated`, the same
  fixed message whichever check failed): a token the API server refused —
  another audience, another cluster, expired — a token for another ServiceAccount
  or namespace than the principal's manifest lists, or a replaced pod. The cause is logged here, never returned.
- A host that could not establish the execution answers **`Unavailable`**: the
  API server is unreachable, no execution binding is wired, or the container
  has not started yet. Execution binding is wired only with delivery
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
  revision and the principal's producer epoch — no build — and
  `VerifyModuleWorkContext` returns a `ModuleCaller` whose `Sealed` carries
  them. So an uninstall revokes a module capability at its
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
- Every mint calls `BindExecution`: the projected token is reviewed and its pod
  held to a workload the principal's applied manifest lists.
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

## Establishing which workload a caller is

`BindExecution` (`pkg/business/module_execution_binding.go`) takes the projected
token and the principal the caller names, and establishes the rest
independently:

1. **Review** the token (TokenReview, audience `accounts`; natively, core's
   native attester under the run's anchor): it names a namespace,
   a ServiceAccount, a pod and a pod UID. A token the API server refused is a
   verdict (`ErrExecutionTokenRejected`); an API server that could not answer is
   `ErrExecutionUnbound`. A token naming no pod, or valid for longer than the
   600-second projection, is refused.
2. **Resolve** the reviewed `(namespace, ServiceAccount)` over the WHOLE
   registry: exactly one principal must declare a workload with that pair, and it
   must be the one asked for (`ErrExecutionWorkloadAmbiguous`,
   `ErrExecutionIdentityMismatch`). The pair is the applied manifest's workload
   by rule — the namespace is the module's, the ServiceAccount the service's — so
   a principal runs as every workload its manifest lists. In a pod, the mesh
   presenter's SPIFFE ID must name the same namespace and ServiceAccount.
3. **Read** the pod the TOKEN named, never one the caller named: its UID must
   be the token's, it must be Running and not being deleted, the manifest's
   container for that workload must be a running regular container, and nothing
   else that could read the token may be in the pod (a sidecar, a running or
   restartable init container, an ephemeral container:
   `ErrExecutionUnapprovedContainer`). A container not started yet is
   `ErrExecutionUnbound`; one stopped is a verdict.

**No image is read or compared.** Build approval left the runtime path with the
module manifest: what a workload runs is deploy-time admission, and a Work
Context seals no build. So containerd's `status.imageID` aliasing (#1145) cannot
refuse an approved build or admit a withdrawn one at mint.

The presenter's trust domain is not compared: the manifest declares no SPIFFE ID
and this host is given no trust domain, so the presenter is held to the reviewed
namespace and ServiceAccount only. Delivering the cell's trust domain to the host
is the follow-up that restores that term.

## Serving the live authority a callee verifies against

`ModuleCapabilitiesService/ResolveWorkContextAuthority` answers the live values a
composed module's verifier holds a presented capability's seal against: the
installation and its revision, a principal's epoch, the operation binding, the owner's authorization revision, and an approval grant
(this host records none, and says so). Without it a module can only run core's
forwarding-hop `Inspector`, which checks no seal, epoch or revision and consumes
no single-use token — so a revoked installation or an advanced epoch stays
accepted there for the capability's whole life.

- **The capability is the authorization.** Each question carries the capability it
  is about. The host authenticates it with core's `Inspector` against the route
  target it resolved from the CALLER'S own credential — the module's registration
  prefix, or the route alias its delivered presence declares
  (`declaredModuleAlias`) — then requires the capability's tenant to be the
  caller's bound organization, and each identifier in the question to be one the
  capability names (owner, a chain principal, the sealed installation, the sealed
  binding, the hop's grant). Nothing is consumed: a question is not a use.
  **An earlier revision took the tenant and audience as request fields and
  compared the audience to the caller's prefix; an independent review showed that
  pinned nothing, because no answer depended on either field.** Do not reintroduce
  a request-named subject here.
- **The answers are the issuer's own, not a second read.** The serving path hands
  each question to `moduleAuthoritySources` (`pkg/adapters/work_context_core.go`),
  which returns the same `hostSealSource`, `ownerRevisions` and `noApprovalGrants`
  that `coreAuthority` mints with and `coreVerifier` verifies with. One body, so a
  mint and a callee's verdict read the same state
  (`TestTheServedAnswersAreTheIssuersOwnSources`).
- **Absence is a STATE**: `UNHELD`, `UNKNOWN`, `BEARS_NO_EXECUTION`, `ABSENT`,
  `NOT_RECORDED`, each with an `UNSPECIFIED` a client refuses on. A verifier turns
  each into a refusal of the capability.
- **A decision and an outage are different answers.** `FailedPrecondition` means
  the issuer decided and the answer is no — a removed member, a principal with no
  authorization record, an inactive installation, a stale delegation, a build that
  cannot be named (`decidedWorkContextAuthority`). `Unavailable` means a lookup
  failed. Collapsing the first into the second is what the review found: the
  removed-member case is the FIRST question core asks, so nothing downstream
  catches it, and a retry policy retries a permanent revocation forever. No
  refusal returns the cause's message — these questions are about a third party,
  and one authority refusal names the subject's current tenant.
- **No replay store is served.** Single-use consumption belongs to the consumer,
  which needs a durable, shared store when it runs more than one replica. Core
  keys its revision source by tenant while this host's revision is
  owner-dependent, so the question names the owner.
- **The cost is five or more round trips per verification**, each authenticating
  the capability and re-reading the caller's live grant. There is no batch
  question.
- The published Go client for it is `saas-sdk-go`'s `moduleauthority`, not this
  repository; the wire contract is held here by
  `pkg/adapters/module_work_context_authority_test.go`, which drives
  `sdk-go/workcontext`'s `PinnedVerifier` against the real handler.

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
- A **headless-only** binding — `headless_scopes` and no invoke — is minted here
  and nowhere else: `business.ModuleOperationAudience`,
  `ModuleOperationAudienceForDelegation` and the delegation-reference arm refuse
  it by name (`errOperationAudienceHeadlessOnly`), so it never acts on a
  person's behalf. The reconciler derives one whenever the binding's headless
  scopes survive the envelope and none of its invoke does: the manifest asks
  only `headless` operations, or asks invoke the envelope does not allow, or the
  envelope's audience is itself headless-only. Installation consent keeps it on
  its headless scopes and never turns an invoke binding whose invoke it
  withholds into one.
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
  the source, the person's role or their revision moves. That revision is the
  person's ordinary one — the higher of the organization's and the person's own —
  so revocation is coarser than the delegation alone: ANY organization-wide
  revision bump (a role or membership change anywhere in the organization)
  revokes every delegated capability in flight. A verifier answers it as stale
  (`FAILED_PRECONDITION`), and the consumer mints again; a delegation that has
  itself ended then answers `PERMISSION_DENIED` (`DELEGATION_REVOKED`). The
  lifetime is 60 seconds, so the cost is one extra round trip, not a stall — but
  a consumer must not retry a stale capability as if the failure were transient.
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
- **`binding` is the installation-principal arm, and PLANNED.** It would name
  one of the release's bindings and mint from that binding's `headless_scopes`
  under the module's own principal (`MintModuleOperationContext`) — an ordinary
  token admission, bounded by the capability's own short lifetime. It cannot
  serve the reconcile trigger and has never been emitted (#1112):
  `ModuleOperationContextTTL` is 60s against the trigger's 10-minute
  `DatasourceTriggerDeadline`, `requireReconcile` needs an organization member
  which an installation principal is not, and `DeclareSourceTrigger` sets the
  field nowhere. The mint route itself works and is tested; what is missing is a
  lifetime policy for occurrences, which would be a second lifetime on a
  capability every other headless call shares.
- **A reconcile occurrence's capability is bound to the SOURCE, not just the
  organization** (#1112). `ReconcileSource` and `LookupReconcileSource` are
  `sourceOperationProcedure`, so a delegated child authenticates as the
  delegating person — an organization administrator, for every source a
  reference is recorded against. Tenant, membership and role are therefore
  satisfied org-wide, which is why `requireReconcile` also calls
  `requireSourceOperationScope` (`invoke` for the pass, `read` for the lookup),
  as `InvokeSourceOperation` and `PruneSourceOperationReceipts` always have.
  Without it a child scoped to one source could reconcile every source in the
  organization. The action names are the existing vocabulary deliberately: a new
  `reconcile` verb is one no composition declares in its
  `source_delegation_scopes`, so it would refuse every real delegation instead
  of narrowing one.
- **Revocation is not durable while the reference is the source id** (#1112). A
  mint resolves the active delegation from the source, so a reconnect records a
  new one the same reference resolves to and an installed trigger re-arms with
  no re-delegation. This host already takes the durable form
  (`SourceDelegationRef.DelegationID`, and the gateway's `delegation_id`); the
  module-authority client a firing module mints through sends only `source_id`
  and asserts the answer's `source_id` matches, so a delegation id in the
  declaration is refused by that client, not here. Named as that client's gap in
  [WORK_CONTEXTS.md § Triggers](../WORK_CONTEXTS.md#triggers-for-work-no-person-started).
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
- **Binding a reference without reconnecting, additively.** A delegation is
  recorded when a person connects or reconnects a source, so a release installed
  afterwards, or a module that declared its binding afterwards, finds no
  reference to name and nothing backfills one.
  `DatasourceService/DelegateSourceTriggers` (`TENANT_REQUIREMENT_ORG_ADMIN`)
  records one under the calling administrator, in one transaction. It grants
  nothing new: the scopes are the binding's declared ones and every fact is
  re-checked on every mint.

  It records in `sourceDelegationBackfill` mode, not the connect's
  `sourceDelegationReplace` (#1112) — two modes of one function, so the registry
  read that answers "which modules accept a delegation here" has exactly one
  path. The replace mode revokes every delegation on the source as `replaced`
  and re-inserts each under the caller, which is right for a connect and
  destructive for a backfill: it took a personal
  source's delegation from its owner and moved every unrelated module's
  authority onto the administrator, failing in-flight children's re-checks. The
  backfill leaves a target that already holds an active delegation untouched and
  records only for a target with none, and it refuses a personal source the
  caller does not own (`ErrTriggerSourcePersonal` → `PermissionDenied`, told
  apart from `NotFound` because the source exists and is readable) — the
  division `ReconcileDatasourceSource` makes on the pass itself, so a reference
  can never bind a trigger to an authority that cannot perform the operation it
  admits. Not `ReconcileSourceAuthority`: that role gate admits the owner *or*
  an organization administrator, so an administrator reaches it on another
  member's personal source and is stopped one step later.
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

- **The schedule is DECLARED here** (issue #1081) **and, for now, also FIRED
  here** (issue #1112). `business.DeclareSourceTrigger` turns a source's
  interval into a trigger a declared release carries — a five-field cron in UTC,
  `"skip"` for both missed occurrences and overlap, a deadline in nanoseconds,
  the input every occurrence is admitted with, and the identity occurrences run
  under.

  #1090 deleted the host's sweep on the strength of that declaration, and
  nothing consumes it: `GetSourceTriggerDeclaration` is a read, no path here
  installs a release, and the module that fires triggers keys an installed
  trigger by its release **name** while this host declares every source under
  `DatasourceReconcileRelease` — so per-source triggers cannot be installed for
  more than one source even once something tries. The result was no scheduled
  reconcile anywhere.

  So `RunDatasourceReconcile`, its one-minute tick in `work.go`,
  `enqueueScheduledReconcile`, `enqueueScheduledPullSync`,
  `scheduledReconcileKey` and the due query
  (`ListDatasourceSourcesDueForReconcile`) are back, as a **labelled stopgap**.
  The one condition that deletes them, and the exact list, is one comment block
  beside `RunDatasourceReconcile`; the firing module's gap is named in
  [WORK_CONTEXTS.md § Triggers](../WORK_CONTEXTS.md#triggers-for-work-no-person-started)
  rather than worked around here.
  `TestTheStopgapSweepIsConfinedToTheFilesThatDeleteIt` walks the AST of every
  non-test file in the service and fails on any of those names appearing outside
  the files the deletion list names, so deleting the stopgap stays one commit;
  four behavioural tests beside it assert what the sweep does rather than what
  it is called. The two schedules must not both run — they enqueue under
  different keys, so a double fire is a second pass serialised behind the first,
  which GitHub survives and a pull provider pays for with a full re-send.
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
- **A snapshot declares what is in scope; it does not order a deletion.**
  `snapshotAt` hands off the complete file list at one commit — every path that
  passes the source's `paths` and `file_extensions` filters — and emits no
  `removed` change for a file that merely left the scope. So what becomes of an
  entry a consumer holds that a newer snapshot no longer names is **that
  module's** decision, under its own provenance rules; this host neither prunes
  it nor promises it survives. The consequence worth stating plainly, because
  `UpdateSource` makes it reachable on purpose: **narrowing a source's filters
  deletes nothing here.** It changes what the next snapshot declares, and a
  surface must not tell an administrator that de-scoped content is either
  retained or removed, because the host does not know which.
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
