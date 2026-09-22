#!/usr/bin/env bash
# The hosted CI runner is Linux x64. Keep the release and archive digest paired.
set -euo pipefail

version="${CODEFLY_VERSION:-0.1.160}"
case "${version}" in
  0.1.145)
    checksum=a6e1a0e7f4adae8b2701dcea7e05cb03f1ac49c85ee96b4ac98dd2fa20dcc4c7
    ;;
  0.1.151)
    # First release with the integrity-input classification for the planner;
    # its build and sync-drift phases dereference a nil Runner (cli#705).
    checksum=eca1e72c8fca8d61626ea8a9f0a969f47d4a64871ffca25b3355e068f009706c
    ;;
  0.1.155)
    # Carries cli#706 (the nil-Runner fix) and cli#704 (`publish clients`);
    # agent admission uses live protocol declarations, not linked Core versions.
    checksum=1cd0ce2abad4b4b0f19ea3ac16ca7ac92b5112082518590a3f4e9a76f5bcd3fa
    ;;
  0.1.159)
    # Carries fixture dependency provisioning, runtime validation initialization,
    # and the verified release-pagination fix from Core v0.4.3.
    checksum=8ee6a651ddacbc2bd93f799e89a62d7dec58a49a2ff0686edb2cbf85560980a9
    ;;
  0.1.160)
    # Adopts Core v0.5.0 (structured configuration, runtime Init dependency
    # mappings) and pins the conformance matrix to that release line.
    checksum=7d68805935e942309406aeec385ea2f3135fa9a6fec06760df6b2370ff48b5c1
    ;;
  *)
    echo "Unsupported Codefly CI version: ${version}" >&2
    exit 1
    ;;
esac

if [[ "$(uname -s)" != Linux || "$(uname -m)" != x86_64 ]]; then
  echo 'The CI Codefly installer requires Linux x64' >&2
  exit 1
fi

archive="codefly_${version}_linux_amd64.tar.gz"
scratch="$(mktemp -d)"
trap 'rm -rf "${scratch}"' EXIT

curl --fail --silent --show-error --location --retry 3 --retry-all-errors \
  "https://github.com/codefly-dev/cli/releases/download/v${version}/${archive}" \
  --output "${scratch}/${archive}"
printf '%s  %s\n' "${checksum}" "${scratch}/${archive}" | sha256sum -c -
tar -xzf "${scratch}/${archive}" -C "${scratch}" codefly
install -d "${RUNNER_TEMP}/codefly-bin"
install -m 755 "${scratch}/codefly" "${RUNNER_TEMP}/codefly-bin/codefly"
echo "${RUNNER_TEMP}/codefly-bin" >> "${GITHUB_PATH}"
