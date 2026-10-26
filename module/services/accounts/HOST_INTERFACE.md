# The host interfaces, served by accounts

**What we are doing.** accounts implements the four host interfaces declared in
the authority interface repository (`codefly.dev/authority`, `composition`, `installation`, `session`)
as adapters over what it already does, so the same contract the local authority
serves on a laptop is served here on a cell, and the authority interface repository's
`pkg/conformance` can prove the two agree. Rung 1 of the validation ladder.

## What is on this branch

- `proto/host/**`: the interface protos, copied unchanged from the authority interface repository;
  `proto/buf.gen.yaml` generates them beside `saas` (`code/pkg/gen/host/**`).
- `module.codefly.yaml`: the `connect` endpoint declares `implements` for the four (moved from `authority` on 2026-10-25; see below).
- `code/pkg/business/interface_policies.go`: the host's method policies for the
  interface procedures. The interface protos carry no `saas.policy` option on
  purpose (policy is one implementation's guard, not the caller's promise), so the
  policy lives here, keyed by full method, and `rpc_policy.go` reads it when a
  descriptor carries none. Descriptions for the catalog live in the same file.
  `InstallableService` is excluded: it is the solution's side.
- `code/pkg/adapters/host_interface.go`: the adapters. `Keys` and `Check` are the
  Work Context authority's issuer, key and `verifyFor`. `Issue` maps by owner
  shape: a person → `StartTask` for the bearer's own session; an installation →
  `StartInstallationTask`; a workload → the headless operation-context mint
  through a binding it declares, or its own module context
  (`host_interface_workload.go`); a parent token with an actor → the hop
  through the actor's binding, without one → `ExchangeAudience` at another
  audience, or a renewal at the parent's own (`RenewWorkContext` for a
  delegated parent, `StartRootSession` for an owner's own); a delegation →
  Unimplemented, naming the delegation gap. `List` → `ListInstallations`.
  `SignIn` → fixture `Authenticate`, returning the access token's session id.
- `code/pkg/adapters/connect_handlers_host_interface.go` + `connect_bindings.yaml`:
  the Connect handlers and their bindings, in the shape every other service uses.

## The gap list (red on this branch until each is closed)

1. **Closed.** The service catalog carries one `api_package` (the service's own) and the `interface_packages` it implements beside it (`catalog.proto` field 9; `service_catalog.go`); the frontend catalog, the gateway route aliases and the mesh gates read the API package only. Four tests that hard-coded one package now read the served set.
2. **Closed.** `cmd/connect-registration` resolves a binding by full service name first and short name otherwise, and imports each implemented package's generated Go and Connect packages under derived aliases; `connect_bindings.yaml` keys the four interface services by full name. The authorization matrix counts services by full name.
3. **Closed.** Workload-owned `Issue` is served: the credential names the
   module (`ModulePrefixOfWorkload`), and the issue is the headless
   operation-context mint through a declared binding, narrowed to the asked
   scopes, operation and lifetime, or the module's own context (see "Review r12
   repairs" below).
4. **Method-policy delivery closed (2026-10-25).** `ApplyManifest` accepts the
   signed core carrier and its optional signed `AuthorityManifestV1` through
   the tenant listener. The existing delivery owner authenticates the carrier,
   persists the pair and reconciles it synchronously. `Check` enforces the
   exact applied generation's method policy. Details and limits below.
5. **Install and GrantToTeam are served.** Install resolves the target and role
   through the existing catalogues. GrantToTeam resolves the installation's
   standing role under its installation lock and uses `GrantInstallationToTeam`.
   Pending installations remain unexposable until the durable install step is
   acknowledged. The shared suite still needs fixture hooks for that acknowledgement
   and the host-generated team id.
6. **Exposure audit reused.** The existing scope-grant transaction emits the
   `saas.scope.granted` audit event; the interface policy declares that success
   emission and adds no duplicate installation-created event. The interface
   documentation groups authority and installation under authorization, session
   under identity, and composition under platform administration.
7. **Closed 2026-10-22.** The interfaces carry the host's names (publisher `codefly.dev`, packages `host.*.v1`), so this repository carries no consumer name; the naming gate is at its main baseline.
8. **The conformance harness** needs accounts booted with its store (the test
   Postgres each package persists) and the fixture people; the suite takes a
   `grpc.ClientConnInterface`, so an in-process server over bufconn is enough once
   registration works.

9. **Closed.** The naming gate reported `generated/service-catalog.json` as "too large to scan": main's catalog was 1.4 KB under the 512 KiB cap and eight methods crossed it. The gate now skips that generated file for the reason it already skips descriptors: every string in it is a scanned proto identifier or a scanned Go description, restated in `AUTHZ_MATRIX.md`. Named here for review: a tool fix, not an allowlist entry.

## How to run the suite against accounts (the harness, next)

The suite is `github.com/codefly-dev/authority/libraries/conformance/go`: it links
no generated package (an implementation links its own; a second registration of
one proto file panics) and drives the interfaces from a published descriptor set
with dynamic messages over any gRPC connection. The harness here is one
integration test in `pkg/adapters`, shaped like `database_transport_integration_test.go`
and `readable_sources_integration_test.go`, which already boot the persisted test
store:

1. boot the store and the service as `pkg/business/service_test.go` does
   (`sdk.WithDependencies`, `infra.NewPostgresStore`, `business.NewService`), seed
   the `dev-admin` fixture (`fixtures.Seed`), set the package-level `service`;
2. configure the Work Context singleton as production does
   (`permission_servers.go:51`) against that store;
3. `NewGrpServer(&Configuration{})` and serve it over `bufconn`, so the interceptor
   authenticates bearers;
4. sign in through the fixture provider (a fixture person's token is their
   `provider_id`, `dev-admin` for the super administrator; the suite's
   `AdminToken` and `ViewerToken` name two of them);
5. `conformance.Run(t, conn, descriptors, fixture)` with the descriptor set
   `codefly generate contracts` writes for this service
   (`module/contracts/api/accounts/authority/contract.binpb`, regenerated on this
   branch so it carries the `host.*` services).

Expected red on the first run: workload-owned `Issue`, `ApplyManifest`, `Install`
and `GrantToTeam`, which refuse by name (gaps 3 to 6), and the `List` assertion
that an installed solution exists, since the fixture installs none. Each red line
is a line of the gap list, which is the point.

## The suite against accounts (2026-10-22)

`HOST_INTERFACE_CONFORMANCE=1 go test ./pkg/adapters/ -run TestAccountsPassesTheHostInterfaceConformanceSuite`
boots the test store, seeds the dev-admin fixture, configures the Work Context
authority against the store, serves the Connect listener on an ephemeral port
and runs the suite over gRPC. The harness composes three modules: documents (the
audience the viewer reads at), org-directory (the page module, one declared
workload, one binding to documents) and model (an audience the viewer's context
is not addressed to); a fake cluster projects org-directory's credential.

**Green (6 of 10):** Keys; sign-in and an unknown token refused by name; the
person line; every refusal with a reason; the workload line; the child hop.

What it took, each a fix at the place that owns the behaviour:

- *Composition installs a module at every organisation* (contract decision):
  `compositiontest.ComposedModuleInstallations` (a harness stand-in, imported by tests alone) answers the live module authority and
  the seal store for a composed module's installation, held by every member, with
  the declared grant as its scope (the manifest is the ceiling); a control-plane
  membership read, `WorkContextOrgMember`, sits beside the other seal reads.
- *An uncomposed audience is NotFound by name*: checked against the derived
  registry through `ModulePrincipalID` before any mint.
- *Check is authenticated by the token it checks*: public, like Keys.
- *Issue is authenticated by the request's shape* (`business.HostIssueMethod`):
  both interceptors let a call carrying no credential at all reach the adapter,
  which refuses a bearer-less person by name; a bearer or a forwarded identity is
  admitted as on every other method.
- *A workload is named by its credential alone*: `ModulePrefixOfWorkload` reviews
  the credential and finds the one principal declaring that workload; the issue
  is the host's operation context when a binding is named, the module's own
  context otherwise; an undeclared binding is NotFound by name.
- *An operation context names its binding*: the seal source resolves
  `<organisation>/<module>/<binding>` (the spelling the interface states; it
  was `module-operation/<principal>/<binding>` until 2026-10-25) from the
  installed grant and the operation-task mint seals it. Until now the token carried no binding, which is
  what left runtime hops "stale" at the revision check.
- *A child hop by the parent token and the actor's credential*: `childHop`
  verifies the parent as issued, names the actor from its credential, re-reads
  the owner's authority as the owner, refuses widening as PermissionDenied and
  derives the hop with no binding.
- *A hidden refusal names its reason*: `mapWorkContextError` logs the one case
  the caller is told nothing.

**Red (4 of 10):**

| Red line | Gap | Proper fix |
|---|---|---|
| empty ask: `authority_scopes: must contain at least 1 item(s)` | **11** | an effective-permissions read in the host (person × audience → scopes); the adapter uses it when the ask is empty |
| `installed solution = <nil>` | fixture | the dev-admin fixture gains an `installations` section performed through the real install path (owner decision); follows gap 4 |
| ApplyManifest: `internal RPC is not exposed on the tenant listener` | 4 | the delivery path behind the composition interface, on the internal listener; the harness dials it there |
| Install, GrantToTeam | 5 | name-to-id resolvers; not reached until the manifest applies |

**Gap 14 (closed 2026-10-25, see "Gap 14: a hop through its binding" below):** a hop
kept the owner's seal installation and core requires a binding named on it to be
granted within that installation; a module's binding is granted within the
module's own installation. The child is now sealed to the installation its
binding is granted within.

Suite changes today, each stating the contract more precisely and none passing a
red line: an authenticated caller for the audience probe; explicit scopes in base
cases with the empty ask as its own line; List as an administrator's read;
`Session.bearer`; the elsewhere audience as its own fixture field; the hop names no
binding until core decides.

## The suite at 13 lines (2026-10-23 evening)

The interface repository's suite (at `2e2bcf2`) now states the hop rule and the
delegate rule: a module hops only on a token addressed to it, names the binding it
exercises, and inherits from the owner, so a chain of modules can cross audiences.
On accounts: 5 pass (Keys, sign-in, the person line, refusals by reason, the
workload line), 3 skip because this harness states no permission matrix, no
delegate chain and no installed solution yet, 5 fail.

The hop line's new first step, a person's token naming its module's binding, is
refused because the adapter sends no scopes for that shape; the host's
`withOperationBindingScopes` (#1219) is the person half once it inherits the
person's holdings rather than installation ids. The hop itself must then require
the parent addressed to the acting module and re-derive from the owner, as the
host's audience exchange already re-mints, so a chain crosses audiences.


## Current suite baseline (2026-10-25)

On branch `authority-interface` at `61835c789`, updating only the shared
conformance dependency to `06d0d3b8567448b991e4a7ab591ccc8ab1c32681` gives
**5 passed, 7 failed, 7 skipped** across nineteen cases. The command is:

```sh
HOST_INTERFACE_CONFORMANCE=1 GOPRIVATE=github.com/codefly-dev/* GOWORK=off go test -json ./pkg/adapters/ -run '^TestAccountsPassesTheHostInterfaceConformanceSuite$' -count=1
```

Run from this service's `code` directory. It starts Accounts with its isolated
PostgreSQL test dependency, and makes no model-provider calls.

| Outcome | Cases |
| --- | --- |
| Passed (5) | Keys; fixture sign-in; explicit person scopes and wrong-audience refusal; named refusal reasons; workload binding |
| Failed (7) | ListFixturePeople missing from copied descriptors; Current missing; empty scope ask; no installed-solution fixture; child-hop initial binding mint has empty scopes; ApplyManifest called on tenant listener; install/grant setup blocked by that delivery refusal |
| Skipped (7) | Permission matrix; stale-installation verification; delegate chain; method policy; resource grants; lookup scopes; durable delegation |

A skip is not qualification. The stale-installation case performs preliminary
verification checks but skips before completing its revocation proof. The
existing thirteen-case result above remains historical evidence only.

The harness directly supplies a composed module registry and synthetic
installations. It does not exercise production signed delivery, persisted
installation consent or concurrent revocation fences. Passing this harness
would not alone establish production equivalence. The manifest-as-ceiling
model remains part of the open host-interface proposal, not proof that the
production envelope contract has changed.

The shared installation-issuance case currently presents a public installation
ID without authenticating the installation caller. That test and the reference
issuer need an authenticated caller contract; Accounts must preserve its existing
trusted installation-mint perimeter. The adapter also still needs to map the
requested installation and organization into that owner operation.

The signed-carrier commit `e154aec500b8206848c825d2812629b4eb72a8c9` is already an
ancestor of this branch; no sibling cherry-pick is needed.


### Repairs following the current baseline

Child issuance now resolves the actor from its workload credential before
verifying the parent through the existing `verifyFor` owner seam against that
actor's audience. A regression through `Issue` proves that a correctly addressed
parent issues, a parent addressed elsewhere does not, and the refused attempt
does not consume that parent's single-use capability. Restoring the previous
`verifyIssued` call reproduced the erroneous issuance.

The SDK dependency process now has a lifetime separate from the service's
bounded context. Cleanup calls `Destroy` with a fresh bounded context before
canceling that lifetime and reports destruction errors. Previously the function's
deferred cancellation killed the SDK context before test cleanup, producing a
canceled destruction warning. The repeated real-store run no longer emits that
warning even though the service context is canceled before cleanup.

These repairs do not close the other seven failures or seven skips. In
particular, Core reports the wrong-audience parent as `Unauthenticated`, while
the shared child-hop case expects `PermissionDenied`; that case is still blocked
earlier by its empty-scope/binding setup. Invalid tokens must not all be remapped
to permission errors to silence this mismatch. Full delegated exchange reuse,
named binding support and current interface generation remain open.

## Session discovery preparation (2026-10-25)

The authentication owners now provide the reads needed by the session adapter:
`devvalidator.Validator.FixtureTokens` returns a defensive, ordered copy of the
configured allowlist. Duplicate tokens retain the validator's existing last-wins
identity and appear only at the winning declaration's position.
`pgauth.Resolver.LookupIdentity` uses the same active-person lookup and organization
selection as login, within the existing database-enforced read-only transaction.
It does not create sessions, provision people, bootstrap an administrator or touch
login timestamps. It returns person/org/org-role/display facts only; it is not a
minting identity.

Real PostgreSQL tests compare discovery with login for ambiguous memberships,
a persisted default newer than the oldest membership, and provider organization
precedence. They verify no session/bootstrap/membership/recency changes, and
refusal of missing or suspended people. Validator tests verify order, duplicate
credential semantics and defensive copies.

These are preparatory owner capabilities, not completed SessionService endpoints.
`Current` and `ListFixturePeople` are still absent from the vendored descriptor;
the conformance baseline above remains 5 pass / 7 fail / 7 skip. Integration must
use only the enabled development validator, preserve real-provider and orgless
sessions, resolve teams through the existing member-filtered store, and prove
ended-session refusal through the actual authentication perimeter. The existing
MFA-challenge response must be handled explicitly, never bypassed.

The proto-generation baseline is reproducible through Codefly after the template
selection fix, with its complete output retained. The catalog compilers are not
yet integrated into Codefly's generation lifecycle; see
[REST regeneration](../../REST_SURFACE.md#regeneration). That is an owner tooling
gap, not authorization to edit generated catalogs or run a substitute pipeline.
No composed browser journey, external identity provider or complete session
conformance is qualified by these tests.

## Milestone 2: the edge and the hop against accounts (2026-10-25)

What the authority's edge and hop call is now served on the declared endpoint:
`SessionService/SignIn`, `Current`, `ListFixturePeople`; `AuthorityService/Issue`
(a person's empty ask included), `Check` (with `method` and `advisory`);
`InstallationService/List`. The branch was merged with main first (no rebase).

- **Protos at authority `06d0d3b`.** `proto/host/**` re-copied unchanged for the
  four declared interfaces (`DIGESTS.sha256` re-recorded) and regenerated with
  `codefly generate proto` and the repository's catalog step
  ([REST regeneration](../../REST_SURFACE.md#regeneration)). `host/artifact/v1`
  is not copied: no `interface.codefly.yaml` declares it. The interface's
  `google.api.http` mappings are not on this host's REST surface: interface
  procedures get Connect routes only, and the public OpenAPI drops their
  operations (the gateway route compiler already did; the public OpenAPI
  renderer now agrees instead of refusing).
- **`implements` is on `accounts/connect`.** The `authority` endpoint admits only
  the internal module-authority procedures behind the internal credential
  (`grpc_auth_interceptor.go`), so every interface method was refused there.
  The Connect listener mounts all four services under their own policies.
  `TestImplementedInterfacesAreDeclaredOnTheEndpointThatServesThem` holds the
  declaration to the endpoint whose contract surface carries every method (it
  fails with the declaration back on `authority`).
- **Gap 11 closed: an empty ask.** A person's `Issue` with no scopes is
  everything they hold at the audience (`host_interface_holdings.go`). The
  candidates are the kinds the audience module declares as content, each with
  every action this host names on it (the composed permission vocabulary, the
  organisation's roles, the composition's bindings to that audience). Each is
  kept only if the person holds it by the check every mint makes
  (`resolveAuthority` with the audience's content reads, the same check
  `heldBindingScope` in #1219 makes for a named binding), and `StartTask`
  decides the ask again. A person holding nothing is refused by name.
  Reconciliation with #1219 (`start-task-operation-bindings`, open): a person's
  ask that names an `operation_binding_id` is that PR's `operation_bindings`;
  once it lands, the adapter forwards the binding to it, and `personHolds`
  and `heldBindingScope` are one check that one of them should call.
- **`Current` and `ListFixturePeople`.** `Current` is an authenticated read of
  the identity the perimeter verified (signature, window, the configured
  revoker): session id, the effective subject, organisation, role (as `SignIn`
  states it) and teams; no bearer back; a credential naming no session is
  `Unauthenticated`. `ListFixturePeople` is
  `business.Service.ListFixturePeople`: the development validator's ordered
  allowlist (`FixtureTokens`), each projected through the resolver's read-only
  `LookupIdentity` and the person's teams; empty, not refused, with no
  development validator wired (every deployment that signs real people in), as
  the interface states. A token whose person is not seeded is omitted.
- **Fixture people.** `dev-admin` now carries the authority fixture's people with
  its tokens (maya, remi, alex, dev at Acme Corp; zoe, omar at a new Globex) and
  teams. Their roles at composed modules are roles over those modules'
  permissions, which this host cannot name (the boundary gate refuses them):
  the composing workspace states them in its `fixture-grants.yaml` document
  (roles and `assignments` in the fixture's own shape, by email beside
  `agent_identifier`), which the seed adds to the selected fixture.
- **`List` names its target.** An installation's `target` is the presence's
  route alias (`notes`), the name `Install` takes and a frontend binds to, not
  this host's presence row id.
- **`Check` applies a named method.** No module has a delivered method policy on
  this host (gap 4: no `AuthorityManifestV1` arrives), so the interface's rule
  for that case applies: a check naming a method is refused by name
  (PermissionDenied), and an advisory check answers the verified capability with
  that refusal in `CheckResponse.refusal`. A hop must run
  `hop/ENFORCEMENT=warn` against accounts until method-policy delivery lands;
  that delivery (ApplyManifest carrying the authority document, on the internal
  listener) is the next step.
- **Unserved, by name.** `ListResources`, `Delegate`, `RevokeDelegation`,
  `GetDelegation` are classified and answer `Unimplemented` naming the gap.
- **The composition's modules.** `Issue`'s "not composed" check and every mint
  read the registry the host derives from delivery (`IsDeclaredModulePrincipal`,
  `ModulePrincipals`), the same one every module capability reads; nothing new
  was added for it. A person's module installation is the production seal store
  in a real run; the harness alone wires `compositiontest.ComposedModuleInstallations`.

### The suite at `06d0d3b` against accounts

`HOST_INTERFACE_CONFORMANCE=1 GOWORK=off go test -json ./pkg/adapters/ -run '^TestAccountsPassesTheHostInterfaceConformanceSuite$' -count=1`,
with the harness now stating a permission matrix and an in-memory revocation
list at the minter's revoker seam (production's is the cache's). **9 passed, 4
failed, 6 skipped** of the nineteen suite cases, and the harness's own line
(an ended session is no longer current) passes.

| Outcome | Case | Why |
| --- | --- | --- |
| pass | keys; fixture sign-in; every listed fixture person signs in; Current; person issued/checked/refused elsewhere; empty ask; permission matrix; refusals by reason; workload binding | |
| fail | an installation acts for itself | the fixture installs no solution (follows gap 4) |
| fail | a child hop names its actor and binding | the person's token naming a binding is #1219; the hop naming its binding is closed below (gap 14) |
| fail | a manifest moves only forward | ApplyManifest is internal and the harness dials the tenant listener (gap 4) |
| fail | install pending, then exposed | blocked by the same delivery refusal (gaps 4, 5) |
| skip | callee refuses what does not verify | needs an installation whose grant can move |
| skip | delegate chain; method policy; resources; lookup; delegation | the harness states no case: each needs a host capability above (gap 4, ListResources, delegation) |

### Gap 14: a hop through its binding (2026-10-25)

A module calling a module on its owner's behalf: `Issue` with a parent token,
the acting module's workload as `actor` and the binding it exercises. It was
refused (`Unimplemented`, "waits on core deciding where a cross-module binding
is held"). The owner's decision (child issuance **A**) is implemented in
`code/pkg/adapters/host_interface_hop.go`:

- **Where the binding is held:** the module principal registry this host already
  derives from delivery, as installed in the organisation
  (`Service.ModuleOperationAudience`, the binding the delegated exchange
  redeems). No second store. The child is sealed to the installation that binding
  is granted within (the live answer for `<organisation>/<module>/<binding>`),
  so core's binding-within-installation rule holds by construction.
- **The child:** the owner's *current* grants at the binding's audience
  (`personHolds`, the check every mint makes, per action of the binding's invoke
  scopes, or its lookup scopes for `operation: lookup`), with the parent's
  actor chain replayed and the acting module as the last hop naming the binding.
  It descends from the parent's session (`parent_session_id`) and never outlives
  the parent. An ask must sit inside that.
- **A narrowed parent stays narrowed:** for every kind the parent carries, in its
  authority scopes or any hop's grant, the child is held to the least of them
  (actions dropped and resources named stay so). The child carries those
  narrowings in its own authority scopes, so a hop further down is held to them
  too. A kind the parent never carried is no narrowing: that is how a chain
  crosses into another audience.
- **Refused by name:** a binding the acting module does not declare (`NotFound`,
  before the parent is verified, so a single-use parent is not spent); an
  audience that is not the binding's; a parent addressed to another module
  (`PermissionDenied`, "hops only on a token addressed to it", also before
  verification); an owner holding none of the binding's scopes; an ask beyond
  what the owner holds; an ask beyond what the parent narrowed to; a headless
  operation; a context not owned by a person (this host reads current grants for
  a person only).
- **Revision check:** `ConfirmModuleHopBinding` accepts the spelling this host
  seals. The hop exercising it is confirmed against its binding; a module hop
  replayed ahead of it carries the owner's authority through that binding and
  stands while its module is installed.

The harness now states a delegate chain (documents to model and back, for an
administrator) and a lookup case. **11 passed, 4 failed, 4 skipped** of the
nineteen suite cases at the same pin (`06d0d3b`), plus the harness's own line:

| Outcome | Case | Why |
| --- | --- | --- |
| pass (new) | a delegate chain inherits the owner's authority at each audience | gap 14 |
| pass (new) | a lookup through a binding carries only the binding's lookup scopes | gap 14 |
| fail | a child hop names its actor and binding | first step: the person's token naming its module's binding is #1219 ("the person holds nothing at org-directory"); the binding id the case checks past it is the one this host now seals (below) |
| fail | an installation acts for itself | the fixture installs no solution (gap 4) |
| fail | a manifest moves only forward; install pending, then exposed | ApplyManifest is internal (gaps 4, 5) |
| skip | callee refuses what does not verify; method policy; resources; delegation | unchanged |

### One spelling, one hop rule (2026-10-25)

- **The sealed binding id is the interface's:** `<organisation>/<module>/<binding>`
  (`ModuleOperationBindingID`), for a workload's operation context and for a
  hop's child alike. The `module-operation/<principal>/<binding>` spelling is
  deleted, not accepted beside it: the seal source, the operation-context shape
  check and `ConfirmModuleHopBinding` read only the new one, and an id naming
  another organisation confirms nothing. The organisation is a canonical UUID,
  which is what tells this id apart from a source delegation's and an installed
  binding's. A revision check naming the binding by its bare key is unchanged.
- **A hop names its binding:** a child naming no binding is refused
  (`PermissionDenied`, "a module's hop names the binding it exercises"), before
  the parent is verified. The interface's old shrink-from-parent child is
  deleted; a module hop has one rule, the one above.
- **`ExchangeDelegatedOperationAudience` stays, for now.** It is the other
  cross-module rule (strict attenuation of a parent through an installed
  binding, plus the source-delegation reference arm). Composed modules still
  call it, on their authority branches and their main branches alike, through
  the Go SDK's module-authority client (`ExchangeOperation`). Deleting it waits
  on those callers moving to the interface's hop.

The suite is unchanged by these at **11 passed, 4 failed, 4 skipped**.

### What was not verified

No composed run: a composition that runs saas as the authority host, a
playground and the edge/hop against a live accounts were not started. The fixture people's sign-in and teams were proven
through the seeded harness store, not a `codefly run`. `codefly generate
contracts saas-starter --check` fails on this branch, before and after this
change: the CLI refuses a contract endpoint whose proto declares services in
more than one package (`cli cmd/generate/contracts.go:518`), so the exported
contracts do not yet carry the `host.*` services; that is a CLI gap. The
frontend fixture schema change was checked against the fixture with zod
directly, not through the frontend's vitest suite (no installed dependencies).

## Fixture installations (2026-10-25)

A fresh local run used to come up with nothing installed: each organisation's
administrator installed the solution and each module by hand through the install
API. A fixture now declares its installations (`installations:` with `org`,
`target`, `role`, `owner`), in the fixture file or in the composing workspace's
`fixture-grants.yaml`, since a target's name and a module's role are the
composition's words. The seed holds each owner to being an owner or admin of
the organisation in the fixture. Targets arrive by delivery after the seed, so
the declared installations are performed after every delivery pass
(`SolutionHostBindingReconciler.AfterEachPass`), as their owner, through
`InstallTargetByName`: an undelivered target waits, a held one (any status but
revoked) is reused, and any other refusal is reported with the pass, naming the
installation. A host with no delivery surface says it installs nothing.
Team grants are not declared yet (GrantToTeam, gap 5).

## Signed method-policy delivery (2026-10-25)

Classification: fixes at the Accounts delivery, authorization and installation
owners. Reused Core signature/admission checks, the existing carrier authorizer,
append-only delivery inbox, binding reconciler, Work Context scope containment,
and scoped-grant transaction. No second registry, issuer or grant table.

- `ApplyManifest` is callable on the declared Connect endpoint. Its authentication
  is the signed carrier plus the delivering module's workload credential. An
  unsigned sketch is refused. The raw interface credential is adapted to the
  existing carrier authorizer's Bearer-header input. HTTP delivery retains its
  existing refusal classification, including unavailable identity review.
- The authority document uses the same proven trust pins and signer as its core
  manifest and must name its module, generation and digest. The authority
  library owns DSSE verification and method-policy vocabulary validation.
  Duplicate or contradictory method policies and resource-specific requirements
  are refused before anything is persisted.
- Migration 49 stores authority document and envelope in the same immutable inbox
  row as the core carrier. Exact replay is allowed. Adding, replacing or dropping
  policy at that same generation is refused. A newer desired receipt cannot
  change the policy of the applied generation. Apply acknowledges only the
  generation the existing reconciler actually applied.
- `Check` re-verifies the applied pair with current pins. Unknown methods are
  `NotFound`; absent policies, missing required scopes and ambiguous HTTP
  templates are named refusals. Method requirements check kind and action
  eligibility; resource-limited grants pass unchanged, and the module enforces
  their exact resource limits. Connect POSTs use the gRPC method policy;
  HTTP templates support the interface's segment placeholders, literal custom
  verbs and final `{path=**}` catch-alls. Advisory
  checks return the same reason beside the verified capability.
- Installation responses project the existing role, revision and team grants.
  Migration 50 routes root-team exposure changes through the installation's
  existing revision owner; unchanged re-grants do not bump it. Grant and uninstall
  serialize under an UPDATE lock, avoiding a SHARE-to-UPDATE upgrade race.
  Existing scope-grant audit is the audit for exposure.
- The delegated-parent regression now declares and approves the operation binding
  it mints before testing the person-only exchange refusal. Its previous failure
  was at mint setup, before either exchange was exercised.

The signed conformance harness now persists its approval revision and delivers
its fixture modules through the real inbox and reconciler. On conformance pin
`06d0d3b8567448b991e4a7ab591ccc8ab1c32681`, the latest run is **13 passed,
3 failed, 3 skipped** among the nineteen shared cases, plus the passing local
ended-session case. Method-policy enforcement and forward-only manifest delivery
pass. The failures are installation-owned issuance (no installation fixture),
the person's binding mint (PR #1219), and installation exposure: the shared case
never acknowledges the pending installation and hard-codes team id `finance`.
The host's direct PostgreSQL test proves pending refusal, acknowledgement,
exposure with the actual generated team id, one revision bump, unchanged replay,
and foreign-organization refusal. Resource grants, durable delegation and
installation-revocation conformance remain skipped.

Before the catalog repair described below, `module/tools` failed only
`TestServiceCatalogProceduresAppearInAPIContractCatalog`
for the fourteen host procedures. The earlier CLI contract exporter rejected the Connect
surface's five proto packages in `cmd/generate/contracts.go`'s package-selection
function (`proto declares services in multiple packages ... a contract endpoint
must be single-package`). The owner fix described below retains the endpoint's explicit
service surface across packages in its descriptor and contract catalog, with a
regression using an owned API plus implemented interfaces. No generated contract
was hand-edited or partially retained.

Generation used the corrected CLI candidate from PR #1128 and the documented
repository catalog chain. The installed CLI ignored template input selection;
that entire invalid baseline was discarded before rerunning the corrected CLI.
The clean-input catalog check also found `email` before `egress` in Accounts'
authored manifest; ordering those inputs restored generation. Complete generated
output is retained. The control-plane call-site inventory is regenerated by its
own documented test generator: new reads are the applied signed policy and the
installation target projection; delivery's transaction moved into its shared
receiver.

No composed deployment, live edge/hop ENFORCE run, frontend journey or release was
qualified. The shared method-policy test exercises enforce refusals against real
Accounts. Existing envelope and installation-consent ceilings still govern
issuance; storing authority scope ceilings and solution roles does not replace
those existing owners or project new roles into the organization catalogue.

The fresh-store blocker recorded here is resolved by the manifest-ceiling change
below: CLI composition delivery needs no administrative envelope. Installation
consent still governs organization access. A live composed ENFORCE run remains
unverified.

Validation: Accounts adapters, business, infra and cataloggen suites pass;
`go vet ./...`, the naming gate and the boundary gate pass. At that checkpoint, `module/tools`
had only the contract-catalog failure described above, now repaired below. The full requested
`commit-identity-gate.mjs check origin/main HEAD` remains red on 38 identity
fields in commits already on this branch, including `86267c97`, `ab8577b3`
and `435e80b0`. Repairing those commits requires rewriting published history;
this task permits fast-forward pushes only. New work uses the requested
GitHub no-reply author and committer identity.

### HTTP solution policies and local refusal diagnosis

Codefly CLI `608561e2b` publishes `x-codefly-authority-policy` on each HTTP
operation. Its `MethodPolicies` reader turns these into `MethodPolicyV1` rows
named `VERB /path`, in the same signed `AuthorityManifestV1` that carries RPC
policies from `authority.hop.v1.policy`. Accounts stores and checks both kinds
through the existing signed-pair owner; it does not parse OpenAPI independently.
The HTTP matcher follows the reference authority's route grammar, including
`{id}:publish` and a final `{path=**}`. An empty capture, wrong verb, unknown
route, undeclared policy, missing scope or ambiguous template is refused.

The Accounts conformance harness adds a signed **solution** delivery through the
tenant listener, then calls `Issue` and `Check` over that listener. It exercises
a scoped HTTP read, a multi-segment checked-caller route, a write refused for
missing authority, an unmarked route and unknown/wrong-verb routes. These local
cases complement the shared suite's signed RPC-policy case.
The full rerun remains **13 passed / 3 failed / 3 skipped** in the shared suite,
plus **2 passed** local cases (signed HTTP policy and ended session). This is
host-listener qualification, not a live composed hop running in ENFORCE mode.

Two other live refusals are separate from policy storage:

- `audience ... declares no content` comes from `ModuleContentResources` reading
  the derived principal's `Resources`. In the inspected local composition, the
  affected envelope entry declared only its tenant, and Accounts logged that
  the manifest requested no authority and the envelope stood whole. The
  composition must declare the intended content resources and the module must
  publish its authority contract; method policies cannot manufacture consent.
- The Accounts log contains 21 `workload credential refused` replies between
  12:42 and 12:45 on 2026-10-25. An earlier 12:27:32 delivery was refused because
  a gRPC endpoint address was not an HTTP(S) origin, and later calls still
  reported that audience as uncomposed. However, `ModulePrefixOfWorkload`
  returns token-review or workload-ownership errors without logging their
  underlying cause; the adapter exposes a fixed refusal. The log therefore
  does **not** prove which failure caused those 21 replies. Runtime credential
  expiry or rotation has not been established as the cause.

## Solution roles, ListResources and installation issuance (2026-10-25)

Classification: fixes at the issuance, installation and store owners. Reused:
the applied signed authority document (the reader `CheckDeliveredMethod`
already used, now `appliedAuthorityDocument`), the installation read
(`ReadHostInstallation`), team membership, the derived-role seam, the headless
installation mint and the rule-A hop. No second registry, role table or issuer.

- **Solution roles decide what a person holds through a solution.**
  `business.PersonSolutionRoles` reads the delivered roles: installer at the
  installation the person owns, and the installation's standing role through
  each team it is exposed to that they belong to, bounded by the delivered
  scope ceilings on the solution's own kinds and by its bindings' invoke and
  lookup scopes on the kinds it consumes. A person's `Issue` at such a
  solution is that grant on its own kinds. A hop with a solution on its path
  carries those roles within the binding (with a solution audience's own grant
  met in), and a parent issued at a solution stands while its roles still grant
  what it carries. Organisation roles answer only where no solution on the
  path declares roles; owner, admin and member keep administering the
  organisation.
- **Install takes the solution's role names.** The installation's standing
  role is a host-derived role per organisation, solution and role, outside the
  catalogue; `GrantScope` admits it only at a solution's authority-root node in
  its organisation, and `List` reads the solution's role name back. A fixture
  installation names its `teams:`, granted through `GrantToTeam` once the
  installation admits it. A built-in role whose name equals a solution role
  (an installation made before this change with `viewer`) reads as that role.
- **ListResources is served.** It reads the capability back without spending
  it, requires the action on the kind, and answers from the owner's grants now:
  solution roles where a solution on the path declares them; otherwise every
  resource for an organisation-wide grant, or the ids of grants limited to one
  resource (`WorkContextScopedResourceIDs`). Ids the capability names are cut to
  those. Content-read standing is not enumerated; the per-collection reads that
  own it still answer it.
- **Installation issuance verifies the installation.** The actor's workload
  credential must name the installed solution, the organisation is held to the
  solution's pinned tenant, and the installation must be that solution's own
  active installation there; each refusal is named before any mint.

The shared contract now admits resource-limited scopes by kind and action.
Accounts preserves those limits in Check; modules enforce exact resources and
can ask `ListResources` for the owner's current grant intersection.

The shared suite at its pin (`06d0d3b`) stays at **13 passed, 3 failed,
3 skipped**, plus the 2 local cases. Install with the solution's own role
`viewer` now succeeds (it failed on the standing grant before `GrantScope`
admitted solution roles); the case still fails later on the unacknowledged
pending installation. The installation-issuance cases are not exercised here:
the pinned case issues with no actor credential, which the interface now
refuses, and the newer suite's case (authority `3925878` onwards) needs a
pin move and a harness installation of a solution with its workload. Unit
tests in `pkg/adapters` cover issuance, roles and `ListResources`; no composed
run was made.

## Review r12 repairs (2026-10-25)

Review r12 of #1217 found one blocker and four majors in the interface
adapters; each is fixed where the behaviour lives, with a test that fails
without it.

- **`Check` holds a method to the actor, not the owner.** A named method is held
  to the call's effective scopes (the last hop's grant, the set `ListResources`
  already used), so an actor delegated `read` by an owner holding `read` and
  `write` no longer passes a method requiring `write`
  (`TestCheckHoldsAMethodToWhatTheActorWasDelegated`).
- **A workload's issue is what it asked for, within its binding.** The ceiling is
  the installed binding's headless grant, or for `lookup` the binding's lookup
  scopes within it. An empty ask is the whole ceiling; a scope outside it,
  `invoke`, an undeclared lookup, an audience other than the binding's and a
  negative lifetime are refused by name; a lifetime only shortens (60 s ceiling
  for an operation context, 15 min for the module's own). The narrowed scopes
  are what the issuance audit records, and they stay within the headless grant
  the revision check confirms.
- **Exchange and renewal through `Issue`.** The organisation is forwarded (the
  request's, or the parent's own); an empty exchange ask is the parent's
  effective scopes; at the parent's own audience the issue is a renewal.
- **A workload's own context is reachable.** Every shape is authenticated first
  (bearer, workload or actor credential), and only then is its audience held to
  the composition, so `module-capabilities` is reached and an uncomposed
  audience is still NotFound by name, while a bearer-less caller learns nothing
  of which modules are composed.
- **Minor repairs.** A hop from a parent narrowed to nothing of a kind is refused
  (core signs no such parent today; the rule is held at the unit). A refused
  workload or actor credential names its classified cause. The hop runs the
  audience guards every mint runs, and no binding may be addressed to
  `module-capabilities`. A resource question no longer reports a registered
  scope node of another kind as an id of the asked kind
  (`TestScopedResourceIDsNameOnlyResourcesOfTheAskedKind`); scoped assignments
  are still read as exact ids, as the mint reads them, not expanded as subtrees.
  An applied authority document is verified once per binding generation
  (`TestAnAppliedAuthorityDocumentIsVerifiedOncePerGeneration`), and an
  unparsable applied row no longer fails every other binding's answer.

The shared suite, run as "Current suite baseline" states, gives **15 passed, 3
failed, 3 skipped** of 21 cases, the same as the branch before these repairs:
the installation case (its fixture names no installed solution audience), the
child hop (the person holds nothing under the `documents` binding in this
harness) and install/grant (the installation is still pending) fail before
reaching anything changed here.

## Combined integration on 2026-10-25

The installation REST registration (#1185), source-operation declaration fence
(#1188), and person operation-binding mint (#1219) are merged here with their
original commits and authorship. The REST routes remain product APIs: the host
Connect installation interface does not replace them. The source fence refuses
an admitted declaration that changed before replay or provider dispatch.

The host person adapter now passes `operation_binding_id` to StartTask's
`operation_bindings`, without first expanding an empty ask into unrelated audience
holdings. StartTask's schema requires scopes or binding names; the existing
resolver expands names under its authority span. A binding-only request is valid,
an empty request is refused, and a person's binding mint accepts invoke only.
The regression mints the installed source scope, including its resource ID, and
has the module redeem that parent. Missing personal authority remains a named
refusal. The separate solution-role mint does not yet project named bindings and
now refuses that shape explicitly instead of silently ignoring the binding.

The shared suite remains **13 passed / 3 failed / 3 skipped**, plus **2 passing
local cases**. Forward-only manifests, signed RPC policies and signed HTTP
solution policies pass. The three remaining shared failures are:

- Installation-owned issuance: this harness still supplies no installed-solution
  fixture or its workload credential.
- Child hop: the initial mint now reaches the binding resolver and refuses the
  viewer by name. The resolver retains a scope only when all its actions are
  held at the parent audience; the fixture's combined invoke/read binding does
  not project the person's read holding at the target audience. The next fix
  belongs in that binding/holdings owner, without widening the fixture grants.
- Pending installation exposure: the shared fixture never acknowledges the
  installation and uses the literal team ID `finance`. The host-generated team
  and acknowledgement hooks are still required at the conformance owner; the
  existing PostgreSQL test covers pending refusal, acknowledgement and exposure.

The fourteen missing host procedures in the published API catalog are fixed at
the CLI exporter, source `8de0f6674550fb040f03bc6cb2530116878c8c6b` in CLI #1128.
Complete contract, runnable and SDK regeneration retains the explicit served
surface across packages. The client-service guard now resolves full service names
across that surface and still checks imported-service leaks. Root/tools Core pins
validate the new format; service pins are unchanged, and release tags still
refuse the source CLI candidate. The SDK candidate is 0.3.37.

The newer installation-identity and solution-role commits on the shared branch
are retained. Their moved control-plane read is recorded by regenerating the
existing call-site inventory. All generated conflicts were regenerated through
their owners, including after rebasing; no generated conflict was resolved by
selecting individual output lines. These are owner integration fixes, reusing
existing REST registration, delivery, source dispatch, binding resolution and
minting. They add no parallel policy or authorization store.

Validation of the combined tree: Accounts adapters, business, infra,
permissionsplugin and cataloggen pass; the source-operation and person-binding
race tests pass; `module/tools` passes, including the formerly failing API
catalog test. Accounts/tools vet and the SDK's 107 tests and build pass. The
boundary gate caught consumer-specific names in a newly integrated role test;
those authored fixture names are generic now, and the boundary gate passes.
No live composed ENFORCE run or release is qualified by these checks.

## Fresh-store manifest ceiling (2026-10-25)

The composition path now derives module principals from applied signed manifests
without requiring an administrative envelope. Existing signature, workload-owner,
immutable generation and installation-consent checks remain in force. Changing
an authority document at the same generation is refused. Every authority decision
also fences the registry against current applied generations, so a replica cannot
use a grant from before a narrowing or withdrawal.

`TestFreshStoreDerivesManifestAuthorityAndIssuesAPerson` boots an empty store,
delivers signed module and solution policies through the production owners,
asserts the derived registry and answers a person's Issue and HTTP Check. It
also refuses a changed authority document at the same generation. It runs by
default, independently of the opt-in shared conformance suite.

**A widening needs the organisation's consent.** With no envelope a module
installation records the consented snapshot of the ceiling it was installed
under (`consentedInstallationScope`), not `Follows`, so a later generation that
widens the manifest reaches no organisation that installed an earlier one until
its administrator installs again; a narrowing still reaches it.
`TestAManifestWideningWithNoEnvelopeDoesNotReachExistingInstallations` derives
generations 1 and 2 through the real pass and holds an existing installation to
generation 1's read. Where an envelope is written, a module installation keeps
following it. An installation that already follows its grant holds nothing on
a host with no envelope (`ErrModuleInstallationConsentRequired`, answered
`FAILED_PRECONDITION` with reason `INSTALLATION_CONSENT_REQUIRED`) until its
administrator installs again; `TestAFollowingInstallationNeedsConsentWithNoEnvelope`
covers it.

Reader inventory before deletion: `MODULE_PRINCIPALS` has no runtime reader and
was already removed. `MODULE_AUTHORITY_ENFORCEMENT` was read by `work.go` and
consumed by registry derivation; both the switch and its warn implementation are
removed. `PutModuleEnvelope` remains because the administration UI, its RPC
adapter, signed-envelope delivery, installation scope and revocation, retained
bindings and registry revision fences still use that surface. Where one exists,
its strict restrictions remain; this change does not retire that separate
administrative contract or migrate its installed delegation modes.

## Composition responses belong to one binding

`ApplyManifest` is served **in-cluster only**: the composition's deliverer calls
accounts' Connect listener directly with its module's `delivery` credential,
and the gateway never routes it (`business.ServedInClusterOnly`), exactly as the
HTTP delivery route is kept off the edge. `TestTheGatewayNeverRoutesModuleDelivery`
holds the built and committed route catalogs and the gateway's route table to
that, and the route validator refuses a catalog that carries it.

`ApplyManifest` waits for the reconciliation pass, then answers from its own
binding's committed generation and digest. A failure applying another binding
or converging fixture installations is logged for the operator and cannot turn
an applied caller manifest into an Unavailable response. A caller whose manifest
has not applied receives its own pending reason; missing or unreadable applied
state remains a named refusal. Aggregate errors never enter this response.

`TestApplyCompositionAnswersOnlyItsOwnManifestOutcome` covers unrelated binding
and fixture failures, the caller's own pending refusal, absent state, generation
and digest mismatches, and an unreadable result without another solution's reason.

## Expanded shared suite (2026-10-25)

The shared suite at `aa487dff75ed` runs 45 cases. Against the reviewed adapters
at `a2a9b92f3`, it reports **13 passed / 32 failed / 0 skipped**, plus **2 passing
local cases**. The default fresh-store signed-delivery regression also passes.
This supersedes the external 5/40 result against an older host revision.

Many new cases refuse because the harness has not supplied their required
fixtures: solution roles, installation credentials and lifecycle, resource
grants, child scopes, workload organizations, parent limits and durable
delegation. These are unverified, not passes. Observed runtime failures include
a named parent binding discarding partially held scopes, a synthetic module
installation being used for a solution, and missing session display names.
Method admission now accepts resource-limited grants without changing them;
unit tests retain wrong-kind, wrong-action and missing-policy refusals.

## Parent bindings preserve the person's holdings

A person's named operation binding now projects each action independently at
the binding's target audience. A read-only person no longer loses their read
because the binding also permits invoke. Actions with different resource sets
stay separate; no cross-product of grants is minted. Content-read eligibility
comes only from retained whole-kind reads at that target, and the exact
installed binding identities are checked again before the signed parent leaves.
This reuses the installed binding resolver and existing person authority checks.

The full shared suite now reports **18 passed / 27 failed / 0 skipped**, plus
**2 passing local cases**. Five child cases are newly green: the attenuating
hop and the named refusals for absent, undeclared, wrong-audience and widening
bindings. All targeted StartTask and module-actor tests, including action/resource
pair preservation, pass. The default empty-store signed-delivery regression
also passes with the updated Core dependency. New required fixture cases
listed above remain unverified; no skipped or missing fixture is counted green.

## Empty declarations and workload diagnostics

A manifest declaring a principal but no authority no longer inherits an
administrative envelope's grants. It retains its workload identity and any
existing tenant restriction, but no content, queues, audit namespaces, operation
bindings, artifact policies or cross-tenant permission. The signed manifest is
the ceiling even when an older envelope permits more. Missing content in that
manifest must be declared by its producer, not synthesized by Accounts.

`ModulePrefixOfWorkload` logs every refusal's underlying reason, including
reviewer failures and ambiguous or undeclared workloads. The presented credential
is never a log field and is redacted if a reviewer echoes it in its error.
The authority libraries are pinned to merged revision `c9cef7470d20`.

## The harness supplies every fixture the suite asks for (2026-10-25)

The harness seeds its own fixture, `code/pkg/adapters/testdata/host-interface-conformance.yaml`
(the dev-admin people plus an owner who is not an administrator, a member with
no role, a resource-limited record grant and the module roles), delivers three
solutions through `ApplyManifest`, installs them through the fixture's declared
installations, acknowledges each install step through the host's own claim and
finish, and exposes them to their teams. Only external sources stand in: the
cluster, the composition's module authority (one module withdrawn for the
absent-organization case) and the policy log the uninstall path requires.

Before: 13 of 45 direct cases passed, 32 failed, most of them for a missing
fixture. After: 21 pass, 24 fail, and none fails for a missing fixture; each
remaining red line is host behaviour, listed with its refusal and location in
the coordination worklist. Production wires no policy log, so the lifecycle's
revocation proves the uninstall path, not a deployed host's ability to revoke.
## Signed solution roles respect the manifest ceiling

Signed solution roles are checked before delivery enters the inbox. Every scope
must stay inside a served kind's signed action ceiling or an explicit invoke or
lookup binding; headless operations cannot grant a person's role. A served kind
without an action ceiling retains the shared contract's unrestricted action
vocabulary. Empty or duplicate roles and excess grants are refused by role name.
The check reuses Core's scope-containment rule, including resource attenuation.

With these role checks and the complete harness, the full shared suite at
merged authority `c9cef7470d20` reports **22 passed / 23 failed / 0 skipped**,
plus **2 passing local cases**. The signed role-ceiling refusal is newly green.
The default fresh-store regression also passes. The same merged local authority
passes **45/45** shared cases. Composed ENFORCE remains unqualified: the merged
hop requires authority 0.2.0, while this host advertises 0.1.0, so Codefly refuses
the binding before either policy surface can be redelivered.

## Interface 0.2.0 and the host behaviour the suite reaches (2026-10-25)

The interface protos are re-copied at authority `aa487dff` (authority and
session 0.2.0; composition and installation comments only), `DIGESTS.sha256`
re-recorded, and regenerated through `codefly generate proto`, the catalog
`go generate` steps, `codefly generate contracts`/`runnables` and the SDK's
`generate.mjs`, with the CI's CLI source pin. `module.codefly.yaml` advertises
`codefly.dev/authority@0.2.0` and `codefly.dev/session@0.2.0`.

- **Session display names.** `SignIn` and `Current` fill
  `person_display_name` (the profile name, else the primary email) and
  `organization_display_name` (the organisation's name), each read under its
  own row-level binding (`Service.SessionDisplayNames`). A session in no
  organisation, or whose person left it, names no organisation.
- **An uninstalled audience** is refused before solution roles are read:
  `FailedPrecondition`, `audience "…" is not installed in this organization,
  so there is no installation to seal the capability to`.
- **A delivered solution named by its alias** resolves to the organisation's
  installation of its solution binding, never to a composed module's
  installation (`WorkContextAudienceInstallation`).
- **An installation's issue** proves its actor first, then reads the named
  installation for whose it is (`WorkContextInstallationIdentity`: organisation,
  delivered principal, status). Unknown is `NotFound`; another solution's
  workload, another organisation, or a pending or revoked installation is
  `PermissionDenied` naming why. The organisation is the installation's own.
  An empty ask also considers the actions the solution's roles name.
- **A person at a solution through its binding** carries what the solution's
  roles grant within that binding as installed; beyond it is refused
  (`beyond`), nothing through it is refused (`holds nothing`).
- **An empty ask** carries a kind the person holds only on named resources, on
  exactly those resources.
- **GrantToTeam** refuses a non-active installation as `PermissionDenied`
  naming `pending` or `revoked`.
- Refusal texts now name the interface's reasons (`the owner holds nothing at
  this audience`, `beyond what the owner holds`, `must name the organization
  of the work item`, `already installed`).
- The harness's HTTP-route subtest installs its solution before issuing there:
  it passed only because an uninstalled solution resolved to a synthetic
  installation.

Later the same day, on the coordinator's decisions: a person's ask meets
their holdings (an unheld id is dropped; an action held on nothing the ask
names is refused, "asked beyond what the owner holds"), and a kind a chain
dropped stays dropped (a denial row: the kind and no action, at a person's
root for each audience kind the ask left out and at a hop for each kind the
owner holds through the binding that the child was not granted; limits are
read per kind and per action). The harness names a real team with no member
for the staleness probe, and the suite is pinned at authority PR #8
(`3222e991`).

The shared suite now reports **40 of 45 shared cases passing, 5 failing**
(42/47 with the two harness cases). Still red:

| Case | What the host must change |
| --- | --- |
| a delegation issues as its grantor | served: see "Delegations to a module" below |

## An installation owns its capability as its agent (2026-10-26)

On the coordinator's decision: an installation acting for itself is the
owner of what it is issued. No person is acting, so the human owner of
record is no longer named.

- **The issue.** `forInstallation` mints through the same headless path with
  `ownedByAgent`: the owner is the installation's agent principal, its kind
  is `agent`, `owner_agent_id` is the agent identifier, there is no actor
  hop and no team attribution. The internal `StartInstallationTask` and the
  install step keep their shape (owner of record, agent acting): a person put
  that work in motion.
- **Seal and revision.** The row-backed seal reads let an agent stand in its
  organisation while its installation does (`standsInOrganization`): the
  installation is active, the agent is neither revoked nor disabled, and an
  owner or co-owner is still an organisation admin. The agent then holds the
  organisation's installations and carries the greater of the organisation's
  revision and its own, so core's check revokes the capability when any of
  these ends.
- **Check and ListResources** re-verify an agent-owned capability
  (`requireInstallationStands`). The agent must still act for one active
  installation, that installation's authority must still resolve, and its
  standing grants must still admit what the capability carries. A standing
  grant can narrow without moving any revision, which is why this re-read is
  needed. A removed owner is now `PermissionDenied`, a decision, where it
  used to be `Internal`. That applies to a person who left as well.
- **Hops.** Rule A reads the installation's standing grants the way it reads a
  person's current grants. Through the binding, a child carries what those
  grants admit at the audience, within the agent's kind ceiling. The parent
  is re-verified first. The child stays owned by the agent, and narrowing is
  never recovered.
- **Harness.** The installation the suite refuses at an unrelated module is
  now `conformance-proposals`. `conformance-pages` cannot serve: its viewer
  role grants `example-records:write`, so `documents` is related to it, and
  the local authority would grant that too.

Suite at `3222e991`: **41 of 45 shared pass** (43/47 with the harness cases).
"an installation acts for itself" is green. "an installation owner survives"
now passes the agent-owned root and the first hop's owner, kind, agent,
tenant, task, session and expiry checks. It stops on that hop's
`actor_chain[0].principal_id`, which is the registry uuid (decision 1), so the
suite has not reached the second and third hops yet. The unit test
`TestAHopFromAnInstallationOwnedParentInheritsTheInstallationsStandingGrants`
covers two agent-owned hops.

## A hop's actor is the module's name (2026-10-26)

On the coordinator's decision, a hop through a binding names its actor by the
module's NAME, as binding ids (`<organisation>/<module>/<binding>`) already
name it; the registry id (`ModulePrincipalID`) stays this host's storage
identity.

- **The binding answer.** A module's installed operation binding is answered
  as granted to the module's name; core holds the exercising principal to it
  by equality, so another module (by name or id) cannot exercise it, and the
  host refuses to sign such a hop before core would. The module's own headless
  context (`StartModuleOperationTask`) is still owned by its registry id,
  because the consumer contract (`CheckAuthorizationRevision`) types an owner
  and every subject as a UUID; the seal source answers that context in its
  owner's spelling, exactly when the owner is the binding's module
  (`hostSealSource.grantee`). Moving headless owners to the name too needs that
  contract to accept a module name, which is a regeneration of
  `saas.accounts.v1`, not done here.
- **Seal, epoch and revision answers** resolve a principal to the declared
  module by its name or its registry id (`ModulePrincipalRegistry.RegistryIDOf`),
  locally and on the module authority surface. A module named as a UUID is
  refused at registry derivation (`ValidateModulePrincipalGrant`), so names and
  ids never collide; two manifests asking for one name were already refused.
- **The journal** records a module actor under its registry id (the column is
  typed as an id; a name would have been dropped to NULL).
- **The solution-role path** reads the hop principals as names, which is what
  `PersonSolutionRoles` was always given.
- **The revision check** (`ConfirmModuleHopBinding`, `IsDeclaredModulePrincipal`)
  resolves a hop's module by name.
- Not moved: the source-delegation and installed-content hops and the module's
  own identity capability still carry the registry id; the interface issues
  none of them yet, and every reader above resolves both spellings.

With the installation-owner work above (rebased onto `acf1593c`), the
shared suite reports **43 of 45 shared cases passing** (45/47 with the two
harness cases). "a module tool binding carries the person's solution role",
"an installation owner survives successive module actors" and the first
subcase of "child target projection" are green. The delegation case and the
child-projection delegation subcase followed with delegations, below: the
suite now passes 45 of 45.

## Delegations to a module (2026-10-26)

`Delegate`, `GetDelegation`, `RevokeDelegation` and Issue from a delegation are
served. A delegation is a row of `module_delegations` (migration 51: tenant
scoped, forced RLS, revoked once and never deleted, its grant immutable).

- **Made by the person, in their own right.** `Delegate` and `RevokeDelegation`
  refuse, by name, a resource-confined credential, an identity acting on
  someone's behalf (`X-Act`, or a delegated request identity), an impersonated
  session and any credential that is not an interactive session: the
  credentials every other person mint holds back. The gateway already refuses a
  resource-bound token at these procedures (ext_authz admits one only at its own
  solution, at a declared module route paired with its capability, and at the
  Work Context mint), so the host's refusal is the second layer, not the only one.
- **Bounded by the grantor.** `Delegate` holds each delegated action to what
  the signed-in person holds now, at the module or at an audience one of its
  bindings reaches, by the check every mint makes; beyond that is
  `PermissionDenied` (`delegates what they do not hold`). A purpose past 1024
  characters is `InvalidArgument` by name.
- **Fail closed.** A host that cannot witness a revocation (no policy log is
  wired) records no delegation: `Delegate` is `FailedPrecondition`, "this host
  cannot witness a revocation, so it records no delegation". A delegation is
  therefore never a standing grant that nothing could end.
- **Issue from a delegation** proves the actor's workload, which must be the
  delegation's module (anyone else: `NotFound`), refuses a revoked delegation
  by name, and issues the module's one hop through the binding it names, for
  the grantor (`owner_principal_id`) with the delegation's id on the hop: the
  grantor's current grants through the binding, cut to what was delegated.
  Every later hop of that chain is cut to the delegation again, re-read from the
  store (a revoked or mismatched one refuses the hop), so a kind or a resource
  the person did not delegate is never recovered below it. The chain confirms a
  stored delegation against the hop acting under it by the module's name.
- **Revocation is immediate and scoped to the delegation.** Its grantor or an
  administrator of the organisation may revoke it (anyone else is told there is
  none). The row is stamped under the policy log like every other narrowing.
  Every hop of a chain under a delegation is sealed to
  `<organisation>/<module>/<binding>@<delegation>`
  (`business.DelegatedOperationBindingID`); the binding answer reads the
  delegation live, so a revoked one revokes the binding for exactly those
  capabilities, at every verifier, and nothing else the grantor holds moves.
  Its module reads it as ended through `GetDelegation`, which, like `Issue`,
  admits a bearer-less call whose actor credential authenticates it
  (`business.ShapeAuthenticated`).
- **Audited.** `saas.module_delegation.granted` and
  `saas.module_delegation.revoked` are registered (delegation, grantor, module,
  scopes; the revoker on a revocation), emitted in the transaction that writes
  the row, and declared as the two procedures' audit events; the events
  contribution, event catalog, AsyncAPI and communication documents are
  regenerated.
- **Evidence.** Suite pinned at authority `3222e991`: **45 of 45 shared cases
  pass** (48/48 with the three harness cases). The harness case "a delegation is
  bounded by its grantor and ends with its revocation" covers the bounds, a
  revocation that leaves the grantor's own capability standing, an
  administrator's revocation, the purpose bound and the refusal with no policy
  log; `TestADelegationRefusesACredentialThatIsNotThePersonsOwn` covers each
  refused credential.
- **Production.** Production wires no policy log, so a deployed host refuses
  `Delegate` until one is wired, as it refuses every narrowing.

## Review r93: chains, pins and the signed manifest (2026-10-26)

- **Every hop of a chain is re-read.** A capability seals one binding, its
  last hop's, and core re-reads only that one; a child therefore kept
  verifying while a binding upstream of it narrowed or was withdrawn.
  `ConfirmModuleChain` now holds each module hop to the binding it hopped
  through (its module's binding addressed to the next hop's module, or to the
  capability's audience for the last): the binding must still be installed,
  and every scope the hop carries of a kind that binding governs must still lie
  within it. A kind the chain carried down from an upstream hop is therefore
  held to that hop's binding as it is now (rule A: a narrowing is never
  recovered, and an upstream one takes effect below it). It runs on the host
  interface's `Check`, `ListResources` and on a parent before a hop is issued
  from it, and on `CheckAuthorizationRevision` for every hop before the last
  (the subjects after the owner are read as the actor chain, in order).
- **One tenant-pin rule.** A principal pinned to a tenant is issued
  capabilities in that tenant only, whatever `cross_tenant` says
  (`ModulePrincipalGrant.IssuedIn`). The installation issue used to let a
  pinned solution with `cross_tenant` be issued in another organisation while
  the module mint refused; both now read the one rule. `cross_tenant` keeps
  its one meaning: a principal's installed authority may serve installations
  in other organisations (`moduleAvailableToOrganization`, which the
  source-delegation and installation paths rely on); it never moves where the
  principal itself is issued.
- **With no envelope, only a verified signature grants.** The accepted rule is
  that the signed manifest is the ceiling when no envelope is written. What
  stands in front of the grant is the signature: the applied manifest's
  carrier is re-read from the delivery inbox and verified under the pinned
  keys on every registry pass, and its digest must be the applied
  generation's. The grant is now derived from the verified bytes themselves.
  An unsigned carrier, no carrier, no pinned keys, a key the pins do not hold,
  and an applied document the signature does not cover each grant nothing
  (`TestWithNoEnvelopeOnlyAVerifiedSignatureGrants`,
  `TestWithNoEnvelopeAnUnverifiedManifestGrantsNothing`).
- **Readers of a hop actor beyond the token.** No other service in this
  repository compares a hop actor to a registry id (the auth gateway forwards
  principals without reading them). Inside accounts, the delegated record
  access and readable-sources paths hand a hop's actor to storage as a grant
  subject; a module named by its name is now handed over as its registry id
  there, as the journal already was. The remaining registry-id comparisons are
  on capabilities that still carry the registry id (source delegations,
  installed-content reads, the module's own identity and headless contexts).

### Each hop is held to the scope set it was issued under (2026-10-26)

The chain re-read compared a hop with the union of its binding's invoke,
lookup and headless sets. Lookup and headless lie within invoke by
declaration, so that union was invoke: a lookup hop upstream in a chain kept
standing after the binding's lookup was withdrawn, for as long as invoke
granted the action. The journal now records, for a module's hop through a
binding, the binding it went through and the set it was issued under
(migration 52: `operation_binding_id`, `operation`, both or neither, part of
the hop's content address when present). `ConfirmModuleChain` holds a recorded
hop to exactly that set: narrowing it, or withdrawing it, ends the hop even
while another set of the same binding still grants the action
(`TestAHopIsHeldToTheScopeSetItWasIssuedUnder`). The last hop's own binding is
also re-read by core, whose binding revision moves with any change to it. The
remote binding answer a callee's verifier asks for runs the same re-read and
answers a stale chain as revoked.

Still held by audience and union: a hop the journal holds no binding for (one
recorded before migration 52, or on a host with no journal), and
`CheckAuthorizationRevision`, whose request names no hop, so it cannot read
which set each was issued under; a callee that verifies through this host's
binding answer gets the exact check.

## Review r136 repairs (2026-10-26)

- **B1, one journal row per delegated issue.** The interface names a hop
  issued from a delegation by the delegation's own id, so every issue from
  one delegation carried the same hop id, and the journal (keyed by it, first
  row wins) recorded only the first issue's binding and scope set. Later
  issues were held to it: invoke then lookup left the lookup hop held to
  invoke; lookup then invoke refused a fresh invoke token. A delegated hop is
  now recorded under its own row, the delegation within the task its issue
  opened (a delegation's issue starts a fresh task and every child keeps it),
  and the readers ask for both a hop's own id and that row. The token still
  carries the delegation's id, as the suite requires. Proven against real
  Postgres in the harness ("every issue from one delegation is held to its own
  scope set": both orders, each issue checked and its own row read back); with
  the shared id restored the lookup-then-invoke issue is refused, as reported.
  The unit-test journal now keeps the first row on a conflict, as Postgres does.
- **M2, the shared suite runs in CI, at authority main.** The conformance test
  no longer waits for an environment switch nothing set: it runs wherever the
  package's store-backed tests run (it replaces the fresh-store test, whose
  assertions it contains). The library is pinned at authority main
  (`d091cc7a`, which contains `f688c549`); this moved `codefly-dev/core` to
  `8af4b787`, the version the library requires. Result: every case accounts is
  offered passes, including the forwarded-context cases ("a module
  authenticates the forwarded context against the published keys": expired,
  forged, unsigned, wrong audience). The solution-principal cases are not
  offered: the suite runs them only against a host whose
  `host.installation.v1.Installation` carries `principal_id`, and accounts
  serves the 0.2.0 installation interface, which does not. Passing them needs
  the solution-principal contract (an installation's own principal owning its
  data, owner-aware checking, role grants on the installation) re-vendored and
  built; that is a feature, not a repair, and is not done here.
- **M3, the read oracles answer only about organisations the caller is
  installed in.** `CheckAuthorizationRevision` and `AuthorizeEvidenceRead`
  refuse, before reading anything, an organisation where the calling module
  has no installation, with the same refusal as an organisation that does not
  exist (`PermissionDenied`, "the calling module is not installed in
  organization …").
- **Minors.** Journal and chain-read refusals log the store error. A module
  refused as itself on the host interface (`ErrModuleIdentityDenied`, an
  unbound token) is `Unauthenticated` with the reason; a token the review
  refused outright stays `PermissionDenied`, the code the shared suite holds a
  forged workload credential to. `Delegate` refuses a module not installed in
  the grantor's organisation (`FailedPrecondition`, by name). The store's
  migration runner records every applied migration's name on its version table
  and refuses a database whose recorded names this ledger contradicts (a
  database without recorded names cannot be checked). **Local and validation
  databases created before migrations 46–49 were renumbered to 49–52 must be
  recreated.** The guard cannot identify those unstamped databases; no upgrade
  path is supported, and a migration failure names this remedy. The harness
  holds a listener bound to `:0`, reads its assigned port, and serves the
  generated Connect catalog on that listener until cleanup.


### Review r153: explicit re-consent and actionable refusals

An organization owner/admin's explicit module re-install is approval of the
current ceiling. Both Install and InstallSolution retain the installation and
write its current consent snapshot through the existing transaction; the
revision trigger invalidates the previous consent's credentials and work.
Non-admin requests do not change consent. The host's fixture setup consents at
creation and repairs its declared module installations when reseeded.
`INSTALLATION_CONSENT_REQUIRED` names both install RPCs as the recovery action.

The read oracles and Delegate distinguish a missing installation from missing
consent and unreadable authority. Absent and uninstalled organizations, including
organizations outside the current tenant grant, still answer alike; a moved
tenant's cause is kept in the log. Both sites log the cause without presented
credentials. A rewritten delivery reports the stored and offered manifest,
payload and authority-document digests, so an authority-only change is legible.

CI and library regeneration require released Codefly CLI **v0.1.215**. Runtime
initialization also passed with v0.1.214, which resolves the accounts service's Codefly-owned `egress` dependency.
Older development binaries may report “no configuration found for egress”.

The source-read SDK is generated with v0.1.215 and
`--services ModuleCapabilitiesService`; its contract and facade exclude
unrelated services. The descriptor-closure regression is restored. The
TypeScript library is regenerated with the same CI pin through its owning
script; no generated file is hand-edited or selected out of its output.
