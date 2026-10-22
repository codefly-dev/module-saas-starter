# The host interfaces, served by accounts

**What we are doing.** accounts implements the four host interfaces declared in
obin-ai/authority (`obin.ai/authority`, `composition`, `installation`, `session`)
as adapters over what it already does, so the same contract the mini gateway
serves on a laptop is served here on a cell, and obin-ai/authority's
`pkg/conformance` can prove the two agree. Rung 1 of the validation ladder.

## What is on this branch

- `proto/obin/**`: the interface protos, copied unchanged from obin-ai/authority;
  `proto/buf.gen.yaml` generates them beside `saas` (`code/pkg/gen/obin/**`).
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

1. **The service catalog assumes one proto package.** `business.CompileServiceCatalog`
   sets `catalog.ApiPackage` from the first procedure and refuses the next package
   (`service_catalog.go:56-60`, and the validation at `:253`). Four interfaces are
   four packages. The catalog, its proto (`ApiPackage`/`ApiVersion`) and the
   generators that read it need a package per service. Until then
   `cmd/service-catalog` fails, the registrations cannot be regenerated, and the
   drift tests (`TestServiceCatalog*`, `TestCatalogConnectRegistration*`,
   `TestConnectRegistrationIsDeterministicAndCurrent`, `TestAPIContractSurfaces*`,
   `TestRPCPolicyInventory*`) are red. This is the first change the branch needs.
2. **Connect bindings are keyed by short service name.** `obin.installation.v1.
   InstallationService` collides with `saas.accounts.v1.InstallationService`;
   the installation interface is therefore not bound yet. Key by full name.
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
7. **The conformance harness** needs accounts booted with its store (the test
   Postgres each package persists) and the fixture people; the suite takes a
   `grpc.ClientConnInterface`, so an in-process server over bufconn is enough once
   registration works.

## How to run the suite, once 1 and 2 are closed

Import `github.com/obin-ai/authority/.../pkg/conformance` (or vendor the one
file), boot accounts in-process, dial it, and call `conformance.Run(t, conn,
conformance.Fixture{...})` with the saas fixture's Acme people. Red is the gap
list; green is the claim that the laptop and the cell implement one contract.
