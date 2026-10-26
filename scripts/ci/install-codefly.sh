#!/usr/bin/env bash
# The hosted CI runner is Linux x64. Keep the release and archive digest paired.
set -euo pipefail

version="${CODEFLY_VERSION:-0.1.213}"
source_revision=""
case "${version}" in
  source-8de0f6674550fb040f03bc6cb2530116878c8c6b)
    # Host-interface candidate: exports every explicitly served proto package.
    source_revision="${version#source-}"
    ;;
  source-5234ce248b092edf1138a16ec4140dfbc8745522)
    # Lifecycle candidate: recognises CLI-owned artifact-cache in every CI
    # phase. Qualify exact owner source without requiring a release first.
    source_revision="${version#source-}"
    ;;
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
  0.1.171)
    # Raises the host's build-recipe support from docker-build-recipe/v3 to v4,
    # which is what lets this tree run a released go-grpc or nextjs agent at all:
    # every agent from go-grpc 0.1.48-dev.42ce38050224 and nextjs 0.0.162 onward
    # advertises v3, v4 and v5, and 0.1.168 verified the plan against v3 alone.
    # Core supports artifact-execution/v1, runtime-init-dependency-mappings/v1,
    # codefly.dev/docker-build-recipe/v4 and configuration-value-template/v1.
    checksum=0bdb109784b9835bea984a94ddb9a809c8c29f0e260c59f821a637cbff464ffe
    ;;
  0.1.179)
    # Previous generator pin; retained for explicit historical qualifications.
    checksum=6e7666ef095815a77c10fb1644c69e17b0e8869476c18713bfaa496971b5c45f
    ;;
  0.1.197)
    # Includes the merged lifecycle/artifact-cache changes (cli#994) and
    # environment-wide standard OTEL delivery (cli#1097).
    checksum=f426ca8ad8c13be4a8aba63c3618a866cb35462a9eb08def2a59b4d9b8290827
    ;;
  0.1.213)
    # Carries cli#1128 (source 8de0f667, squash-merged) and cli#1158: the
    # hop-routes producer, which resolves the authority interface dependency.
    # Digest of the downloaded Linux amd64 archive; matches checksums.txt.
    checksum=e144417e595aa4e98937e01e5c2ce15123ae962fbe446479e8cd82e70ff7536a
    ;;
  0.1.185)
    # Released Core v0.18.0 and cli#947: scope-slot delivery, endpoint-served
    # Runnable derivation, and CLI-owned module contracts.codefly.yaml.
    # Digest from the signed release's checksums.txt (Linux amd64 archive).
    checksum=72950d9a1b28d8153f8266a13e1425d3584f8fbbe0a40ebe7e89a521d7eeac7f
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

if [[ -n "${source_revision}" ]]; then
  if [[ "${GITHUB_REF:-}" == refs/tags/* ]]; then
    echo 'A host release requires a qualified released Codefly pin, not a source candidate' >&2
    exit 1
  fi
  install -d "${RUNNER_TEMP}/codefly-bin"
  GOWORK=off GOBIN="${RUNNER_TEMP}/codefly-bin" go install \
    -ldflags "-X github.com/codefly-dev/cli/pkg/cliupdate.version=${version}" \
    "github.com/codefly-dev/cli/cmd/codefly@${source_revision}"
  echo "${RUNNER_TEMP}/codefly-bin" >> "${GITHUB_PATH}"
  exit 0
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
