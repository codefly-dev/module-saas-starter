---
name: refresh-base-manifest
description: Refresh `module/tools/base-manifest.json`, the base-file integrity manifest that hashes every file shipped to consumers. Use after editing ANY file under `module/` — including `topology.bindings.codefly.yaml` and the generated service manifests — or when CI fails "Base manifest integrity" or "Codefly CI" with a base-file drift or hash mismatch.
---

# Refreshing the base-file integrity manifest

**The procedure is [module/AGENTS.md § Base-file integrity
manifest](../../../module/AGENTS.md#base-file-integrity-manifest). Read it there
and follow it.** It lives in the module tree on purpose: consumers get that file
and the docs inside the tree reference it, so this skill must not become a second
copy that drifts from it.

What it covers: the `check` (consumer) versus `gen` (canonical) split, the
clean-worktree regeneration recipe, and the two traps — regenerate **last**, and a
branch behind `main` reds the check via the merge ref.

The one thing to hold onto before you open it: this is the easiest gate in the
repository to trip, and the failure is not in the file you edited.
