# AGENTS.md — deployment

`topology.bindings.codefly.yaml` is **the source of truth** for the service graph
and the agent version each service pins. Everything under `generated/` and every
`services/<svc>/service.codefly.yaml` is rendered from it and carries a `DO NOT
EDIT` header. [README.md](./README.md) is the working guide — the service graph,
configuration groups, and the committed path for adding a dependency.

## Agent version pins

Each service pins the version of its Codefly service agent (`go-grpc`, `nextjs`,
`redis`, `postgres`, `vault`). To see what is pinned and whether a newer release
exists:

```bash
codefly agent list        # PINNED vs LATEST-RESOLVABLE, resolvability, how far behind
codefly agent versions <agent>
```

To change a pin, **edit the version in `topology.bindings.codefly.yaml`**, then
regenerate the per-service manifests and refresh the base manifest:

- `services/<svc>/service.codefly.yaml` is generated. A hand-edit is lost on the
  next composition and silently drifts the two files apart.
- `codefly update workspace` does **not** rewrite the bindings for this repo (it
  skips the generated manifests by design), so the bindings edit is manual.
- The bindings file is base-tracked, so finish with [../AGENTS.md § Base-file
  integrity manifest](../AGENTS.md#base-file-integrity-manifest) or CI reds on
  integrity.

**Latest is not always safe.** Agent releases can carry breaking changes to
service manifests. Verify the newer agent actually boots the graph (`codefly run
service`) before pinning it — "it resolves" is not "it runs". If you did not boot
the graph, say so in the pull request body.
