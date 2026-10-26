#!/usr/bin/env bash
# Regenerate every checked-in generated library with the pinned Codefly CLI and
# fail on any difference, the way `codefly generate contracts --check` gates the
# exported contracts. A library no released generator reproduces is drift the
# next regeneration would silently discard.
set -euo pipefail
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$repo_root"

# The source-read SDK, exactly as libraries/source-read-sdk/README.md documents.
codefly generate client --from saas-starter/accounts/authority --name source-read-sdk \
  --language go --services ModuleCapabilitiesService \
  --go-module github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go \
  --output libraries/source-read-sdk --force

# The TypeScript SDK, through its own generator script (both steps).
(cd module/services/frontend/code/packages/saas-sdk && node scripts/generate.mjs --force)

paths=(libraries/source-read-sdk module/services/frontend/code/packages/saas-sdk/generated)
if ! git diff --exit-code --stat -- "${paths[@]}" || [ -n "$(git status --porcelain --untracked-files=all -- "${paths[@]}")" ]; then
  git status --porcelain --untracked-files=all -- "${paths[@]}"
  echo "a generated library differs from what the pinned Codefly CLI produces; regenerate it with scripts/ci/check-generated-libraries.sh and commit the result" >&2
  exit 1
fi
echo "generated libraries are reproducible with codefly $(codefly version)"
