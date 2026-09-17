---
name: pin-a-service-agent
description: Inspect or change the Codefly service-agent version a service pins (`go-grpc`, `nextjs`, `redis`, `postgres`, `vault`) in this module. Use when asked what agent version a service runs, whether a newer one exists, to bump or roll back a pin, or when a service manifest looks stale or hand-edited.
---

# Agent version pins

**The procedure is [module/deployment/AGENTS.md § Agent version
pins](../../../module/deployment/AGENTS.md#agent-version-pins). Read it there and
follow it.** It sits beside `topology.bindings.codefly.yaml`, the file you are
about to edit, and `module/deployment/README.md` links to it — so this skill must
not become a second copy that drifts from it.

What it covers: `codefly agent list` / `codefly agent versions` for reading the
current state, that the bindings file is the source of truth and
`service.codefly.yaml` is generated, that `codefly update workspace` will not do
this edit for you, and the base-manifest refresh that has to follow.

The one thing to hold onto before you open it: latest is not always safe, so boot
the graph before pinning — and if you did not, say so in the PR body.
