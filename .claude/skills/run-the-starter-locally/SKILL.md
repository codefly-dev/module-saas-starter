---
name: run-the-starter-locally
description: Boot this module's whole service graph on a developer machine — with the fake-auth fixture, with the real provider stack, or underneath a solution being developed against it — and diagnose a run that will not come up. Use when asked to run, start, boot, serve, or reproduce something against the starter locally, when `codefly run` fails or leaves stray processes behind, or when a service appears to start but is missing from the graph.
---

# Running the starter locally

The graph is vault → store → cache → accounts → auth-gateway → frontend, plus
marketing. One command boots all of it with a fake-auth fixture:

```bash
codefly run service --fixture dev-admin
```

- Agents resolve from their published GitHub releases by default; add
  `--local-agents` to resolve only from `~/.codefly/agents/` (offline, or when
  testing a local agent build).
- To qualify published agent pins independently of a developer's populated
  cache, use `codefly --plugin-path <empty-task-cache> ci run ...`. Codefly
  downloads the selected release agents there. Preserve the shared cache;
  a local development binary installed under a release filename is not proof
  of the released image recipe.
- No TTY (CI, pipes, MCP) auto-enables `--headless`.
- Docker must be running. `codefly doctor` checks prerequisites; `codefly clear`
  reaps stray processes and containers between runs.
- The module ships no collector. Standard OpenTelemetry process variables
  select export: `OTEL_EXPORTER_OTLP_ENDPOINT` names the collector, with
  `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` supplying identity.
  CLIs implementing [CLI #1097](https://github.com/codefly-dev/cli/pull/1097)
  deliver these for every service through an environment's optional `telemetry:`
  block, pointing at the HTTPS operations intake.
  Without an endpoint, SDK Init enables no tracing, including for `local`.
  No module configuration group is required.

For a real external identity provider and the production-grade provider stack,
use the `local-dogfood` environment and its configuration groups:

- [LOCAL_DOGFOODING.md](../../../LOCAL_DOGFOODING.md) — runnable local product
- [DOGFOODING.md](../../../DOGFOODING.md) — feature-by-feature checklist
- [LOCAL_DOGFOODING.md § Configure providers through Codefly](../../../LOCAL_DOGFOODING.md#configure-providers-through-codefly) — provider configuration

## Running a solution against this host

Drive it from the **solution's own** codefly workspace, which composes this repo
as a module by path:

```bash
codefly add module --source <this-repo>/module
codefly run service --fixture dev-admin   # from the solution root
```

Composition provisions this host's `configurations/local/*` groups — `legal`,
`identity`, `internal-auth` (token included), and the rest — into the solution
workspace automatically. The solution does not hand-author them; it overrides a
group only by declaring one of the same name. The CLI's native delivery signs
presence and authority for Accounts to reconcile. The runtime does not register
itself or renew presence on an idle timer; its work credentials are separate.

For a disposable qualification run, add `--temporary-ports` to isolate ports,
container names and runtime state. `--output-env <owner-only-file>` with
`--output-env-service <module>/<service>` exports the resolved environment for
that service. Read only the required endpoint from it; never copy its credentials
into a hand-written runtime command or publish the file.

## When a run will not come up

**Do not hand-write the environment the SDK resolves.** Injected variables,
endpoint addresses, derived ports, credentials copied out of another component's
config — if you are typing one of those, stop. The value is true only on this
machine for the next ten minutes, and a service started that way can boot and
serve without its signed delivery being applied to the host. A listening socket
does not prove graph membership or installation. A hand-built environment that
"works" is the failure mode, not the fix.

The fix for a run the tooling cannot express is a fix **in the tooling**. File it
against the CLI (codefly-dev/cli) and say in your PR body what you could not
run, rather than shipping a local workaround.

Diagnose rather than pattern-match:

- "It started working when I set X" is not a diagnosis. Set X back and confirm it
  breaks.
- Do not trust an error message before checking it. A runtime reporting a
  provisioned secret that "does not match its digest" has meant a missing
  internal token, with the digest verifiably correct.
- Read the run log for the service that is *missing*, not only for the one that
  reported an error.
