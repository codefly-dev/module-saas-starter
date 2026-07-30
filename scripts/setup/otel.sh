#!/usr/bin/env bash

# Configure the in-graph OpenTelemetry Collector. The application always sends
# to the collector through its Codefly-injected endpoint; this configuration
# selects only the collector's outbound exporter.

set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=provider-common.sh
source "${SCRIPT_DIR}/provider-common.sh"

SETUP_PROVIDER="otel"
DEFAULT_WORKSPACE="$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd)"

workspace="${DEFAULT_WORKSPACE}"
env_file=""
headers_file=""
exporter="${OBSERVABILITY_EXPORTER:-debug}"
endpoint="${OTEL_EXPORTER_OTLP_ENDPOINT:-}"
headers="${OTEL_EXPORTER_OTLP_HEADERS:-}"
force=0
skip_doctor=0

usage() {
  printf '%s\n' \
    "Configure the Codefly OpenTelemetry Collector." \
    "" \
    "Usage: scripts/setup/otel.sh [options]" \
    "" \
    "  --debug               Keep telemetry in the local collector debug exporter." \
    "  --endpoint URL        Forward with OTLP/HTTP to this non-secret endpoint." \
    "  --headers-file PATH   Read OTEL_EXPORTER_OTLP_HEADERS safely." \
    "  --env-file PATH       Read OBSERVABILITY_EXPORTER and OTEL_* values." \
    "  --workspace PATH      Target SaaS starter checkout." \
    "  --force               Replace differing local configuration." \
    "  --skip-doctor         Skip Codefly workspace doctor." \
    "  -h, --help            Show this help."
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --debug)
      exporter="debug"
      endpoint=""
      headers=""
      shift
      ;;
    --endpoint)
      setup_require_value "$1" "${2:-}"
      exporter="otlphttp"
      endpoint="${2%/}"
      shift 2
      ;;
    --headers-file)
      setup_require_value "$1" "${2:-}"
      headers_file="$2"
      shift 2
      ;;
    --env-file)
      setup_require_value "$1" "${2:-}"
      env_file="$2"
      shift 2
      ;;
    --workspace)
      setup_require_value "$1" "${2:-}"
      workspace="$2"
      shift 2
      ;;
    --force)
      force=1
      shift
      ;;
    --skip-doctor)
      skip_doctor=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      setup_fail "unknown option: $1 (use --help)"
      ;;
  esac
done

setup_prepare_workspace
setup_prepare_temporary_directory

if [[ -n "${env_file}" ]]; then
  [[ -f "${env_file}" ]] || setup_fail "environment file does not exist: ${env_file}"
  candidate="$(setup_read_dotenv_value OBSERVABILITY_EXPORTER "${env_file}")"
  [[ -z "${candidate}" ]] || exporter="${candidate}"
  candidate="$(setup_read_dotenv_value OTEL_EXPORTER_OTLP_ENDPOINT "${env_file}")"
  [[ -z "${candidate}" ]] || endpoint="${candidate%/}"
  candidate="$(setup_read_dotenv_value OTEL_EXPORTER_OTLP_HEADERS "${env_file}")"
  [[ -z "${candidate}" ]] || headers="${candidate}"
fi
if [[ -n "${headers_file}" ]]; then
  headers="$(setup_read_secret_file OTEL_EXPORTER_OTLP_HEADERS "${headers_file}")"
fi

case "${exporter}" in
  debug)
    endpoint=""
    headers=""
    ;;
  otlphttp)
    [[ "${endpoint}" =~ ^https://[^[:space:]]+$ ||
       "${endpoint}" =~ ^http://(localhost|127\.0\.0\.1)(:[0-9]+)?(/.*)?$ ]] ||
      setup_fail "OTLP endpoint must use HTTPS or local HTTP"
    [[ "${headers}" != *$'\n'* && "${headers}" != *$'\r'* ]] ||
      setup_fail "OTLP headers contain an invalid newline"
    ;;
  *)
    setup_fail "OBSERVABILITY_EXPORTER must be debug or otlphttp"
    ;;
esac

public_config="${SETUP_TEMPORARY_DIR}/observability.env"
secret_config="${SETUP_TEMPORARY_DIR}/observability.secret.env"
printf '%s\n' \
  "OBSERVABILITY_EXPORTER=${exporter}" \
  "OTEL_EXPORTER_OTLP_ENDPOINT=${endpoint}" \
  >"${public_config}"
printf '%s\n' \
  "OTEL_EXPORTER_OTLP_HEADERS=${headers}" \
  >"${secret_config}"

setup_install_pair observability "${public_config}" observability "${secret_config}"
setup_doctor

printf '\nOpenTelemetry Collector dogfood configuration is ready.\n'
if [[ "${exporter}" == "debug" ]]; then
  printf 'Telemetry remains local and is emitted by the collector debug exporter.\n'
else
  printf 'Collector export destination:\n  %s\n' "${endpoint}"
fi
