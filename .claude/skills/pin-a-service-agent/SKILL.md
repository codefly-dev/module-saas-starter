---
name: pin-a-service-agent
description: Inspect or change the Codefly service-agent version a service pins (`go-grpc`, `nextjs`, `redis`, `postgres`, `vault`) in this module. Use when asked what agent version a service runs, whether a newer one exists, to bump or roll back a pin, or when a service manifest looks stale or hand-edited.
---

# Agent version pins

Each service pins the version of its Codefly service agent. To see what is pinned
and whether a newer release exists:

```bash
codefly agent list        # PINNED vs LATEST-RESOLVABLE, resolvability, how far behind
codefly agent versions <agent>
```

## Changing a pin

The **source of truth is
`module/deployment/topology.bindings.codefly.yaml`** — it carries the service
graph and the agent version each service pins. Edit the version there, then
regenerate the per-service manifests and refresh the base manifest.

- `module/services/<svc>/service.codefly.yaml` is **generated**. Its header says
  `DO NOT EDIT`; a hand-edit is lost on the next composition and drifts the two
  files apart.
- `codefly update workspace` does **not** rewrite the bindings for this repo (it
  skips the generated manifests by design), so the bindings edit is manual.
- The bindings file is a tracked base file, so finish with the
  `refresh-base-manifest` skill or CI reds on integrity.

## Latest is not always safe

Agent releases can carry breaking changes to service manifests. Verify the newer
agent actually boots the graph (`codefly run service`, see the
`run-the-starter-locally` skill) before pinning it. "It resolves" is not "it
runs"; if you did not boot the graph, say so in the PR body.
