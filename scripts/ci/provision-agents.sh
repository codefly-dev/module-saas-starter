#!/usr/bin/env bash
set -euo pipefail

sources="$(mktemp -d "${RUNNER_TEMP}/saas-agent-sources.XXXXXX")"
trap 'rm -rf "$sources"' EXIT

# The Go agent bootstraps itself and packages each subsequent native agent.
while IFS=$'\t' read -r repository version revision; do
  if [[ "$AGENT_CACHE_HIT" == true && "$repository" != codefly-dev/service-object-storage ]]; then
    continue
  fi
  directory="$sources/${repository##*/}"
  git init --quiet "$directory"
  git -C "$directory" fetch --quiet --depth=1 "https://github.com/$repository.git" "$revision"
  git -C "$directory" checkout --quiet --detach FETCH_HEAD
  grep -Fxq "version: $version" "$directory/agent.codefly.yaml"
  if [[ "$AGENT_CACHE_HIT" != true ]]; then
    codefly agent build --native-only --dir "$directory"
  fi
  if [[ "$repository" == codefly-dev/service-object-storage ]]; then
    image="service-object-storage:${revision}"
    docker build --tag "$image" "$directory"
    echo "SOS_GATEWAY_IMAGE=$image" >> "$GITHUB_ENV"
  fi
done < "${1:-$(dirname "$0")/agent-sources.tsv}"
