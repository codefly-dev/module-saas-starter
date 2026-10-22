# The host interfaces, served by accounts

**What we are doing.** accounts implements the four host interfaces declared in
the authority interface repository (`codefly.dev/authority`, `composition`, `installation`, `session`)
as adapters over what it already does, so the same contract the mini gateway
serves on a laptop is served here on a cell, and the authority interface repository's
`pkg/conformance` can prove the two agree. Rung 1 of the validation ladder.

## What is on this branch

- `proto/host/**`: the interface protos, copied unchanged from the authority interface repository;
  `proto/buf.gen.yaml` generates them beside `saas` (`code/pkg/gen/host/**`).
- `module.codefly.yaml`: the `authority` endpoint declares `implements` for the four.
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

## The first run of the suite against accounts (2026-10-22)

`HOST_INTERFACE_CONFORMANCE=1 go test ./pkg/adapters/ -run TestAccountsPassesTheHostInterfaceConformanceSuite`
boots the test store, seeds the dev-admin fixture, configures the Work Context
authority against the store, serves the Connect listener on an ephemeral port
and runs the suite over gRPC. Green: Keys; sign-in and an unknown token refused
by name. Red, each a line of the gap list:

| Red line | Which gap | Proper fix |
|---|---|---|
| person-owned Issue: `audience "documents" is not installed in this organization` | **10, new**: the host requires a module installation per organisation before a module may be an audience; the fixture installs none and the mini gateway holds one per organisation implicitly | the contract decides whether a composed module is installed at every organisation by composition (the mini gateway's reading) or by an explicit module installation; the host's fixture then installs the composed modules at Acme |
| empty ask: `authority_scopes: must contain at least 1 item(s)` | **11, new**: the host cannot answer "everything the owner holds at the audience"; it has no effective-permissions read | an effective-permissions read in the host (person × audience module → scopes), which the adapter uses when the ask is empty |
| uncomposed audience refused as PermissionDenied "not one this host serves", contract says NotFound | **12, new**: refusal code for an unknown audience | the adapter checks the audience against the module registry before the mint and refuses NotFound by name; or the contract adopts PermissionDenied; one or the other, written down |
| workload Issue: `authentication required` | 3 | the broker's attestation behind `Issue`: a workload credential in the request is the authentication |
| `installed solution = <nil>` | fixture: the dev-admin fixture installs no solution | the fixture gains an `installations` section performed through the real install path (primer decision 6) |
| ApplyManifest: `internal RPC is not exposed on the tenant listener` | 4 | the delivery path behind the composition interface, on the internal listener; the harness dials it there |
| Install, GrantToTeam | 5 | not reached yet: the install subtest applies a manifest first |

Rule held: the suite was changed three times today, each time to state the
contract more precisely (an authenticated caller for the audience probe; explicit
scopes in base cases with the empty ask as its own line; List as an administrator's
read), never to make a red line green.
