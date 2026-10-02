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

Who registers a composed module's REST prefix is in
[../auth-gateway/AGENTS.md](../auth-gateway/AGENTS.md#composed-module-rest-federation):
the consuming backend, holding the prefix's registration secret. The consumed
module holds only its identity secret, which is what the Work Context
exchanges below authenticate.

## The solution registry is one durable record

Both halves of a solution registration — frontend remote and gateway upstream —
are **one durable record**, not two process-local maps.
`solution_registrations` (migration `126_solution_registrations`) holds the
solution identity, its publisher, the frontend and backend halves, a
registry-wide `revision`, and a per-half lease. A registration therefore
survives a restart and reaches every replica, and is only served when it is
**whole**: a solution that registered its page but not its backend is a durable
`pending` record, deliberately absent from the navigation.

- Writes are **compare-and-swap on the revision**, so a stale publisher cannot
  overwrite newer state.
- A frontend-half write that changes the manifest also **admits the audit event
  types its dashboard graph declares** (events carrying `fields`), in the same
  transaction (`pkg/business/solution_audit_events.go`). An admitted type is an
  `audit_event_types` row owned by `solution:<id>`. A solution admits types
  only into the namespaces its own `MODULE_PRINCIPALS` entry (keyed by its
  solution id) binds — no entry, no admission — so the registration credential
  alone claims nothing. A namespace belongs to one producer (one the composed
  event catalog publishes domain events under is already held), a
  re-declaration may only add fields, and a refusal rolls the whole write back.
  Ownership follows the binding: the operator releases a namespace by removing
  it from the holder's entry and binding it to another solution, whose next
  admission takes every type in it over, recorded as
  `audit_namespaces_taken_over` on `saas.solution.registration_updated`.
  Every path that reads a type's schema — the version stamp, payload checks,
  the category label, and PII redaction on webhooks, the export feed and
  downloads — resolves it through one lookup (`business.AuditEventResolver`),
  which reads a declared type's row, so a declared field marked `pii` is
  stripped like a catalog one and a declared type is never dead-lettered as
  unregistered. `ModuleEmitAuditEvent` accepts a declared type only when the
  `solution` scope names its owner and the caller's `MODULE_PRINCIPALS` grant
  lists its namespace. A declared type also carries a **visibility** — `tenant`
  by default, or `external` — and only an `external` one is ever delivered to a
  tenant's outbound webhook endpoint. It takes two keys: the producer declares
  it (manifest `dashboard.events[].visibility`, or
  `ModuleAuditEventTypeDeclaration.visibility`) and the operator grants the
  namespace external delivery (`external_namespaces`, a subset of `namespaces`,
  refused at boot if it is not). Visibility is fixed at admission — a
  re-declaration that changes it is refused — because narrowing would silently
  stop deliveries to endpoints already subscribed and widening would start
  sending out facts under a name a tenant subscribed to when it meant something
  else.
- A composed **module** with no frontend half declares its own audit event
  types through `ModuleCapabilitiesService.DeclareAuditEventTypes`
  (`pkg/business/module_audit_declarations.go`): the same validator
  (`ValidateAuditEventTypeDeclarations`) and the same admission, under the
  same binding. `prefix` must be the caller's own (the principal derived from
  it), the types are owned as `solution:<prefix>`, and its emissions name that
  prefix as `solution`. Re-declaring what is admitted writes and records
  nothing, so a module may declare on every start; a change is recorded as
  `saas.module.audit_types_declared`.
- A deregistration leaves a **tombstone**, so a retiring deployment's delayed
  heartbeat cannot resurrect it.
- accounts serves this as `SolutionRegistryService` on the **internal listener**;
  the auth-gateway is its **only** client and brokers the frontend's half,
  exactly as it brokers module registration.
- Both surfaces hold a short-lived cache rebuilt from that snapshot, so
  convergence after any write is bounded: the gateway reconciles about every 10s
  plus an on-demand refresh on a cache miss, the frontend holds a 5s snapshot TTL.

## Declared solution presence reconciles into that same record

`solution_host_bindings` (migration `17_solution_host_bindings`) is the host's
durable record of the `SolutionHostBinding` documents delivery hands it (issue
#952). It is **not** a second registry: an applied generation writes the same
`solution_registrations` row a self-registering runtime writes, and the only thing
it adds to that row is the declaration that produced it (`declared_binding_id`,
`declared_generation`, `declared_release`).

- **Core is the judge.** `github.com/codefly-dev/core/solutionhost` owns the
  document, its validation and `Host.Admit`, which judges the whole desired set
  because route-alias uniqueness and "declared once per set" are not properties of
  one document. The reconciler runs it over the whole mount on every pass and
  applies nothing it refused. Core's shipped `Fixtures()` are this side's
  acceptance cases, so the renderer and the host cannot drift on the same bytes.
- **One row per binding ID, three column groups.** `desired_*` is the newest
  generation delivery has shown this host, admitted or not; `applied_*` is the
  generation the host reconciled plus the registry key it reconciled into;
  `pending_reason` is why they differ. Observed state is not there at all — it is
  the lease and the endpoints on `solution_registrations`, reported by the runtime.
  `ListSolutionHostBindings` serves all three, which is the only way to tell a
  binding whose first generation was refused (no registration record exists) from
  one that was never declared.
- **The registry key is the route alias, not the binding ID.** A binding ID names
  one deployment instance and may carry characters a path segment may not. A
  present generation must declare exactly one single-segment lowercase alias or it
  is refused with a reason naming the route; Core's alias vocabulary is wider. A
  tombstone declares none, which is why the row keeps the key it last applied.
- **A refusal is attributed, not fatal.** One stale render must not hold back every
  other binding, because a stale render is the ordinary case — the renderer derives
  a generation from the previously delivered document. A refusal Core raises that
  cannot be attributed to a document withholds the whole set instead, failing
  closed on a rule this host does not recognise.
- **Convergence and restart are the row lock.** Each apply re-decides under
  `GetSolutionHostBindingForUpdate`, so two replicas proposing the same generation
  produce one apply and `DecisionCurrent` for the other, and a restart resumes from
  the recorded generation rather than deriving it.
- **The mixed window is enforced in `planSolutionRegistrationWrite`,** on the row
  the heartbeat path already locks — never by a second table read, which would
  leave a window in which a heartbeat decided it was undeclared and then wrote. A
  heartbeat for a declared record may refresh the lease, the upstream and the
  manifest; it may not repoint the route, revive a declared tombstone, or
  deregister. A heartbeat for an **undeclared** record is unchanged, and a record
  becomes declared only when a generation applies — so a document that has not
  passed cannot take a working self-registered solution offline. Both halves are
  tested (`solution_registry_declared_test.go`).
- **Declaring an existing registration adopts it.** Its halves and leases are
  observations; discarding them would take a working solution offline at the
  instant it became declared. The publisher of record stays `solution:<id>`, the
  subject the registrant's own credential proves.

The full model, the field-ownership split and the configuration keys are in
[../../SOLUTION_REGISTRATION.md](../../SOLUTION_REGISTRATION.md#6-declared-presence-delivery-says-what-runs-a-heartbeat-says-how-it-is).

## The composed-module service principal

A module consuming the module-facing capability surface
(`ModuleCapabilitiesService`: job enqueue/claim, notify, approvals, audit,
events, subject visibility) calls it as its own **service principal**, whose id
is derived from the
same registration prefix (`business.ModulePrincipalID`) — nothing is
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
- The capability **seals identity and tenant only**: what the principal may do is
  re-read from the declared grant on every call, so narrowing a grant takes
  effect immediately rather than when the outstanding token expires.
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
