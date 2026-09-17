# AGENTS.md — the module tree

This is the canonical module source that **ships to consumers**, composed into
each one as a copy. Two consequences govern every edit here:

- **Base-file integrity applies.** `tools/base-manifest.json` hashes every file in
  this tree; editing one without refreshing the manifest fails CI. Use the
  `refresh-base-manifest` skill.
- **A consumer cannot edit what you write here** — it may only add files
  alongside. Anything a consumer must be able to vary belongs in a configuration
  group or an extension point, not in a file they would have to fork.

Read the root [AGENTS.md](../AGENTS.md) first for ownership, boundaries and the
behavioural rules. [SERVICE_CATALOG.md](./SERVICE_CATALOG.md) and
[DEPLOYMENT_TOPOLOGY.md](./DEPLOYMENT_TOPOLOGY.md) describe the graph;
[services/README.md](./services/README.md) introduces the eight services.

## Generated files, and their source

`deployment/topology.bindings.codefly.yaml` is **the source of truth** for the
service graph and the agent version each service pins.

`services/<svc>/service.codefly.yaml` is **generated** from it — the header says
`DO NOT EDIT`. Edit the bindings file and regenerate; a hand-edit is lost on the
next composition and silently drifts the two apart. Changing a pinned agent
version is the `pin-a-service-agent` skill.

The same rule holds wherever a header declares it: generated Go, generated
contracts under `contracts/`, the generated mesh policies under
`deployment/generated/`, and the REST projection in
[REST_SURFACE.md](./REST_SURFACE.md). Regenerate rather than patch the output; a
gate exists for most of them and will catch a hand-edit.

## Go suites

This repository holds **six independent Go modules** — the root module,
`module/tools`, and one per Go service (`accounts`, `auth-gateway`, `store`,
`telemetry`) — and there is **no `go.work`**, so `go test ./...` covers only the
module you run it in. From the repository root that is the module agent, the host,
and the generated reference composition; every service's own module is outside it,
because each has its own `go.mod`.

To exercise a service, run its suite from its own directory — its DB-backed
suites need Codefly and Docker — or let `codefly ci run` do it.

## Runtime composition seams

Solutions and composed modules are independently deployed and **self-register at
runtime**: the host learns to render and proxy them with no rebuild. Nothing in
this tree names a specific one — a registered target is data, never a branch, and
the seam must stay generic (root [AGENTS.md](../AGENTS.md) § Boundaries).

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
consumer workspace — `legal`, `identity`, `internal-auth`, `federation`,
`module-capabilities`, and the rest. A consumer overrides a group only by
declaring one of the same name. A secret's **digest** belongs to this host's
group; its plaintext belongs to whoever presents it, and the two are provisioned
separately on purpose. An unset secret map means the capability it guards is
denied to everyone — fail closed, never open.
