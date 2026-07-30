#!/usr/bin/env bash

# Configure Cloudflare Turnstile for public acquisition forms. The secret is
# validated server-side with a deliberately invalid response token; this
# distinguishes a valid secret without completing or mutating a challenge.

set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=provider-common.sh
source "${SCRIPT_DIR}/provider-common.sh"

SETUP_PROVIDER="turnstile"
DEFAULT_WORKSPACE="$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd)"

workspace="${DEFAULT_WORKSPACE}"
env_file=""
secret_file=""
site_key="${NEXT_PUBLIC_TURNSTILE_SITE_KEY:-${TURNSTILE_SITE_KEY:-}}"
secret_key="${TURNSTILE_SECRET_KEY:-}"
allowed_hostnames="${TURNSTILE_ALLOWED_HOSTNAMES:-localhost,127.0.0.1}"
verify_url="${TURNSTILE_VERIFY_URL:-https://challenges.cloudflare.com/turnstile/v0/siteverify}"
fixture=""
force=0
skip_remote_validation=0
skip_doctor=0

usage() {
  printf '%s\n' \
    "Configure Cloudflare Turnstile for local-dogfood." \
    "" \
    "Usage: scripts/setup/turnstile.sh [options]" \
    "" \
    "  --env-file PATH       Read TURNSTILE_* values." \
    "  --secret-file PATH    Read TURNSTILE_SECRET_KEY safely." \
    "  --site-key KEY        Public widget key." \
    "  --hostnames CSV       Exact accepted response hostnames." \
    "  --fixture pass|fail|replay" \
    "                        Install Cloudflare's deterministic test credentials." \
    "  --workspace PATH      Target SaaS starter checkout." \
    "  --force               Replace differing local configuration." \
    "  --skip-remote-validation" \
    "                        Skip read-only Siteverify secret validation." \
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
    --secret-file)
      setup_require_value "$1" "${2:-}"
      secret_file="$2"
      shift 2
      ;;
    --site-key)
      setup_require_value "$1" "${2:-}"
      site_key="$2"
      shift 2
      ;;
    --hostnames)
      setup_require_value "$1" "${2:-}"
      allowed_hostnames="$2"
      shift 2
      ;;
    --fixture)
      setup_require_value "$1" "${2:-}"
      fixture="$2"
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
  for name in NEXT_PUBLIC_TURNSTILE_SITE_KEY TURNSTILE_SITE_KEY TURNSTILE_SECRET_KEY TURNSTILE_ALLOWED_HOSTNAMES TURNSTILE_VERIFY_URL; do
    candidate="$(setup_read_dotenv_value "${name}" "${env_file}")"
    case "${name}" in
      NEXT_PUBLIC_TURNSTILE_SITE_KEY|TURNSTILE_SITE_KEY) [[ -z "${candidate}" ]] || site_key="${candidate}" ;;
      TURNSTILE_SECRET_KEY) [[ -z "${candidate}" ]] || secret_key="${candidate}" ;;
      TURNSTILE_ALLOWED_HOSTNAMES) [[ -z "${candidate}" ]] || allowed_hostnames="${candidate}" ;;
      TURNSTILE_VERIFY_URL) [[ -z "${candidate}" ]] || verify_url="${candidate}" ;;
    esac
  done
fi
if [[ -n "${secret_file}" ]]; then
  secret_key="$(setup_read_secret_file TURNSTILE_SECRET_KEY "${secret_file}")"
fi

case "${fixture}" in
  "")
    ;;
  pass)
    site_key="1x00000000000000000000AA"
    secret_key="1x0000000000000000000000000000000AA"
    skip_remote_validation=1
    ;;
  fail)
    site_key="2x00000000000000000000AB"
    secret_key="2x0000000000000000000000000000000AA"
    skip_remote_validation=1
    ;;
  replay)
    site_key="1x00000000000000000000AA"
    secret_key="3x0000000000000000000000000000000AA"
    skip_remote_validation=1
    ;;
  *)
    setup_fail "fixture must be pass, fail, or replay"
    ;;
esac

if [[ -z "${site_key}" ]]; then
  [[ -t 0 ]] || setup_fail "Turnstile site key is missing; use --site-key"
  printf 'Turnstile public site key: ' >&2
  IFS= read -r site_key
fi
if [[ -z "${secret_key}" ]]; then
  [[ -t 0 ]] || setup_fail "TURNSTILE_SECRET_KEY is missing; use --env-file or --secret-file"
  printf 'Turnstile secret key (input hidden): ' >&2
  IFS= read -r -s secret_key
  printf '\n' >&2
fi
[[ "${site_key}" =~ ^[A-Za-z0-9_-]{20,64}$ ]] ||
  setup_fail "Turnstile site key has an invalid format"
[[ "${secret_key}" =~ ^[A-Za-z0-9_-]{20,128}$ ]] ||
  setup_fail "Turnstile secret key has an invalid format"
[[ "${allowed_hostnames}" =~ ^[A-Za-z0-9.,_-]+$ ]] ||
  setup_fail "Turnstile allowed hostnames must be a comma-separated hostname list"
[[ "${verify_url}" =~ ^https://[^[:space:]]+$ ||
   "${verify_url}" =~ ^http://(localhost|127\.0\.0\.1)(:[0-9]+)?/.*$ ]] ||
  setup_fail "Turnstile verify URL must use HTTPS or local HTTP"

if [[ "${skip_remote_validation}" -eq 0 ]]; then
  command -v curl >/dev/null 2>&1 ||
    setup_fail "curl is required for remote validation"
  command -v jq >/dev/null 2>&1 ||
    setup_fail "jq is required for remote validation"
  validation="${SETUP_TEMPORARY_DIR}/siteverify.json"
  status="$(
    curl --silent --show-error --max-time 15 \
      --output "${validation}" --write-out '%{http_code}' \
      --config - --request POST "${verify_url}" <<EOF
data = "secret=${secret_key}"
data = "response=codefly-secret-validation"
EOF
  )"
  [[ "${status}" == "200" ]] ||
    setup_fail "Turnstile Siteverify is unavailable (HTTP ${status})"
  if jq -e '."error-codes" // [] | index("invalid-input-secret") != null' "${validation}" >/dev/null; then
    setup_fail "Cloudflare rejected the Turnstile secret"
  fi
fi

public_config="${SETUP_TEMPORARY_DIR}/abuse-protection.env"
secret_config="${SETUP_TEMPORARY_DIR}/abuse-protection.secret.env"
printf '%s\n' \
  'ABUSE_PROTECTION_MODE=turnstile' \
  'NEXT_PUBLIC_ABUSE_PROTECTION_MODE=turnstile' \
  "NEXT_PUBLIC_TURNSTILE_SITE_KEY=${site_key}" \
  "TURNSTILE_ALLOWED_HOSTNAMES=${allowed_hostnames}" \
  "TURNSTILE_VERIFY_URL=${verify_url}" \
  >"${public_config}"
printf '%s\n' \
  "TURNSTILE_SECRET_KEY=${secret_key}" \
  >"${secret_config}"

setup_install_pair abuse-protection "${public_config}" abuse-protection "${secret_config}"
setup_doctor

printf '\nTurnstile dogfood configuration is ready.\n'
if [[ -n "${fixture}" ]]; then
  printf 'Deterministic fixture behavior: %s\n' "${fixture}"
else
  printf 'Allowed response hostnames: %s\n' "${allowed_hostnames}"
fi
