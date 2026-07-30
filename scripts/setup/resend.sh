#!/usr/bin/env bash

# Configure Resend transactional email and its signed delivery webhook.

set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=provider-common.sh
source "${SCRIPT_DIR}/provider-common.sh"

SETUP_PROVIDER="resend"
DEFAULT_WORKSPACE="$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd)"

workspace="${DEFAULT_WORKSPACE}"
env_file=""
api_key_file=""
webhook_secret_file=""
webhook_origin=""
api_key="${RESEND_API_KEY:-}"
webhook_secret="${RESEND_WEBHOOK_SECRET:-}"
email_from="${EMAIL_FROM:-}"
force=0
skip_remote_validation=0
skip_doctor=0
provision_webhook=0

usage() {
  printf '%s\n' \
    "Configure Resend transactional email for local-dogfood." \
    "" \
    "Usage: scripts/setup/resend.sh [options]" \
    "" \
    "  --env-file PATH          Read RESEND_* and EMAIL_FROM." \
    "  --api-key-file PATH      Read RESEND_API_KEY safely." \
    "  --webhook-secret-file PATH" \
    "                           Read RESEND_WEBHOOK_SECRET safely." \
    "  --webhook-origin URL     Public HTTPS ingress used by Resend itself." \
    "  --from VALUE             Non-secret RFC 5322 sender identity." \
    "  --provision-webhook      Explicitly create the delivery webhook if absent." \
    "  --workspace PATH         Target SaaS starter checkout." \
    "  --force                  Replace differing local configuration." \
    "  --skip-remote-validation Skip read-only Resend validation." \
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
    --api-key-file)
      setup_require_value "$1" "${2:-}"
      api_key_file="$2"
      shift 2
      ;;
    --webhook-secret-file)
      setup_require_value "$1" "${2:-}"
      webhook_secret_file="$2"
      shift 2
      ;;
    --webhook-origin)
      setup_require_value "$1" "${2:-}"
      webhook_origin="${2%/}"
      shift 2
      ;;
    --from)
      setup_require_value "$1" "${2:-}"
      email_from="$2"
      shift 2
      ;;
    --provision-webhook)
      provision_webhook=1
      shift
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
codefly_origin="$(setup_public_origin)"
if [[ -z "${webhook_origin}" ]]; then
  webhook_origin="${codefly_origin}"
fi
setup_validate_webhook_origin "${webhook_origin}"
if [[ "${provision_webhook}" -eq 1 ]]; then
  setup_require_remote_webhook_origin "${webhook_origin}"
fi
webhook_url="${webhook_origin}/v1/email/webhook/resend"
setup_prepare_temporary_directory

if [[ -n "${env_file}" ]]; then
  [[ -f "${env_file}" ]] || setup_fail "environment file does not exist: ${env_file}"
  candidate="$(setup_read_dotenv_value RESEND_API_KEY "${env_file}")"
  [[ -z "${candidate}" ]] || api_key="${candidate}"
  candidate="$(setup_read_dotenv_value RESEND_WEBHOOK_SECRET "${env_file}")"
  [[ -z "${candidate}" ]] || webhook_secret="${candidate}"
  candidate="$(setup_read_dotenv_value EMAIL_FROM "${env_file}")"
  [[ -z "${candidate}" ]] || email_from="${candidate}"
fi
if [[ -n "${api_key_file}" ]]; then
  api_key="$(setup_read_secret_file RESEND_API_KEY "${api_key_file}")"
fi
if [[ -n "${webhook_secret_file}" ]]; then
  webhook_secret="$(setup_read_secret_file RESEND_WEBHOOK_SECRET "${webhook_secret_file}")"
fi
if [[ -z "${api_key}" ]]; then
  [[ -t 0 ]] || setup_fail "RESEND_API_KEY is missing; use --env-file or --api-key-file"
  printf 'Resend API key (input hidden): ' >&2
  IFS= read -r -s api_key
  printf '\n' >&2
fi
[[ "${api_key}" =~ ^re_[A-Za-z0-9_]+$ ]] ||
  setup_fail "Resend API key must start with re_"
if [[ -z "${email_from}" ]]; then
  [[ -t 0 ]] || setup_fail "EMAIL_FROM is missing; use --env-file or --from"
  printf 'Sender identity (for example Acme <onboarding@example.com>): ' >&2
  IFS= read -r email_from
fi
[[ "${email_from}" == *"@"* ]] || setup_fail "EMAIL_FROM must contain an email address"
[[ "${email_from}" != *$'\n'* && "${email_from}" != *$'\r'* ]] ||
  setup_fail "EMAIL_FROM contains an invalid newline"

remote_validated=0
if [[ "${skip_remote_validation}" -eq 0 || "${provision_webhook}" -eq 1 ]]; then
  setup_require_remote_tools
  domains="${SETUP_TEMPORARY_DIR}/domains.json"
  domain_status="$(
    curl --silent --show-error --max-time 15 \
      --output "${domains}" --write-out '%{http_code}' \
      --config - https://api.resend.com/domains <<EOF
header = "Authorization: Bearer ${api_key}"
EOF
  )"
  [[ "${domain_status}" == "200" ]] ||
    setup_fail "Resend rejected the API key (HTTP ${domain_status})"
  jq -e '.data | arrays' "${domains}" >/dev/null ||
    setup_fail "Resend returned an invalid domain response"
  remote_validated=1
fi

if [[ "${provision_webhook}" -eq 1 ]]; then
  webhooks="${SETUP_TEMPORARY_DIR}/webhooks.json"
  list_status="$(
    curl --silent --show-error --max-time 15 \
      --output "${webhooks}" --write-out '%{http_code}' \
      --config - https://api.resend.com/webhooks <<EOF
header = "Authorization: Bearer ${api_key}"
EOF
  )"
  [[ "${list_status}" == "200" ]] ||
    setup_fail "cannot inspect Resend webhooks (HTTP ${list_status})"
  existing_id="$(
    jq -r --arg endpoint "${webhook_url}" \
      '[.data[] | select(.endpoint == $endpoint)][0].id // empty' "${webhooks}"
  )"
  if [[ -n "${existing_id}" ]]; then
    [[ -n "${webhook_secret}" ]] ||
      setup_fail "Resend webhook ${existing_id} already exists, but its signing secret cannot be retrieved; supply --webhook-secret-file"
  else
    payload="${SETUP_TEMPORARY_DIR}/webhook-request.json"
    jq -n --arg endpoint "${webhook_url}" '{
      endpoint: $endpoint,
      events: [
        "email.sent", "email.delivered", "email.delivery_delayed",
        "email.failed", "email.bounced", "email.complained",
        "email.opened", "email.clicked"
      ]
    }' >"${payload}"
    created="${SETUP_TEMPORARY_DIR}/webhook-created.json"
    create_status="$(
      curl --silent --show-error --max-time 20 \
        --output "${created}" --write-out '%{http_code}' \
        --config - --request POST https://api.resend.com/webhooks \
        --header 'Content-Type: application/json' --data-binary "@${payload}" <<EOF
header = "Authorization: Bearer ${api_key}"
EOF
    )"
    [[ "${create_status}" == "200" || "${create_status}" == "201" ]] ||
      setup_fail "Resend webhook creation failed (HTTP ${create_status})"
    webhook_secret="$(jq -r '.signing_secret // .data.signing_secret // empty' "${created}")"
    [[ "${webhook_secret}" =~ ^whsec_[A-Za-z0-9_=-]+$ ]] ||
      setup_fail "Resend did not return a signing secret"
  fi
fi

if [[ -z "${webhook_secret}" ]]; then
  [[ -t 0 ]] ||
    setup_fail "RESEND_WEBHOOK_SECRET is missing; supply it or use --provision-webhook"
  printf 'Resend webhook signing secret for %s (input hidden): ' "${webhook_url}" >&2
  IFS= read -r -s webhook_secret
  printf '\n' >&2
fi
[[ "${webhook_secret}" =~ ^whsec_[A-Za-z0-9_=-]+$ ]] ||
  setup_fail "Resend webhook secret must start with whsec_"

public_config="${SETUP_TEMPORARY_DIR}/email.env"
secret_config="${SETUP_TEMPORARY_DIR}/email.secret.env"
printf '%s\n' \
  'EMAIL_PROVIDER=resend' \
  "EMAIL_FROM=${email_from}" \
  'RESEND_API_BASE=https://api.resend.com' \
  >"${public_config}"
printf '%s\n' \
  "RESEND_API_KEY=${api_key}" \
  "RESEND_WEBHOOK_SECRET=${webhook_secret}" \
  >"${secret_config}"

setup_install_pair email "${public_config}" email "${secret_config}"
setup_doctor

printf '\nResend dogfood configuration is ready.\n'
[[ "${remote_validated}" -eq 0 ]] || printf 'Validated the Resend account.\n'
printf 'Delivery webhook:\n  %s\n' "${webhook_url}"
if [[ "${webhook_origin}" == "${codefly_origin}" ]]; then
  printf 'This is the Codefly-owned local callback. Resend delivery requires a public HTTPS tunnel or deployed ingress.\n'
fi
printf 'Next: send an invitation and verify sent, delivered, bounced, and complaint projections.\n'
