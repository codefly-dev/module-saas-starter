# AGENTS.md — the module tree

This is the canonical module source that **ships to consumers**, composed into
each one as a copy. Two consequences govern every edit here:

- **What ships is the tree.** The module is released as an immutable package of
  this directory; there is no second record of its files to keep fresh.
- **A consumer cannot edit what you write here** — it may only add files
  alongside. Anything a consumer must be able to vary belongs in a configuration
  group or an extension point, not in a file they would have to fork.

Read the repository-root `AGENTS.md` first for ownership, boundaries and the
behavioural rules (in a consumer, read your own).
[SERVICE_CATALOG.md](./SERVICE_CATALOG.md) and
[DEPLOYMENT_TOPOLOGY.md](./DEPLOYMENT_TOPOLOGY.md) describe the graph;
[services/README.md](./services/README.md) introduces the services; `module.codefly.yaml` is the inventory they are counted from, so neither doc states a number.


## Generated files are never edited by hand

This tree is full of generated output — `pkg/gen/`, `generated/`, the catalogs,
the TypeScript under `services/frontend/code/src/gen/`. **None of it is ever
edited, and none of it is ever partially kept.** Running a generator and then
reverting the files you did not want is the same mistake as editing them: the
result is a tree no one can reproduce.

Regenerate with the documented command for the service, run the whole chain, and
require a clean `git status`. If the chain cannot reproduce what is committed,
that is a bug in the generator, its pins, or its inputs: fix one of those, or
file a precise issue against the tool that owns it and say in the PR body that
you could not regenerate cleanly. Never keep some of what a generator wrote and
discard the rest — a curated generated tree is a broken generator, reported as
such, never a tidied diff.

Before changing any proto or generator input, run the generator with no change
at all and require a clean tree. That is what separates "my change churned the
tree" from "my toolchain does not match CI".

## The manifests are the model

`module.codefly.yaml` and every `services/<svc>/service.codefly.yaml` are
**authored**. A service manifest is the only place its agent is named, and the
deployment facts no Codefly field carries — endpoint ports, public egress,
cluster-internal HTTP routes, bootstrap Jobs, Kubernetes identity — sit in the
same manifest under `spec.deployment`. Nothing renders these files and no second
document restates them. `codefly update workspace` is the one command that
moves an agent version; [deployment/AGENTS.md](./deployment/AGENTS.md) covers
what to check before trusting a newer one.

The rule for everything else is the header: wherever a file declares `DO NOT
EDIT` — generated Go, the contracts under `contracts/`, the projections under
`deployment/generated/` and the policy goldens that `go generate
./pkg/cataloggen` renders from the manifests — regenerate rather than patch the
output.

Docs that *describe* a generated surface are a different thing and are
hand-maintained — [REST_SURFACE.md](./REST_SURFACE.md) is written, not emitted, so
adding a REST-enabled RPC means editing it. Do not go looking for a generator.

## Go suites

Every Go service here carries **its own `go.mod`** — `accounts`, `auth-gateway`
and `store` — and `tools` is another, with **no `go.work`** tying them together. So
`go test ./...` only ever covers the module you run it in, and a service's suite
is outside whatever you run from the tree root. The canonical repository adds its
own root module (the module agent, the host, and the generated reference
composition); a consumer's root module is its own.
`libraries/source-read-sdk/go` and `qualification/module-authority` are two more
modules, outside this tree, that CI exercises in steps of their own.

To exercise a service, run its suite from its own directory — its DB-backed
suites need Codefly and Docker — or let `codefly ci run` do it.

The recurring worker-heartbeat test uses Go's `testing/synctest` virtual clock;
its assertions must not depend on wall-clock scheduling under parallel CI load.
The module-authority qualification helper models both the declared cross-tenant
ceiling and current installation consent; a person's source delegation alone
must never substitute for either. Its runtime credential and delegated parent
must also name the same installation organization.

Dependency-backed tests use Core's default isolated control channel. With CLI
v0.1.185, keep the SDK session suffix on each package's naming scope: it gives
each invocation fresh database state. Do not opt into `WithSharedControlChannel`,
which is only for older CLIs and reuses fixed local database directories across
otherwise independent runs and migration revisions.

Core is pinned per Go module. The module agent, tooling, frontend payload,
accounts, auth-gateway and store select released Core **v0.19.7**, which includes
the earlier lifecycle and multi-package contract candidates. Accounts and
auth-gateway use released SDK v0.4.1, which owns tracing startup and shutdown.
Never ship a host release on an untagged Core candidate. Run qualification with
`GOWORK=off` so a workspace cannot mask the pins.
Accounts requires Go 1.27.3. All go-grpc services pin released agent 0.1.58,
whose builder uses Go 1.27.3 under `GOTOOLCHAIN=local`; CI uses the same patch.
Go 1.27.0 is affected by [GO-2026-6613](https://pkg.go.dev/vuln/GO-2026-6613),
and Go 1.27.2 by [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809).
A source audit under a newer compiler does not qualify the compiler used in the
image: move the owning agent and image contract together, regenerate the recipe,
and check the built executable's toolchain evidence. Never lower the accounts
minimum to accommodate an older builder.
CLI v0.1.185 requires an explicit run-level test outcome from every service
agent. The cache, store and vault manifests pin released Redis 0.0.100,
Postgres 0.0.146 and Vault 0.0.44, which include their owners' outcome fixes.
Vault 0.0.44 also corrects its owner's runtime image digest; the agent resolves
that image, so the module pins the released agent rather than restating the digest.
An empty legacy response is not evidence that tests passed; do not bypass that
check or disable the service's test phase to accept it.

CI selects released Codefly CLI **v0.1.221** in
`scripts/ci/install-codefly.sh` for every planner/executor job. Use that release
for local runtime qualification too: the accounts service declares Codefly's
`egress` configuration producer, which older development binaries may not
resolve, and auth-gateway declares `hop-routes`, which a release before 0.1.219
withheld entirely from a composition naming no module behind the hop instead of
delivering it empty. The CLI exports the host interfaces alongside the owned API
in one Connect contract. Released Core v0.19.7 in the module agent and `module/tools` validates that
multi-package format.

Accounts and auth-gateway use released SDK v0.4.1; other services retain their
own released pins.
Their listeners resolve allocated
endpoints through `ResolveNetworkInstance`, which selects the delivered or local
endpoint from the runtime environment; the removed `WithDefaultNetwork` switch
must not be replaced with a hard-coded address or port.

The shared `frontendpayload` Go module is an additional independent suite:
`cd module/frontendpayload && GOWORK=off go test -race ./... && go vet ./...`.
Its CI step verifies preparation separately from the service consumers.

## Runtime composition seams

Solutions and composed modules are independently deployed. Signed delivery
updates their presence; the host projects optional pages and resolves routes through
the SDK. Organisation installation grants remain separate from shared deployment,
and idle runtime credential renewal does not establish presence. A declared target
is data, never a branch (repository-root `AGENTS.md`, § Boundaries).

The depth sits with the code that owns each half:

- [services/auth-gateway/AGENTS.md](./services/auth-gateway/AGENTS.md) — upstream
  registration, the composed-module REST prefix, and the credentials both take.
- [services/accounts/AGENTS.md](./services/accounts/AGENTS.md) — the durable
  registry record, module principals, and minting a module Work Context.
- [services/frontend/AGENTS.md](./services/frontend/AGENTS.md) — the registration
  route, the public and internal projections, remote loading and CSP.

Design records: [SOLUTION_REGISTRATION.md](./SOLUTION_REGISTRATION.md),
[MODULE_INSTALLATION.md](./MODULE_INSTALLATION.md),
[WORK_CONTEXTS.md](./WORK_CONTEXTS.md),
[TRUST_CAPABILITIES.md](./TRUST_CAPABILITIES.md).

## Configuration

`configurations/<env>/*` declares the groups composition provisions into a
consumer workspace — `legal`, `identity`, `internal-auth`, `federation`, and
the rest. A consumer overrides a group only by declaring one of the same name.
No group declares module authority: the module principal registry is derived
from applied signed manifests; an existing administrative envelope can narrow
them ([services/accounts/AGENTS.md](./services/accounts/AGENTS.md#the-composed-module-service-principal)).

**A credential's blast radius is the set of services that DECLARE its group**, not
the set that reads it: a declared group is injected whether anything reads it or
not. So which group a credential lives in is the security decision, and
`module/tools/credential_group_scope_test.go` holds both halves of it — which
services may declare each credential-bearing group, and that the
gateway-provenance key appears in no group but `gateway-trust`.

That is why the identity provider's own credentials live in `identity-provider`
rather than in `identity`: the frontend declares `identity` to render the login
page from its public facts (issuer, client id, display name, scope), so a
deployment putting the provider's client secret and management key there hands a
public-facing process credentials it never reads. Only accounts declares
`identity-provider`.

**Do not generalise about how an unset value behaves — read the group.**
`federation` fails closed when its declared-presence keys are empty: no
coordinate means no delivery surface and no execution binding, so no module may
obtain a Work Context. It declares no module or solution secret — those three
digest maps are deleted (#1056); a module proves its identity with its pod's
projected ServiceAccount token. That is a property of that group, not of
configuration here.

Other groups ship a **working default**, which is the opposite posture: `local`
carries `CODEFLY_INTERNAL_TOKEN=local-dev-only-replace-me` in
`internal-auth.secret.env` and `IDENTITY_SIGNUP_MODE=open` in `identity.env`.

For the credentials that decide **perimeter membership** — the cluster-internal
token and the gateway-provenance token — a shipped default no longer reaches a
deployed runtime: accounts and `auth-gateway` each refuse to start when one
carries a marker from the same list the shipping gate requires of every secret
default, or is shorter than 32 characters, and the refusal names the
group-qualified key. The marker lists and the length floor live in
`shipped_placeholder.go` beside each service's entrypoint, and
`module/tools/shipped_placeholder_lockstep_test.go` holds all three copies
identical — one added on only one side leaves either a published value a cell
accepts or a default that side can no longer boot on.

Every **other** group still ships a working default that a deployed runtime will
use: an unprovisioned optional group is not a denied one. Where that matters for a
credential, the loader's own "is this required" check is the place to say so.

**A key the group does not declare is inert, which is worse than absent.** A
composition supplies values for declared keys; it cannot declare one. So a
`workspaceEnv(group, KEY)` whose KEY no `configurations/local/<group>.env`
assigns returns nothing on every deployment, while reading as wired — the
operator sets the value, the platform carries none, and nothing errors.
`security`/`TRUSTED_PROXY_CIDRS` was that for as long as accounts had per-IP
rate limiting. Both halves are gated in
`module/tools/workspace_configuration_dependencies_test.go`, over every
service's Go and TypeScript source: every group a service reads is declared in
its manifest, and every literal key it reads is declared in that group's
default. An undeclared GROUP is the same failure one level up — the frontend
read `application`/`APP_BASE_URL` undeclared and presented its cluster-internal
address to accounts as its public origin. [configurations/README.md](./configurations/README.md)
carries the rule and the few pairs that knowingly stay inert.

**A group is required by the BACKEND SELECTION, not by a workload's presence.**
accounts declares the `vault` group to *reach* the vault key service — an
address, an auth method, an AppRole mount, and the credential in its secret
half. What makes that binding necessary is the backend the environment selected
(`KEY_SERVICE_BACKEND` at runtime, `key-service:` on the environment at render
time), so that is what the module agent asks before declaring it: a composition
whose every environment selects the cell's cloud key service stops answering for
`VAULT_AUTH_METHOD` and the AppRole pair of a Vault it does not use, and one
where any environment selects the vault backend keeps the group.

Keying it off the `vault` service's presence gave the same answer for this
module — the only thing that service is for IS that backend — and the wrong one
as soon as the two part: a cell whose Vault is the **cell's**, composed here as a
managed `cell-vault`, renders no Vault of this module's and needs every key in
the group. The selection is resolved once, in `gitops.go`'s `resolveKeyService`,
and read by both the manifest edit and the overlay render, so the group a service
declares and the workloads an environment schedules cannot disagree.

The grain is the composition, not the environment: a group is declared in one
service manifest for the whole composition, so it is withdrawn only when **no**
environment selects the backend it reaches. A composition that runs this
module's Vault locally and its cell's cloud key service in the cell therefore
keeps the group — the local run needs it — and accounts answers for the mismatch
at boot instead: with a vault backend selected and the group carrying nothing,
it refuses naming the group and the selection rather than blaming the first key
read out of it (`requireVaultGroup`).

Whether a Vault is composed **per environment** is a declaration in the
composing workspace's manifest rather than here — `key-service: vault | kms`,
where `kms` composes no Vault at all. The module agent implements it; the CLI
does not yet parse the field, so the live route for a composition with no Vault
anywhere is the service inventory, which the agent reads as the same selection.
Neither route reaches a composition that pins a published release of this
module — see [deployment/README.md](./deployment/README.md), "Composing no
Vault".
