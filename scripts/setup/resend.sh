#!/usr/bin/env bash

# Compatibility shim. Resend transactional-email and delivery-webhook setup moved to the
# codefly-dev/provider-resend plugin, which owns domain and account observation, webhook
# create/observe with lost-response reconciliation, signing-secret capture, setup/runtime
# credential separation, and email configuration projection. This script no longer contacts
# Resend, writes configuration, or provisions webhooks. It shape-checks a supplied API key
# locally so a malformed key is caught early, and prints the exact migration path. Every
# removed behavior fails closed with guidance.

set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=provider-common.sh
source "${SCRIPT_DIR}/provider-common.sh"

SETUP_PROVIDER="resend"
PROVIDER_PLUGIN="codefly-dev/provider-resend"

api_key="${RESEND_API_KEY:-}"

usage() {
  printf '%s\n' \
    "Resend email setup has moved to the ${PROVIDER_PLUGIN} plugin." \
    "" \
    "Usage: scripts/setup/resend.sh [options]" \
    "" \
    "  --env-file PATH        Shape-check RESEND_API_KEY from a dotenv file (never stored)." \
    "  --api-key-file PATH    Shape-check RESEND_API_KEY from a file without exposing it in argv." \
    "  -h, --help             Show this help." \
    "" \
    "The plugin now owns domain and account observation, delivery-webhook create and" \
    "observe, signing-secret capture, setup/runtime credential separation, and email" \
    "configuration projection. Supply credentials to the plugin, never to this script and" \
    "never on the command line. This script contacts nothing, writes nothing, and manages" \
    "no remote resources."
}

removed_flag() {
  setup_fail "$1"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      setup_require_value "$1" "${2:-}"
      [[ -f "$2" ]] || setup_fail "environment file does not exist: $2"
      candidate="$(setup_read_dotenv_value RESEND_API_KEY "$2")"
      [[ -z "${candidate}" ]] || api_key="${candidate}"
      shift 2
      ;;
    --api-key-file)
      setup_require_value "$1" "${2:-}"
      api_key="$(setup_read_secret_file RESEND_API_KEY "$2")"
      shift 2
      ;;
    --webhook-secret-file)
      removed_flag "--webhook-secret-file is removed; this script never captures signing secrets. Hand the webhook secret to the ${PROVIDER_PLUGIN} plugin, which captures it to a durable sink and returns only an opaque handle; a signing secret is never routed through this script."
      ;;
    --webhook-origin)
      removed_flag "--webhook-origin is removed; this script never copies a webhook origin into configuration. The ${PROVIDER_PLUGIN} plugin resolves ingress and projects the email configuration."
      ;;
    --from)
      removed_flag "--from is removed; this script writes no email configuration. The ${PROVIDER_PLUGIN} plugin projects the sender identity."
      ;;
    --provision-webhook)
      removed_flag "--provision-webhook is removed; this script creates no remote resources. Create and observe the delivery webhook with the ${PROVIDER_PLUGIN} plugin (ApplyAction/Observe), which reconciles a lost create response instead of blindly retrying a non-idempotent POST."
      ;;
    --skip-remote-validation)
      removed_flag "--skip-remote-validation is removed; this script performs no remote validation. The ${PROVIDER_PLUGIN} plugin owns read-only account and domain observation (Validate/Observe)."
      ;;
    --force)
      removed_flag "--force is removed; this script writes no configuration, so there is nothing to overwrite. The ${PROVIDER_PLUGIN} plugin owns configuration projection and drift."
      ;;
    --workspace)
      removed_flag "--workspace is removed; this script writes no workspace configuration. The ${PROVIDER_PLUGIN} plugin projects the email configuration into the workspace."
      ;;
    --skip-doctor)
      removed_flag "--skip-doctor is removed; this script runs no doctor. Run the workspace doctor and the ${PROVIDER_PLUGIN} plugin's Doctor operation instead."
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

if [[ -n "${api_key}" ]]; then
  if [[ "${api_key}" =~ ^re_[A-Za-z0-9_]+$ ]]; then
    printf 'Recognized a well-formed Resend API key (re_). It is not stored; hand it to the %s plugin.\n' \
      "${PROVIDER_PLUGIN}"
  else
    setup_fail "the supplied value is not a well-formed Resend API key (expected re_...); even a valid key goes to the ${PROVIDER_PLUGIN} plugin, never this script"
  fi
fi

printf '%s\n' \
  "Resend email setup has moved to the ${PROVIDER_PLUGIN} plugin." \
  "" \
  "The plugin now owns what this script used to do:" \
  "  - read-only account and domain observation (Validate/Observe);" \
  "  - delivery-webhook create and observe with lost-response reconciliation (ApplyAction/Observe);" \
  "  - signing-secret capture to a durable sink;" \
  "  - setup/runtime credential separation via behavioral scope probes;" \
  "  - email configuration projection into the workspace." \
  "" \
  "Give the API key and any webhook secret to the plugin, never to this script and never on" \
  "the command line. A Resend key's effective scope (full_access versus a domain-restricted" \
  "sending key) cannot be told from the key itself; the plugin proves it with a behavioral" \
  "probe, and a full-access key is rejected for production runtime." \
  "" \
  "If an earlier run of this script created a Resend delivery webhook, import it into the" \
  "plugin by its exact remote webhook id after confirmation, not by its endpoint URL — the" \
  "plugin never adopts a webhook from its URL alone."
