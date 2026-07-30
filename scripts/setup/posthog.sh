#!/usr/bin/env bash

# Configure PostHog product analytics with separate capture and management
# origins. Project and personal keys are never accepted on the command line.

set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=provider-common.sh
source "${SCRIPT_DIR}/provider-common.sh"

SETUP_PROVIDER="posthog"
DEFAULT_WORKSPACE="$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd)"

workspace="${DEFAULT_WORKSPACE}"
env_file=""
project_key_file=""
personal_key_file=""
project_key="${POSTHOG_PROJECT_API_KEY:-${NEXT_PUBLIC_POSTHOG_KEY:-}}"
personal_key="${POSTHOG_PERSONAL_API_KEY:-}"
project_id="${POSTHOG_PROJECT_ID:-}"
capture_host="${POSTHOG_HOST:-}"
api_host="${POSTHOG_API_HOST:-}"
force=0
skip_remote_validation=0
skip_doctor=0

usage() {
  printf '%s\n' \
    "Configure PostHog analytics for local-dogfood." \
    "" \
    "Usage: scripts/setup/posthog.sh [options]" \
    "" \
    "  --env-file PATH          Read POSTHOG_* values." \
    "  --project-key-file PATH  Read the phc_ project capture key safely." \
    "  --personal-key-file PATH Read the phx_ personal key safely." \
    "  --project-id ID          Numeric PostHog project ID." \
    "  --host URL               HTTPS capture origin, such as https://eu.i.posthog.com." \
    "  --api-host URL           HTTPS management origin, such as https://eu.posthog.com." \
    "  --workspace PATH         Target SaaS starter checkout." \
    "  --force                  Replace differing local configuration." \
    "  --skip-remote-validation Skip read-only project validation." \
    "  --skip-doctor            Skip Codefly workspace doctor." \
    "  -h, --help               Show this help."
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      setup_require_value "$1" "${2:-}"
      env_file="$2"
      shift 2
      ;;
    --project-key-file)
      setup_require_value "$1" "${2:-}"
      project_key_file="$2"
      shift 2
      ;;
    --personal-key-file)
      setup_require_value "$1" "${2:-}"
      personal_key_file="$2"
      shift 2
      ;;
    --project-id)
      setup_require_value "$1" "${2:-}"
      project_id="$2"
      shift 2
      ;;
    --host)
      setup_require_value "$1" "${2:-}"
      capture_host="${2%/}"
      shift 2
      ;;
    --api-host)
      setup_require_value "$1" "${2:-}"
      api_host="${2%/}"
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
  for name in POSTHOG_PROJECT_API_KEY POSTHOG_PERSONAL_API_KEY POSTHOG_PROJECT_ID POSTHOG_HOST POSTHOG_API_HOST; do
    candidate="$(setup_read_dotenv_value "${name}" "${env_file}")"
    case "${name}" in
      POSTHOG_PROJECT_API_KEY) [[ -z "${candidate}" ]] || project_key="${candidate}" ;;
      POSTHOG_PERSONAL_API_KEY) [[ -z "${candidate}" ]] || personal_key="${candidate}" ;;
      POSTHOG_PROJECT_ID) [[ -z "${candidate}" ]] || project_id="${candidate}" ;;
      POSTHOG_HOST) [[ -z "${candidate}" ]] || capture_host="${candidate%/}" ;;
      POSTHOG_API_HOST) [[ -z "${candidate}" ]] || api_host="${candidate%/}" ;;
    esac
  done
fi
if [[ -n "${project_key_file}" ]]; then
  project_key="$(setup_read_secret_file POSTHOG_PROJECT_API_KEY "${project_key_file}")"
fi
if [[ -n "${personal_key_file}" ]]; then
  personal_key="$(setup_read_secret_file POSTHOG_PERSONAL_API_KEY "${personal_key_file}")"
fi
if [[ -z "${project_key}" ]]; then
  [[ -t 0 ]] || setup_fail "POSTHOG_PROJECT_API_KEY is missing"
  printf 'PostHog project capture key (input hidden): ' >&2
  IFS= read -r -s project_key
  printf '\n' >&2
fi
if [[ -z "${personal_key}" ]]; then
  [[ -t 0 ]] || setup_fail "POSTHOG_PERSONAL_API_KEY is missing"
  printf 'PostHog personal API key (input hidden): ' >&2
  IFS= read -r -s personal_key
  printf '\n' >&2
fi
if [[ -z "${project_id}" ]]; then
  [[ -t 0 ]] || setup_fail "POSTHOG_PROJECT_ID is missing; use --project-id"
  printf 'PostHog numeric project ID: ' >&2
  IFS= read -r project_id
fi
if [[ -z "${capture_host}" ]]; then
  [[ -t 0 ]] || setup_fail "POSTHOG_HOST is missing; use --host"
  printf 'PostHog capture origin: ' >&2
  IFS= read -r capture_host
  capture_host="${capture_host%/}"
fi
if [[ -z "${api_host}" ]]; then
  case "${capture_host}" in
    https://eu.i.posthog.com) api_host="https://eu.posthog.com" ;;
    https://us.i.posthog.com) api_host="https://us.posthog.com" ;;
    *) api_host="${capture_host}" ;;
  esac
fi

[[ "${project_key}" =~ ^phc_[A-Za-z0-9_-]+$ ]] ||
  setup_fail "PostHog project key must start with phc_"
[[ "${personal_key}" =~ ^phx_[A-Za-z0-9_-]+$ ]] ||
  setup_fail "PostHog personal key must start with phx_"
[[ "${project_id}" =~ ^[1-9][0-9]*$ ]] ||
  setup_fail "PostHog project ID must be a positive integer"
[[ "${capture_host}" =~ ^https://[^[:space:]/]+$ ||
   "${capture_host}" =~ ^http://(localhost|127\.0\.0\.1)(:[0-9]+)?$ ]] ||
  setup_fail "PostHog capture host must be an HTTPS origin or local HTTP origin"
[[ "${api_host}" =~ ^https://[^[:space:]/]+$ ||
   "${api_host}" =~ ^http://(localhost|127\.0\.0\.1)(:[0-9]+)?$ ]] ||
  setup_fail "PostHog API host must be an HTTPS origin or local HTTP origin"

if [[ "${skip_remote_validation}" -eq 0 ]]; then
  setup_require_remote_tools
  project_response="${SETUP_TEMPORARY_DIR}/project.json"
  project_status="$(
    curl --silent --show-error --max-time 15 \
      --output "${project_response}" --write-out '%{http_code}' \
      --config - "${api_host}/api/projects/${project_id}/" <<EOF
header = "Authorization: Bearer ${personal_key}"
EOF
  )"
  [[ "${project_status}" == "200" ]] ||
    setup_fail "PostHog rejected the project or personal key (HTTP ${project_status})"
  response_id="$(jq -r '.id // empty' "${project_response}")"
  [[ "${response_id}" == "${project_id}" ]] ||
    setup_fail "PostHog returned a different project"
  response_token="$(jq -r '.api_token // empty' "${project_response}")"
  [[ -z "${response_token}" || "${response_token}" == "${project_key}" ]] ||
    setup_fail "the capture key does not belong to PostHog project ${project_id}"
fi

public_config="${SETUP_TEMPORARY_DIR}/product-analytics.env"
secret_config="${SETUP_TEMPORARY_DIR}/product-analytics.secret.env"
printf '%s\n' \
  'PRODUCT_ANALYTICS_MODE=posthog' \
  'NEXT_PUBLIC_PRODUCT_ANALYTICS_MODE=posthog' \
  "POSTHOG_HOST=${capture_host}" \
  "POSTHOG_API_HOST=${api_host}" \
  "POSTHOG_PROJECT_ID=${project_id}" \
  "NEXT_PUBLIC_POSTHOG_HOST=${capture_host}" \
  "NEXT_PUBLIC_POSTHOG_KEY=${project_key}" \
  >"${public_config}"
printf '%s\n' \
  "POSTHOG_PROJECT_API_KEY=${project_key}" \
  "POSTHOG_PERSONAL_API_KEY=${personal_key}" \
  >"${secret_config}"

setup_install_pair product-analytics "${public_config}" product-analytics "${secret_config}"
setup_doctor

printf '\nPostHog dogfood configuration is ready.\n'
printf 'Project: %s\nCapture: %s\nManagement: %s\n' "${project_id}" "${capture_host}" "${api_host}"
printf 'Next: run the onboarding journey and inspect the activation funnel and delivery projection.\n'
