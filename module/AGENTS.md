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

Every Go service here carries **its own `go.mod`** — `accounts`, `auth-gateway`,
`store` and `policy-log` — and `tools` is another, with **no `go.work`** tying them together. So
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

Core is pinned per Go module. The host's four Go services (accounts, auth-gateway,
policy-log, store) run one core release, v0.19.3, which carries the
issuer-side `Authority.ReadBack`. The module agent (the root module),
`module/tools` and the module-authority qualification module stay on released
v0.18.0. Never ship a host release on an untagged pseudo-version. Run
qualification with `GOWORK=off` so a developer workspace cannot mask the pinned
dependency.
Accounts requires Go 1.27.2. All go-grpc services pin released agent 0.1.54,
whose builder uses Go 1.27.2 under `GOTOOLCHAIN=local`; CI uses the same patch.
Go 1.27.0 is affected by [GO-2026-6613](https://pkg.go.dev/vuln/GO-2026-6613).
A source audit under a newer compiler does not qualify the compiler used in the
image: move the owning agent and image contract together, regenerate the recipe,
and check the built executable's toolchain evidence. Never lower the accounts
minimum to accommodate an older builder.
CLI v0.1.185 requires an explicit run-level test outcome from every service
agent. The cache, store and vault manifests pin released Redis 0.0.97,
Postgres 0.0.144 and Vault 0.0.42, which include their owners' outcome fixes.
An empty legacy response is not evidence that tests passed; do not bypass that
check or disable the service's test phase to accept it.

Accounts, auth-gateway, policy-log and store use released SDK v0.4.0;
accounts must not retain the pre-release SDK SHA from the callable-source
development branch. Their listeners resolve allocated
endpoints through `ResolveNetworkInstance`, which selects the delivered or local
endpoint from the runtime environment; the removed `WithDefaultNetwork` switch
must not be replaced with a hard-coded address or port.

## Runtime composition seams

Solutions and composed modules are independently deployed and **self-register at
runtime**: the host learns to render and proxy them with no rebuild. Nothing in
this tree names a specific one — a registered target is data, never a branch, and
the seam must stay generic (repository-root `AGENTS.md`, § Boundaries).

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
from delivered authority inside the module envelope a platform super_admin
writes ([services/accounts/AGENTS.md](./services/accounts/AGENTS.md#the-composed-module-service-principal)).

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
