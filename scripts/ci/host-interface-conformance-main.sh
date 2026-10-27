#!/usr/bin/env bash
# The shared host-interface conformance suite at codefly-dev/authority's main,
# against accounts. accounts pins an authority revision in its go.mod; this
# runs the newest authority that still contains that pin, so a case authority
# adds, or a rule the authority core changes, reaches accounts on its next pull
# request instead of on the next re-pin. A main that does not contain the pin
# (the pin is a branch not merged yet) runs at the pin and says so.
set -euo pipefail
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$repo_root/module/services/accounts/code"
export GOWORK=off

pinned="$(go list -m -f '{{.Version}}' github.com/codefly-dev/authority/libraries/authority-core/go)"
pinned_sha="${pinned##*-}"
main_sha="$(git ls-remote https://github.com/codefly-dev/authority.git refs/heads/main | cut -c1-12)"
status="$(curl -fsS "https://api.github.com/repos/codefly-dev/authority/compare/${pinned_sha}...${main_sha}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')"
echo "authority pinned at ${pinned_sha}; main at ${main_sha}; main is ${status} of the pin"
if [ "$status" = "ahead" ] || [ "$status" = "identical" ]; then
  for library in authority-core conformance authority-client delivery resources; do
    go get "github.com/codefly-dev/authority/libraries/${library}/go@${main_sha}"
  done
  go mod tidy
else
  echo "::notice::authority main does not contain the pinned revision ${pinned_sha}; the suite runs at the pin"
fi
go test -count=1 -timeout 30m -run '^TestAccountsPassesTheHostInterfaceConformanceSuite$' ./pkg/adapters
