#!/usr/bin/env bash

# Configure Stripe test-mode billing for local Codefly dogfooding. Secrets are
# accepted only through environment variables, dotenv files, hidden prompts,
# or raw one-line files and are never printed.

set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=provider-common.sh
source "${SCRIPT_DIR}/provider-common.sh"

SETUP_PROVIDER="stripe"
DEFAULT_WORKSPACE="$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd)"

workspace="${DEFAULT_WORKSPACE}"
env_file=""
api_key_file=""
webhook_secret_file=""
webhook_origin=""
api_key="${STRIPE_API_KEY:-}"
webhook_secret="${STRIPE_WEBHOOK_SECRET:-}"
force=0
skip_remote_validation=0
skip_doctor=0
provision_webhook=0

usage() {
  printf '%s\n' \
    "Configure Stripe test-mode billing for local-dogfood." \
    "" \
    "Usage: scripts/setup/stripe.sh [options]" \
    "" \
    "  --env-file PATH          Read STRIPE_API_KEY and STRIPE_WEBHOOK_SECRET." \
    "  --api-key-file PATH      Read STRIPE_API_KEY without exposing it in argv." \
    "  --webhook-secret-file PATH" \
    "                           Read STRIPE_WEBHOOK_SECRET safely." \
    "  --webhook-origin URL     Public HTTPS ingress used by Stripe itself." \
    "  --provision-webhook      Explicitly create the Codefly webhook when absent." \
    "  --workspace PATH         Target SaaS starter checkout." \
    "  --force                  Replace differing local configuration." \
    "  --skip-remote-validation Skip read-only Stripe account validation." \
    "  --skip-doctor            Skip Codefly workspace doctor." \
    "  -h, --help               Show this help." \
    "" \
    "The script refuses live-mode keys. Product and price creation remains an" \
    "operator-owned pricing decision; this script configures the tested billing" \
    "adapter and its signed lifecycle webhook."
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
webhook_url="${webhook_origin}/v1/billing/webhook"
setup_prepare_temporary_directory

if [[ -n "${env_file}" ]]; then
  [[ -f "${env_file}" ]] || setup_fail "environment file does not exist: ${env_file}"
  candidate="$(setup_read_dotenv_value STRIPE_API_KEY "${env_file}")"
  [[ -z "${candidate}" ]] || api_key="${candidate}"
  candidate="$(setup_read_dotenv_value STRIPE_WEBHOOK_SECRET "${env_file}")"
  [[ -z "${candidate}" ]] || webhook_secret="${candidate}"
fi
if [[ -n "${api_key_file}" ]]; then
  api_key="$(setup_read_secret_file STRIPE_API_KEY "${api_key_file}")"
fi
if [[ -n "${webhook_secret_file}" ]]; then
  webhook_secret="$(setup_read_secret_file STRIPE_WEBHOOK_SECRET "${webhook_secret_file}")"
fi
if [[ -z "${api_key}" ]]; then
  [[ -t 0 ]] || setup_fail "STRIPE_API_KEY is missing; use --env-file or --api-key-file"
  printf 'Stripe test-mode API key (input hidden): ' >&2
  IFS= read -r -s api_key
  printf '\n' >&2
fi
[[ "${api_key}" =~ ^sk_test_[A-Za-z0-9_]+$ ]] ||
  setup_fail "only a Stripe sk_test_ key is accepted for local dogfooding"

remote_validated=0
if [[ "${skip_remote_validation}" -eq 0 || "${provision_webhook}" -eq 1 ]]; then
  setup_require_remote_tools
  account_response="${SETUP_TEMPORARY_DIR}/account.json"
  account_status="$(
    curl --silent --show-error --max-time 15 \
      --output "${account_response}" --write-out '%{http_code}' \
      --config - https://api.stripe.com/v1/account <<EOF
header = "Authorization: Bearer ${api_key}"
EOF
  )"
  [[ "${account_status}" == "200" ]] ||
    setup_fail "Stripe rejected the API key (HTTP ${account_status})"
  jq -e '.id | strings | startswith("acct_")' "${account_response}" >/dev/null ||
    setup_fail "Stripe returned an invalid account response"
  remote_validated=1
fi

if [[ "${provision_webhook}" -eq 1 ]]; then
  endpoint_list="${SETUP_TEMPORARY_DIR}/webhooks.json"
  list_status="$(
    curl --silent --show-error --max-time 15 \
      --output "${endpoint_list}" --write-out '%{http_code}' \
      --config - 'https://api.stripe.com/v1/webhook_endpoints?limit=100' <<EOF
header = "Authorization: Bearer ${api_key}"
EOF
  )"
  [[ "${list_status}" == "200" ]] ||
    setup_fail "cannot inspect Stripe webhook endpoints (HTTP ${list_status})"
  existing_id="$(
    jq -r --arg url "${webhook_url}" \
      '[.data[] | select(.url == $url and .status == "enabled")][0].id // empty' \
      "${endpoint_list}"
  )"
  if [[ -n "${existing_id}" ]]; then
    [[ -n "${webhook_secret}" ]] ||
      setup_fail "Stripe webhook ${existing_id} already exists, but Stripe cannot reveal its signing secret; supply --webhook-secret-file"
  else
    created="${SETUP_TEMPORARY_DIR}/webhook-created.json"
    create_status="$(
      curl --silent --show-error --max-time 20 \
        --output "${created}" --write-out '%{http_code}' \
        --config - \
        --request POST https://api.stripe.com/v1/webhook_endpoints \
        --data-urlencode "url=${webhook_url}" \
        --data-urlencode 'description=Codefly local-dogfood billing lifecycle' \
        --data-urlencode 'enabled_events[]=customer.subscription.created' \
        --data-urlencode 'enabled_events[]=customer.subscription.updated' \
        --data-urlencode 'enabled_events[]=customer.subscription.deleted' \
        --data-urlencode 'enabled_events[]=invoice.paid' \
        --data-urlencode 'enabled_events[]=invoice.payment_succeeded' \
        --data-urlencode 'enabled_events[]=invoice.payment_failed' \
        --data-urlencode 'enabled_events[]=customer.subscription.trial_will_end' <<EOF
header = "Authorization: Bearer ${api_key}"
EOF
    )"
    [[ "${create_status}" == "200" ]] ||
      setup_fail "Stripe webhook creation failed (HTTP ${create_status})"
    webhook_secret="$(jq -r '.secret // empty' "${created}")"
    [[ "${webhook_secret}" =~ ^whsec_[A-Za-z0-9_]+$ ]] ||
      setup_fail "Stripe did not return a signing secret"
  fi
fi

if [[ -z "${webhook_secret}" ]]; then
  [[ -t 0 ]] ||
    setup_fail "STRIPE_WEBHOOK_SECRET is missing; supply it or use --provision-webhook"
  printf 'Stripe webhook signing secret for %s (input hidden): ' "${webhook_url}" >&2
  IFS= read -r -s webhook_secret
  printf '\n' >&2
fi
[[ "${webhook_secret}" =~ ^whsec_[A-Za-z0-9_]+$ ]] ||
  setup_fail "Stripe webhook secret must start with whsec_"

public_config="${SETUP_TEMPORARY_DIR}/billing.env"
secret_config="${SETUP_TEMPORARY_DIR}/billing.secret.env"
printf '%s\n' \
  'BILLING_PROVIDER=stripe' \
  'STRIPE_API_BASE=https://api.stripe.com' \
  >"${public_config}"
printf '%s\n' \
  "STRIPE_API_KEY=${api_key}" \
  "STRIPE_WEBHOOK_SECRET=${webhook_secret}" \
  >"${secret_config}"

setup_install_pair billing "${public_config}" billing "${secret_config}"
setup_doctor

printf '\nStripe dogfood configuration is ready.\n'
[[ "${remote_validated}" -eq 0 ]] ||
  printf 'Validated the test-mode account and webhook origin.\n'
printf 'Webhook URL:\n  %s\n' "${webhook_url}"
if [[ "${webhook_origin}" == "${codefly_origin}" ]]; then
  printf 'This is the Codefly-owned local callback. Keep a Stripe CLI forwarder running or expose the ingress before expecting remote delivery.\n'
fi
printf 'Next: map the starter Pro plan to a Stripe test price, then run the billing lifecycle smoke suite.\n'
