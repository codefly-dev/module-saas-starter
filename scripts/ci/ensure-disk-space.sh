#!/usr/bin/env bash
# Avoid spending minutes deleting hosted-runner toolchains when space is ample.
set -euo pipefail

minimum_kib=$((12 * 1024 * 1024))
available_kib() {
  df -Pk /var/lib/docker | awk 'NR == 2 { print $4 }'
}

for toolchain in /usr/local/lib/android /opt/ghc /usr/share/dotnet; do
  if (( $(available_kib) >= minimum_kib )); then
    break
  fi
  sudo rm -rf "${toolchain}"
done

df -h /var/lib/docker
if (( $(available_kib) < minimum_kib )); then
  echo "::error::Docker requires at least 12 GiB free before CI starts"
  exit 1
fi
