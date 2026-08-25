# AGENTS.md

Orientation for anyone — human or agent — working in this repository. It links
to the authoritative docs rather than restating them; when this file and a
linked doc disagree, the linked doc wins.

## What this repo is

`saas-starter` is a Codefly **module**: a multi-tenant SaaS backend
(three-layer authorization, Postgres RLS, RBAC, impersonation, audit), an
authenticated Next.js product, a separately deployable marketing site, and the
supporting cache/store/vault/telemetry services. It is published as an
immutable module package that downstream workspaces **compose** (never fork).

- Architecture, service graph, and capability ownership: [MODULE.md](./MODULE.md)
- Feature inventory: [module/FEATURES.md](./module/FEATURES.md)
- First-run walkthrough: [module/GETTING_STARTED.md](./module/GETTING_STARTED.md)

## Repository layout

- `module/` — the canonical module source that ships to consumers. **This is
  the tree the base-integrity tooling and CI run against.**
- `modules/saas-starter` — a symlink to `module/` (the workspace-composed view
  Codefly expects under `modules/<name>/`).
- `module/deployment/topology.bindings.codefly.yaml` — **the source of truth**
  for the service graph and the agent version each service pins.
- `module/services/<svc>/service.codefly.yaml` — **generated** from the
  bindings file. The header says `DO NOT EDIT`; edit the bindings source and
  regenerate instead (see below).
- `main.go` / `gitops.go` — this repo is itself the `saas-starter` **module
  agent** binary; composing the module runs this code to regenerate the
  per-service manifests.
- `agent.codefly.yaml` — the module's own name, publisher, and **release
  version**.

## Running the starter locally

Boots the whole dependency graph (vault → store → cache → telemetry → accounts
→ auth-sidecar → frontend, plus marketing) with a fake-auth fixture:

```bash
codefly run service --fixture dev-admin
```

- Agents resolve from their published GitHub releases by default; add
  `--local-agents` to resolve only from `~/.codefly/agents/` (offline / local
  agent builds).
- No TTY (CI, pipes, MCP) auto-enables `--headless`.
- Docker must be running; `codefly doctor` checks prerequisites and
  `codefly clear` reaps stray processes/containers between runs.

### Passing an environment variable to a service

Three paths, by how permanent the value should be:

- **Ad-hoc, this run only** — `--set <service>:KEY=VAL` (repeatable) injects the
  var into that service's process env for the current run, nothing committed:

  ```bash
  codefly run service --fixture dev-admin \
    --set frontend:FRONTEND_SKIN_DIR=/abs/path/to/skin-dir
  ```

  `--set` splits on the **first** `:` (service) then the **first** `=` (the
  value may itself contain `:` or `=`). **Caveat:** the flag is a comma-split
  list, so a value containing commas is torn into malformed entries — e.g. a
  JSON blob like `FRONTEND_SKIN_JSON={"brand":"x","mode":"dark"}` will not
  survive. Pass a comma-free value (a directory via `FRONTEND_SKIN_DIR`) or use
  one of the paths below. (`--set` is a `codefly run` CLI flag; its authoritative
  reference lives in the CLI, not this repo.)
- **Committed, portable** — a **configuration group** under `configurations/`,
  wired to the service in the deployment bindings. Use this for any value that
  should travel with the repo. See
  [module/deployment/README.md](./module/deployment/README.md#configuration--environment-variables)
  for the layout, the `workspace_configuration_dependencies` wiring, and the
  full add-a-var loop.
- **Machine-local, uncommitted (frontend only)** — a `.env*.local` file in
  `module/services/frontend/code/` is auto-loaded by `next dev` (the frontend's
  `local` execution profile) and is gitignored. Handy for a value you want off
  the command line but out of git, with no bindings/manifest churn.

Rule of thumb: `--set` for a one-off, a configuration group for anything
committed, and (in a deployed environment) a mounted ConfigMap for production —
the same tiering the skin resolver follows (`FRONTEND_SKIN_JSON` for a quick
inline descriptor, `FRONTEND_SKIN_DIR` for a mounted directory).

For a real external identity provider (WorkOS) and the production-grade
provider stack (Stripe, Resend, PostHog, Sentry, OTEL, Turnstile), use the
`local-dogfood` environment and the setup scripts:

- Runnable local product: [LOCAL_DOGFOODING.md](./LOCAL_DOGFOODING.md)
- Feature-by-feature dogfood checklist: [DOGFOODING.md](./DOGFOODING.md)
- Provider bootstrap scripts: [scripts/setup/README.md](./scripts/setup/README.md)

## Building, testing, and CI

- Canonical gate — everything CI enforces: `codefly ci run`. It owns lint,
  compile/typecheck, tests, dependency/vuln audit, SBOM, and container build.
  See [RELEASE_GATES.md](./RELEASE_GATES.md).
- Go module checks: `go build ./...` and `go test ./...`.
- The one repo-specific CI gate is base-file integrity (below).

## Agent version pins

Each service pins the version of its Codefly service agent (`go-grpc`,
`nextjs`, `redis`, `postgres`, `vault`). To see what is pinned and whether a
newer release exists:

```bash
codefly agent list        # PINNED vs LATEST-RESOLVABLE, resolvability, how far behind
codefly agent versions <agent>
```

To change a pin, **edit the version in
`module/deployment/topology.bindings.codefly.yaml`** (the source), then
regenerate the per-service manifests and refresh the base manifest. Do not
hand-edit `service.codefly.yaml` — it is generated. `codefly update workspace`
does **not** rewrite the bindings for this repo (it skips the generated
manifests by design), so the bindings edit is manual.

Latest is not always safe: verify the newer agent actually boots the graph
(`codefly run service`) before pinning it. Agent releases can carry breaking
changes to service manifests.

## Base-file integrity manifest — the easy gate to trip

`module/tools/base-manifest.json` hashes every base file. Editing a tracked
base file (including `topology.bindings.codefly.yaml`) without refreshing the
manifest fails two CI checks ("Base manifest integrity" and "Codefly CI").
Regenerate it **from a clean checkout**, because `gen`'s tree walk otherwise
hashes gitignored harness artifacts CI never sees:

```bash
git worktree add --detach /tmp/bm-clean HEAD
cd /tmp/bm-clean/module && node tools/base-integrity.mjs gen && node tools/base-integrity.mjs verify
# copy module/tools/base-manifest.json back, confirm the diff is only your files, commit
git worktree remove /tmp/bm-clean --force
```

## Cutting a release

A release is a version bump on `agent.codefly.yaml` (`version: 0.0.N`) landed on
`main`, then a matching `vX.Y.Z` tag. The tag triggers the immutable
module-package publication job (strict manifest validation, SBOM, provenance
signing). Consumers then `codefly sync module` onto the new tag.

## Doc index

Deep references live under `module/` — authorization
([module/AUTHORIZATION_CATALOG.md](./module/AUTHORIZATION_CATALOG.md),
[AUTHZ.md](./AUTHZ.md)), database/RLS
([module/DATABASE_AUTHORITY.md](./module/DATABASE_AUTHORITY.md)), the approval
primitive design ([APPROVALS_DESIGN.md](./APPROVALS_DESIGN.md)), deployment
topology ([module/DEPLOYMENT_TOPOLOGY.md](./module/DEPLOYMENT_TOPOLOGY.md)),
frontend ([FRONTEND_ARCHITECTURE.md](./FRONTEND_ARCHITECTURE.md),
[module/FRONTEND_PLUGINS.md](./module/FRONTEND_PLUGINS.md)), REST/gateway
([module/REST_SURFACE.md](./module/REST_SURFACE.md),
[module/GATEWAY_ROUTES.md](./module/GATEWAY_ROUTES.md)), email
([EMAIL_PROVIDER_ADAPTERS.md](./EMAIL_PROVIDER_ADAPTERS.md)), supply chain
([SUPPLY_CHAIN_SECURITY.md](./SUPPLY_CHAIN_SECURITY.md)), security review and
hardening ([SECURITY_REVIEW.md](./SECURITY_REVIEW.md),
[SECURITY_HARDENING_PLAN.md](./SECURITY_HARDENING_PLAN.md)), and production
readiness ([PRODUCTION_READY.md](./PRODUCTION_READY.md)), and a cross-domain
platform-functionality reference mapping an external multi-tenant-platform audit
to what this starter ships, partially ships, or lacks
([PLATFORM_REFERENCE.md](./PLATFORM_REFERENCE.md)). Start from
[MODULE.md](./MODULE.md), whose "Quick links" section indexes the full set.
