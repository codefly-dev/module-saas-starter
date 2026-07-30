#!/usr/bin/env bash

# Configure Sentry runtime error reporting and source-map upload credentials.

set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=provider-common.sh
source "${SCRIPT_DIR}/provider-common.sh"

SETUP_PROVIDER="sentry"
DEFAULT_WORKSPACE="$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd)"

workspace="${DEFAULT_WORKSPACE}"
env_file=""
token_file=""
auth_token="${SENTRY_AUTH_TOKEN:-}"
organization="${SENTRY_ORG:-}"
project="${SENTRY_PROJECT:-}"
dsn="${SENTRY_DSN:-${NEXT_PUBLIC_SENTRY_DSN:-}}"
api_base="${SENTRY_API_BASE:-https://sentry.io}"
environment="${SENTRY_ENVIRONMENT:-local-dogfood}"
force=0
skip_remote_validation=0
skip_doctor=0

usage() {
  printf '%s\n' \
    "Configure Sentry for local-dogfood." \
    "" \
    "Usage: scripts/setup/sentry.sh [options]" \
    "" \
    "  --env-file PATH       Read SENTRY_* values." \
    "  --token-file PATH     Read SENTRY_AUTH_TOKEN safely." \
    "  --org SLUG            Sentry organization slug." \
    "  --project SLUG        Sentry project slug." \
    "  --api-base URL        Sentry API origin for self-hosted deployments." \
    "  --environment NAME    Runtime environment tag." \
    "  --workspace PATH      Target SaaS starter checkout." \
    "  --force               Replace differing local configuration." \
    "  --skip-remote-validation" \
    "                        Skip read-only project and DSN discovery." \
    "  --skip-doctor         Skip Codefly workspace doctor." \
    "  -h, --help            Show this help."
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      setup_require_value "$1" "${2:-}"
      env_file="$2"
      shift 2
      ;;
    --token-file)
      setup_require_value "$1" "${2:-}"
      token_file="$2"
      shift 2
      ;;
    --org)
      setup_require_value "$1" "${2:-}"
      organization="$2"
      shift 2
      ;;
    --project)
      setup_require_value "$1" "${2:-}"
      project="$2"
      shift 2
      ;;
    --api-base)
      setup_require_value "$1" "${2:-}"
      api_base="${2%/}"
      shift 2
      ;;
    --environment)
      setup_require_value "$1" "${2:-}"
      environment="$2"
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
    --skip-remote-validation)
      skip_remote_validation=1
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
  for name in SENTRY_AUTH_TOKEN SENTRY_ORG SENTRY_PROJECT SENTRY_DSN NEXT_PUBLIC_SENTRY_DSN SENTRY_API_BASE SENTRY_ENVIRONMENT; do
    candidate="$(setup_read_dotenv_value "${name}" "${env_file}")"
    case "${name}" in
      SENTRY_AUTH_TOKEN) [[ -z "${candidate}" ]] || auth_token="${candidate}" ;;
      SENTRY_ORG) [[ -z "${candidate}" ]] || organization="${candidate}" ;;
      SENTRY_PROJECT) [[ -z "${candidate}" ]] || project="${candidate}" ;;
      SENTRY_DSN|NEXT_PUBLIC_SENTRY_DSN) [[ -z "${candidate}" ]] || dsn="${candidate}" ;;
      SENTRY_API_BASE) [[ -z "${candidate}" ]] || api_base="${candidate%/}" ;;
      SENTRY_ENVIRONMENT) [[ -z "${candidate}" ]] || environment="${candidate}" ;;
    esac
  done
fi
if [[ -n "${token_file}" ]]; then
  auth_token="$(setup_read_secret_file SENTRY_AUTH_TOKEN "${token_file}")"
fi
if [[ -z "${auth_token}" ]]; then
  [[ -t 0 ]] || setup_fail "SENTRY_AUTH_TOKEN is missing; use --env-file or --token-file"
  printf 'Sentry auth token (input hidden): ' >&2
  IFS= read -r -s auth_token
  printf '\n' >&2
fi
if [[ -z "${organization}" ]]; then
  [[ -t 0 ]] || setup_fail "SENTRY_ORG is missing; use --org"
  printf 'Sentry organization slug: ' >&2
  IFS= read -r organization
fi
if [[ -z "${project}" ]]; then
  [[ -t 0 ]] || setup_fail "SENTRY_PROJECT is missing; use --project"
  printf 'Sentry project slug: ' >&2
  IFS= read -r project
fi
[[ "${auth_token}" =~ ^[A-Za-z0-9_.-]{20,}$ ]] ||
  setup_fail "Sentry auth token has an invalid format"
[[ "${organization}" =~ ^[A-Za-z0-9_-]+$ ]] ||
  setup_fail "Sentry organization slug has an invalid format"
[[ "${project}" =~ ^[A-Za-z0-9_-]+$ ]] ||
  setup_fail "Sentry project slug has an invalid format"
[[ "${environment}" =~ ^[A-Za-z0-9_.-]+$ ]] ||
  setup_fail "Sentry environment has an invalid format"
[[ "${api_base}" =~ ^https://[^[:space:]/]+$ ||
   "${api_base}" =~ ^http://(localhost|127\.0\.0\.1)(:[0-9]+)?$ ]] ||
  setup_fail "Sentry API base must be an HTTPS origin or local HTTP origin"

if [[ "${skip_remote_validation}" -eq 0 ]]; then
  setup_require_remote_tools
  project_response="${SETUP_TEMPORARY_DIR}/project.json"
  project_status="$(
    curl --silent --show-error --max-time 15 \
      --output "${project_response}" --write-out '%{http_code}' \
      --config - "${api_base}/api/0/projects/${organization}/${project}/" <<EOF
header = "Authorization: Bearer ${auth_token}"
EOF
  )"
  [[ "${project_status}" == "200" ]] ||
    setup_fail "Sentry rejected the token or project (HTTP ${project_status})"
  response_slug="$(jq -r '.slug // empty' "${project_response}")"
  [[ "${response_slug}" == "${project}" ]] ||
    setup_fail "Sentry returned a different project"

  keys_response="${SETUP_TEMPORARY_DIR}/keys.json"
  keys_status="$(
    curl --silent --show-error --max-time 15 \
      --output "${keys_response}" --write-out '%{http_code}' \
      --config - "${api_base}/api/0/projects/${organization}/${project}/keys/" <<EOF
header = "Authorization: Bearer ${auth_token}"
EOF
  )"
  [[ "${keys_status}" == "200" ]] ||
    setup_fail "cannot discover the Sentry project DSN (HTTP ${keys_status})"
  discovered_dsn="$(jq -r '.[0].dsn.public // empty' "${keys_response}")"
  [[ -n "${discovered_dsn}" ]] || setup_fail "Sentry project has no client key"
  [[ -z "${dsn}" || "${dsn}" == "${discovered_dsn}" ]] ||
    setup_fail "the supplied DSN does not belong to the selected project"
  dsn="${discovered_dsn}"
fi

[[ "${dsn}" =~ ^https?://[^[:space:]@]+@[^[:space:]]+/[0-9]+$ ]] ||
  setup_fail "Sentry DSN is missing or invalid; remote discovery is recommended"

public_config="${SETUP_TEMPORARY_DIR}/error-tracking.env"
secret_config="${SETUP_TEMPORARY_DIR}/error-tracking.secret.env"
printf '%s\n' \
  'ERROR_TRACKING_MODE=sentry' \
  'NEXT_PUBLIC_ERROR_TRACKING_MODE=sentry' \
  "NEXT_PUBLIC_SENTRY_DSN=${dsn}" \
  "NEXT_PUBLIC_SENTRY_ENVIRONMENT=${environment}" \
  >"${public_config}"
printf '%s\n' \
  "SENTRY_DSN=${dsn}" \
  "SENTRY_ENVIRONMENT=${environment}" \
  "SENTRY_ORG=${organization}" \
  "SENTRY_PROJECT=${project}" \
  "SENTRY_AUTH_TOKEN=${auth_token}" \
  >"${secret_config}"

setup_install_pair error-tracking "${public_config}" error-tracking "${secret_config}"
setup_doctor

printf '\nSentry dogfood configuration is ready.\n'
printf 'Project: %s/%s\n' "${organization}" "${project}"
printf 'Release names remain deployment-owned and should be set to the exact Git commit.\n'
