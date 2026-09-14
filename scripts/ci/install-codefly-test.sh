#!/usr/bin/env bash
# Test-only tool: the release CLI predates Core's isolated dependency sessions.
set -euo pipefail

if [[ "$(uname -s)" != Linux || "$(uname -m)" != x86_64 ]]; then
  echo 'The CI Codefly test-tool installer requires Linux x64' >&2
  exit 1
fi

export GOTOOLCHAIN=local GOWORK=off GOFLAGS=
if [[ "$(go env GOVERSION)" != go1.27.0 ]]; then
  echo 'The pinned Codefly test tool requires Go 1.27.0' >&2
  exit 1
fi

repository=https://github.com/codefly-dev/cli
commit=7a3a895a1ea308ea838e9e165221fc875d2563bb
tree=ea209937b139e02138990039985c684228d9af8d
scratch="$(mktemp -d "${RUNNER_TEMP}/codefly-test-source.XXXXXX")"
trap 'rm -rf "${scratch}"' EXIT

git init --quiet "${scratch}/source"
git -C "${scratch}/source" fetch --quiet --depth=1 "${repository}" "${commit}"
git -C "${scratch}/source" checkout --quiet --detach FETCH_HEAD
if [[ "$(git -C "${scratch}/source" rev-parse HEAD)" != "${commit}" ||
      "$(git -C "${scratch}/source" rev-parse HEAD^{tree})" != "${tree}" ]]; then
  echo 'Codefly test-tool source does not match the pinned commit and tree' >&2
  exit 1
fi

(
  cd "${scratch}/source"
  go build -mod=readonly -p=2 -buildvcs=true -trimpath -ldflags='-s -w' \
    -o "${scratch}/codefly" ./cmd/codefly
)
go version -m "${scratch}/codefly" > "${scratch}/build-info.txt"
grep -Fq "vcs.revision=${commit}" "${scratch}/build-info.txt"
grep -Fq 'vcs.modified=false' "${scratch}/build-info.txt"

install -d "${RUNNER_TEMP}/codefly-test-bin"
install -m 755 "${scratch}/codefly" "${RUNNER_TEMP}/codefly-test-bin/codefly"
install -m 644 "${scratch}/build-info.txt" "${RUNNER_TEMP}/codefly-test-bin/build-info.txt"
digest="$(sha256sum "${scratch}/codefly" | cut -d ' ' -f 1)"
printf '{"repository":"%s","commit":"%s","tree":"%s","binary_sha256":"%s","toolchain":"go1.27.0","scope":"test-only source build; not an official release"}\n' \
  "${repository}" "${commit}" "${tree}" "${digest}" > "${RUNNER_TEMP}/codefly-test-bin/provenance.json"
cat "${RUNNER_TEMP}/codefly-test-bin/provenance.json"
cat "${RUNNER_TEMP}/codefly-test-bin/build-info.txt"
echo "${RUNNER_TEMP}/codefly-test-bin" >> "${GITHUB_PATH}"
