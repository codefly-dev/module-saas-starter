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
# Ancestry is read from a blobless clone of authority's main, never from the
# GitHub API: an unauthenticated API call shares a small per-address budget on
# hosted runners, and a check that fails when that budget runs out is a flake.
# The clone is retried, because a network failure says nothing about accounts.
authority="$(mktemp -d)"
trap 'rm -rf "$authority"' EXIT
for attempt in 1 2 3; do
  if git clone -q --bare --filter=blob:none --single-branch --branch main \
    https://github.com/codefly-dev/authority.git "$authority/main.git"; then
    break
  fi
  rm -rf "$authority/main.git"
  if [ "$attempt" = 3 ]; then
    echo "::error::cannot clone codefly-dev/authority main"
    exit 1
  fi
  sleep $((attempt * 10))
done
main_sha="$(git -C "$authority/main.git" rev-parse --short=12 HEAD)"
if git -C "$authority/main.git" merge-base --is-ancestor "$pinned_sha" "$main_sha" 2>/dev/null; then
  status="ahead"
  [ "$(git -C "$authority/main.git" rev-parse --short=12 "$pinned_sha")" = "$main_sha" ] && status="identical"
else
  status="diverged"
fi
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
