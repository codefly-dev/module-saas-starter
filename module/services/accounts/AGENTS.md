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
  lists its namespace.
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
- **Exchange.** `ExchangeDelegatedOperationAudience` of a parent whose actor hop
  is a declared module principal carrying a delegation id admits the parent's
  tenant by re-checking that delegation (`ConfirmSourceDelegationParent`), not by
  the caller's `authorizeTenant`; the parent must still be addressed to the
  caller and the child is attenuated to the caller's binding. A parent without
  such a hop, and every read exchange, keep `authorizeTenant` unchanged.
- **Codes.** `FAILED_PRECONDITION` + `DELEGATION_MISSING` (the source has no
  active delegation: reconnect), `PERMISSION_DENIED` + `DELEGATION_REVOKED` or
  `DELEGATION_INVALID` (indistinguishable from absent), `UNAUTHENTICATED` for an
  unproven module. The reasons are `google.rpc.ErrorInfo` under
  `accounts.saas.codefly.dev`, a wire contract the gateway pins too.
- **Administration.** `DatasourceService/ListSourceDelegations` and
  `RevokeSourceDelegation` (`TENANT_REQUIREMENT_ORG_ADMIN`, like the other
  datasource administration) show and end an organization's delegations.
- **Audit.** `saas.datasource.delegation.created`, `.used` (every mint) and
  `.revoked` (with `reason`).

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
