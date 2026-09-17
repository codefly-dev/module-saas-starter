---
name: refresh-base-manifest
description: Refresh `module/tools/base-manifest.json`, the base-file integrity manifest that hashes every file shipped to consumers. Use after editing ANY file under `module/` — including `topology.bindings.codefly.yaml` and the generated service manifests — or when CI fails "Base manifest integrity" or "Codefly CI" with a base-file drift or hash mismatch.
---

# Refreshing the base-file integrity manifest

`module/tools/base-manifest.json` records the sha256 of every base file the
module ships. Consumers compose the module as a copy, and the manifest is what
makes "consumers ADD files, never MODIFY base ones" mechanical. Editing a tracked
base file without refreshing the manifest fails two CI checks: **Base manifest
integrity** and **Codefly CI**.

Regenerate it **from a clean checkout**. `gen` walks the tree, so a dirty
worktree makes it hash gitignored harness artifacts CI never sees:

```bash
git worktree add --detach /tmp/bm-clean HEAD
cd /tmp/bm-clean/module && node tools/base-integrity.mjs gen && node tools/base-integrity.mjs verify
# copy module/tools/base-manifest.json back, confirm the diff is only your files, commit
git worktree remove /tmp/bm-clean --force
```

Two things that cost time:

- **Regenerate last.** Any later edit to a base file — an amend, a review fixup,
  a rebase that brings one in — re-stales the manifest you just refreshed.
- **A branch behind `main` reds this check too**, because it runs against the
  merge ref. If the diff looks clean and the check is still red, rebase before
  suspecting the manifest — and read the log, since the same job also runs the
  commit-identity gate.
