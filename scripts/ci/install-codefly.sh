#!/usr/bin/env bash
# The hosted CI runner is Linux x64. Keep the release and archive digest paired.
set -euo pipefail

version="${CODEFLY_VERSION:-0.1.168}"
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
    # its Core 0.3.35 runtime requires the agent fleet pinned at Core >= 0.3.28.
    # (0.3.35 is what the released archive vendors — `go version -m codefly`.)
    checksum=1cd0ce2abad4b4b0f19ea3ac16ca7ac92b5112082518590a3f4e9a76f5bcd3fa
    ;;
  0.1.160)
    # Requires CLI-agent protocol v1 from every service agent (Core 0.5 fleet):
    # go-grpc >= 0.1.43, nextjs >= 0.0.157, postgres >= 0.0.137, redis >= 0.0.92,
    # vault >= 0.0.33. Refuses the previous pins outright.
    checksum=7d68805935e942309406aeec385ea2f3135fa9a6fec06760df6b2370ff48b5c1
    ;;
  0.1.161)
    # Core v0.5.1: `latest` resolves to the newest published release (cli#785);
    # cli#786 removed `codefly verify`, `codefly sync module` and the `verify`
    # CI phase, so this tree carries no base manifest and plans `sync-drift` alone.
    checksum=47e133cebd8f72fc44e2c1c3f76b375377ba946818fcbbdcee1f126c12a7fd5c
    ;;
  0.1.162)
    # cli#799: committed `module-resolution`, CODEFLY_MODULE_CACHE, per-module
    # render namespaces; `generate contracts` skips an exported REST endpoint
    # that carries no OpenAPI document instead of failing, which is what lets
    # this tree export the gateway's REST endpoint at all.
    checksum=9ffdb661f448332c8a0edab90c16006683289ce51f97c9067de8e974e8bdb679
    ;;
  0.1.168)
    # Core v0.5.9: the GitOps render classifies configuration names by their
    # carrier (cli#826), environments admit a configuration profile chain
    # (cli#827), and workspace endpoint references resolve per consumer and
    # order the run (cli#828).
    checksum=d8c6180afdd4ca9b393d0bf5d5a7974c20fa1047023f31c1f03100465889cc02
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
