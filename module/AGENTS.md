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
[services/README.md](./services/README.md) introduces the eight services.


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
`store`, `telemetry` — and `tools` is another, with **no `go.work`** tying them
together. So `go test ./...` only ever covers the module you run it in, and a
service's suite is outside whatever you run from the tree root. The canonical
repository adds its own root module (the module agent, the host, and the generated
reference composition), for six in total; a consumer's root module is its own.

To exercise a service, run its suite from its own directory — its DB-backed
suites need Codefly and Docker — or let `codefly ci run` do it.

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
consumer workspace — `legal`, `identity`, `internal-auth`, `federation`,
`module-capabilities`, and the rest. A consumer overrides a group only by
declaring one of the same name.

**Do not generalise about how an unset value behaves — read the group.** The three
digest maps in `federation` (`MODULE_REGISTRATION_SECRETS`,
`MODULE_IDENTITY_SECRETS`, `SOLUTION_REGISTRATION_SECRETS`) each fail closed when
empty, and say so in the file: only the digest lives on this side, the plaintext
goes to whoever presents it, and empty means nobody may register or obtain a Work
Context. That is a property of those maps, not of configuration here.

Other groups ship a **working default**, which is the opposite posture: `local`
carries `CODEFLY_INTERNAL_TOKEN=local-dev-only-replace-me` in
`internal-auth.secret.env` and `IDENTITY_SIGNUP_MODE=open` in `identity.env`.
Nothing rejects a shipped placeholder that reaches a real deployment, so an
unprovisioned group is not a denied one — it is a live credential with a value
everybody knows.
