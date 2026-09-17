---
name: run-the-starter-locally
description: Boot this module's whole service graph on a developer machine — with the fake-auth fixture, with the real provider stack, or underneath a solution being developed against it — and diagnose a run that will not come up. Use when asked to run, start, boot, serve, or reproduce something against the starter locally, when `codefly run` fails or leaves stray processes behind, or when a service appears to start but is missing from the graph.
---

# Running the starter locally

The graph is vault → store → cache → telemetry → accounts → auth-gateway →
frontend, plus marketing. One command boots all of it with a fake-auth fixture:

```bash
codefly run service --fixture dev-admin
```

- Agents resolve from their published GitHub releases by default; add
  `--local-agents` to resolve only from `~/.codefly/agents/` (offline, or when
  testing a local agent build).
- No TTY (CI, pipes, MCP) auto-enables `--headless`.
- Docker must be running. `codefly doctor` checks prerequisites; `codefly clear`
  reaps stray processes and containers between runs.

For a real external identity provider and the production-grade provider stack,
use the `local-dogfood` environment and the setup scripts:

- [LOCAL_DOGFOODING.md](../../../LOCAL_DOGFOODING.md) — runnable local product
- [DOGFOODING.md](../../../DOGFOODING.md) — feature-by-feature checklist
- [scripts/setup/README.md](../../../scripts/setup/README.md) — provider bootstrap

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
group only by declaring one of the same name. A well-behaved solution runtime
self-registers with both the host and the gateway on its own through the codefly
SDK.

## When a run will not come up

**Do not hand-write the environment the SDK resolves.** Injected variables,
endpoint addresses, derived ports, credentials copied out of another component's
config — if you are typing one of those, stop. The value is true only on this
machine for the next ten minutes, and a service started that way can boot,
serve, and still be absent from the graph: at least one runtime skips
registration *silently* when its credential is missing, logging nothing. A
hand-built environment that "works" is the failure mode, not the fix.

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
