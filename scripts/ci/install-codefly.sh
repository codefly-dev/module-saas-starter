#!/usr/bin/env bash
# The hosted CI runner is Linux x64. Keep the release and archive digest paired.
set -euo pipefail

if [[ "$(uname -s)" != Linux || "$(uname -m)" != x86_64 ]]; then
  echo 'The CI Codefly installer requires Linux x64' >&2
  exit 1
fi

version=0.1.145
checksum=a6e1a0e7f4adae8b2701dcea7e05cb03f1ac49c85ee96b4ac98dd2fa20dcc4c7
archive="codefly_${version}_linux_amd64.tar.gz"
scratch="$(mktemp -d)"
trap 'rm -rf "${scratch}"' EXIT

curl --fail --silent --show-error --location --retry 3 \
  "https://github.com/codefly-dev/cli/releases/download/v${version}/${archive}" \
  --output "${scratch}/${archive}"
printf '%s  %s\n' "${checksum}" "${scratch}/${archive}" | sha256sum -c -
tar -xzf "${scratch}/${archive}" -C "${scratch}" codefly
install -d "${RUNNER_TEMP}/codefly-bin"
install -m 755 "${scratch}/codefly" "${RUNNER_TEMP}/codefly-bin/codefly"
echo "${RUNNER_TEMP}/codefly-bin" >> "${GITHUB_PATH}"
