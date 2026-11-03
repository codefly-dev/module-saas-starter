#!/usr/bin/env bash
# The shared host-interface conformance suite at codefly-dev/authority's main,
# against accounts. accounts pins an authority revision in its go.mod; this
# runs the newest authority that still contains that pin, so a case authority
# adds, or a rule the authority core changes, reaches accounts on its next pull
# request instead of on the next re-pin. A main that does not contain the pin
# (the pin is a branch not merged yet) runs at the pin and says so.
#
# Before the suite, at the pin and before any re-pin, the declaration gate: the
# interface versions module.codefly.yaml declares accounts implements
# (`implements: codefly.dev/<interface>@<version>`) are held to the version each
# interface publishes at the pinned revision, the revision whose suite accounts'
# own tests run. "Declares 0.3.5" and "passes 0.3.5's suite" are one fact, or
# this job is red.
set -euo pipefail
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$repo_root/module/services/accounts/code"
export GOWORK=off

pinned="$(go list -m -f '{{.Version}}' github.com/codefly-dev/authority/libraries/authority-core/go)"
# The commit the pseudo-version abbreviates, in full, from the module cache's
# origin record: GitHub serves a commit by its full id alone, and the record is
# written when the module is downloaded, so this reads nothing new.
pinned_commit="$(go mod download -json "github.com/codefly-dev/authority/libraries/authority-core/go@${pinned}" \
  | sed -n 's/^[[:space:]]*"Hash": *"\([0-9a-f]\{40\}\)".*$/\1/p')"
if [ -z "$pinned_commit" ]; then
  echo "::error::cannot resolve the commit behind the authority pin ${pinned}"
  exit 1
fi
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
# The pinned commit itself, whether or not main contains it: a pin on an
# unmerged branch is reachable through that branch, and GitHub serves any
# reachable commit by its full id. A commit authority no longer carries leaves
# the declaration unprovable at its source, which is a red, not a skip.
if ! git -C "$authority/main.git" fetch -q --filter=blob:none origin "$pinned_commit"; then
  echo "::error::codefly-dev/authority no longer carries the pinned commit ${pinned_commit}; the declared interface versions cannot be read at the pin"
  exit 1
fi
main_sha="$(git -C "$authority/main.git" rev-parse --short=12 HEAD)"
if git -C "$authority/main.git" merge-base --is-ancestor "$pinned_commit" "$main_sha"; then
  status="ahead"
  [ "$(git -C "$authority/main.git" rev-parse --short=12 "$pinned_commit")" = "$main_sha" ] && status="identical"
else
  status="diverged"
fi
echo "authority pinned at ${pinned_commit:0:12}; main at ${main_sha}; main is ${status} of the pin"

# The declaration gate reads interfaces/<name>/interface.codefly.yaml at the
# pin; the blobless clone fetches those few blobs on demand.
mkdir "$authority/pinned"
git -C "$authority/main.git" archive --format=tar "$pinned_commit" interfaces | tar -x -C "$authority/pinned"
(cd "$repo_root/module/tools" && go run ./cmd/module-package verify-host-interfaces --module .. --interfaces "$authority/pinned/interfaces" --revision "$pinned_commit")

if [ "$status" = "ahead" ] || [ "$status" = "identical" ]; then
  for library in authority-core conformance authority-client delivery resources; do
    go get "github.com/codefly-dev/authority/libraries/${library}/go@${main_sha}"
  done
  go mod tidy
else
  echo "::notice::authority main does not contain the pinned revision ${pinned_commit:0:12}; the suite runs at the pin"
fi
go test -count=1 -timeout 30m -run '^TestAccountsPassesTheHostInterfaceConformanceSuite$' ./pkg/adapters
