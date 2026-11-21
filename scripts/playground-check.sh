#!/usr/bin/env bash
# The host-only playground journeys that need no solution, against a running
# `codefly run service --fixture dev-admin` (module/PLAYGROUND.md). They are
# the Go suite in module/tools/playground, which asks the Codefly CLI where the
# run's frontend is and makes every call itself: no address is typed here, and
# none is handed from one process to another.
#   scripts/playground-check.sh [--naming-scope <scope>]
set -euo pipefail
cd "$(dirname "$0")/../module/tools"
if [[ "${1:-}" == "--naming-scope" ]]; then
  [[ -n "${2:-}" ]] || { echo "--naming-scope needs a value" >&2; exit 2; }
  export PLAYGROUND_SCOPE="$2"
fi
# GO may name a wrapper command (local.mk), so it is left to split.
GOWORK=off exec ${GO:-go} test -tags playground -count=1 -v ./playground
