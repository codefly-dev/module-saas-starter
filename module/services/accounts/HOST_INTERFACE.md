# The host interfaces, served by accounts

**What we are doing.** accounts implements the four host interfaces declared in
the authority interface repository (`codefly.dev/authority`, `composition`, `installation`, `session`)
as adapters over what it already does, so the same contract the mini gateway
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
  `StartInstallationTask`; a parent token → `StartChildSession` with an actor,
  `ExchangeAudience` without. `List` → `ListInstallations`. `SignIn` → fixture
  `Authenticate`, returning the access token's session id.
- `code/pkg/adapters/connect_handlers_host_interface.go` + `connect_bindings.yaml`:
  the Connect handlers and their bindings, in the shape every other service uses.

## The gap list (red on this branch until each is closed)

1. **Closed.** The service catalog carries one `api_package` (the service's own) and the `interface_packages` it implements beside it (`catalog.proto` field 9; `service_catalog.go`); the frontend catalog, the gateway route aliases and the mesh gates read the API package only. Four tests that hard-coded one package now read the served set.
2. **Closed.** `cmd/connect-registration` resolves a binding by full service name first and short name otherwise, and imports each implemented package's generated Go and Connect packages under derived aliases; `connect_bindings.yaml` keys the four interface services by full name. The authorization matrix counts services by full name.
3. **Workload-owned Issue** is minted on the gateway's workload endpoint
   (`/modules/_operation-context`) from a pod token and an installed binding, not
   on an accounts RPC. Moving the broker's attestation behind `Issue` is the fix;
   the adapter returns Unimplemented naming it.
4. **ApplyManifest**: accounts learns a module through signed delivery at
   `/platform/_delivery/manifest`; the interface's `ModuleManifestV1` is the proto
   form of that document. The delivery path must accept it, with the ceiling read
   from the manifest and not from the envelope. Unimplemented, named.
5. **Install and GrantToTeam** name the target and the role; accounts installs by
   target id, agent identifier and role id. A resolver from the applied presence
   and the role catalogue is needed. Unimplemented, named.
6. **No exposure audit event** exists; `GrantToTeam`'s policy borrows
   `saas.installation.created` until one is registered.
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
  `business.ComposedModuleInstallations` answers the live module authority and
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
  `module-operation/<principal>/<binding>` from the installed grant and the
  operation-task mint seals it. Until now the token carried no binding, which is
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

**Open decision on core (gap 14):** a hop keeps the owner's seal installation
and core requires a binding named on it to be granted within that installation;
a module's binding is granted within the module's own installation. So a
cross-module hop cannot name the binding it exercises today; the host carries it
out of band in the revision check's request. The suite does not ask the hop to
name one until core decides (authority README, "Open decisions").

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
  the composing workspace's own `fixtures/` adds them and assigns them by
  email, which fixture `assignments` now accept beside `agent_identifier`.
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
  in a real run; the harness alone wires `ComposedModuleInstallations`.

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
| fail | a child hop names its actor and binding | the person's token naming a binding is #1219; the hop naming its binding waits on core (gap 14) |
| fail | a manifest moves only forward | ApplyManifest is internal and the harness dials the tenant listener (gap 4) |
| fail | install pending, then exposed | blocked by the same delivery refusal (gaps 4, 5) |
| skip | callee refuses what does not verify | needs an installation whose grant can move |
| skip | delegate chain; method policy; resources; lookup; delegation | the harness states no case: each needs a host capability above (gap 4, ListResources, delegation) |

### What was not verified

No composed run: platform-obin, a playground and the edge/hop against a live
accounts were not started. The fixture people's sign-in and teams were proven
through the seeded harness store, not a `codefly run`. `codefly generate
contracts saas-starter --check` fails on this branch, before and after this
change: the CLI refuses a contract endpoint whose proto declares services in
more than one package (`cli cmd/generate/contracts.go:518`), so the exported
contracts do not yet carry the `host.*` services; that is a CLI gap. The
frontend fixture schema change was checked against the fixture with zod
directly, not through the frontend's vitest suite (no installed dependencies).

