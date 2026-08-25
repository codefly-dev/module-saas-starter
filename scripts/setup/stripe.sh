#!/usr/bin/env bash

# Compatibility shim. Stripe billing setup moved to the codefly-dev/provider-stripe
# plugin, which owns account validation, webhook create/observe, secret capture, and
# billing configuration projection. This script no longer contacts Stripe, writes
# configuration, or manages remote resources. It classifies a supplied test-mode key
# locally so a live key is caught early, and prints the exact migration path. Every
# removed behavior fails closed with guidance.

set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=provider-common.sh
source "${SCRIPT_DIR}/provider-common.sh"

SETUP_PROVIDER="stripe"
PROVIDER_PLUGIN="codefly-dev/provider-stripe"

api_key="${STRIPE_API_KEY:-}"

usage() {
  printf '%s\n' \
    "Stripe billing setup has moved to the ${PROVIDER_PLUGIN} plugin." \
    "" \
    "Usage: scripts/setup/stripe.sh [options]" \
    "" \
    "  --env-file PATH        Classify STRIPE_API_KEY from a dotenv file (never stored)." \
    "  --api-key-file PATH    Classify STRIPE_API_KEY from a file without exposing it in argv." \
    "  -h, --help             Show this help." \
    "" \
    "The plugin now owns account validation, webhook create/observe, secret capture," \
    "and billing configuration projection. Supply credentials to the plugin, never to" \
    "this script and never on the command line. This script contacts nothing, writes" \
    "nothing, and manages no remote resources."
}

removed_flag() {
  setup_fail "$1"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      setup_require_value "$1" "${2:-}"
      [[ -f "$2" ]] || setup_fail "environment file does not exist: $2"
      candidate="$(setup_read_dotenv_value STRIPE_API_KEY "$2")"
      [[ -z "${candidate}" ]] || api_key="${candidate}"
      shift 2
      ;;
    --api-key-file)
      setup_require_value "$1" "${2:-}"
      api_key="$(setup_read_secret_file STRIPE_API_KEY "$2")"
      shift 2
      ;;
    --provision-webhook)
      removed_flag "--provision-webhook is removed; this script creates no remote resources. Create and observe the billing webhook endpoint with the ${PROVIDER_PLUGIN} plugin (ApplyAction/Observe)."
      ;;
    --skip-remote-validation)
      removed_flag "--skip-remote-validation is removed; this script performs no remote validation. The ${PROVIDER_PLUGIN} plugin owns read-only account validation (Validate/Observe)."
      ;;
    --webhook-origin)
      removed_flag "--webhook-origin is removed; this script never copies a webhook origin or port into configuration. The ${PROVIDER_PLUGIN} plugin resolves ingress and projects the billing configuration."
      ;;
    --webhook-secret-file)
      removed_flag "--webhook-secret-file is removed; this script never captures signing secrets. Hand the webhook secret to the ${PROVIDER_PLUGIN} plugin, which captures it to a durable sink; a management or runtime credential is never routed through this script."
      ;;
    --force)
      removed_flag "--force is removed; this script writes no configuration, so there is nothing to overwrite. The ${PROVIDER_PLUGIN} plugin owns configuration projection and drift."
      ;;
    --workspace)
      removed_flag "--workspace is removed; this script writes no workspace configuration. The ${PROVIDER_PLUGIN} plugin projects the billing configuration into the workspace."
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
  if [[ "${api_key}" =~ ^(sk|rk)_test_[A-Za-z0-9_]+$ ]]; then
    printf 'Recognized a Stripe test-mode key (%s_test_). It is not stored; hand it to the %s plugin.\n' \
      "${BASH_REMATCH[1]}" "${PROVIDER_PLUGIN}"
  elif [[ "${api_key}" =~ ^(sk|rk)_live_ ]]; then
    setup_fail "the supplied Stripe key is a live-mode key; only test-mode keys (sk_test_ or rk_test_) are usable for local dogfooding, and even those go to the ${PROVIDER_PLUGIN} plugin, never this script"
  else
    setup_fail "the supplied value is not a recognized Stripe test-mode key (expected sk_test_ or rk_test_)"
  fi
fi

printf '%s\n' \
  "Stripe billing setup has moved to the ${PROVIDER_PLUGIN} plugin." \
  "" \
  "The plugin now owns what this script used to do:" \
  "  - read-only account validation (Validate/Observe);" \
  "  - webhook endpoint create and observe (ApplyAction/Observe);" \
  "  - signing-secret capture to a durable sink;" \
  "  - billing configuration projection into the workspace." \
  "" \
  "Give the test-mode key and any webhook secret to the plugin, never to this script" \
  "and never on the command line. This script does not project a management key into" \
  "runtime configuration." \
  "" \
  "If an earlier run of this script created a Stripe webhook endpoint, import it into" \
  "the plugin by its exact endpoint id (we_...), not by URL — the plugin never adopts" \
  "an endpoint from its URL."
