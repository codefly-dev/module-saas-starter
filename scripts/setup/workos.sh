#!/usr/bin/env bash

# Configure WorkOS as the identity adapter for the runnable local-dogfood
# Codefly environment. This script never accepts an API key as a command-line
# argument and never prints credential values.

set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=provider-common.sh
source "${SCRIPT_DIR}/provider-common.sh"
SETUP_PROVIDER="workos"
DEFAULT_WORKSPACE="$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd)"

workspace="${DEFAULT_WORKSPACE}"
env_file=""
api_key_file=""
client_id="${WORKOS_CLIENT_ID:-}"
api_key="${WORKOS_API_KEY:-}"
# The sign-in button renders "Continue with <display name>". WorkOS is B2B
# infrastructure the end user has no relationship with, so the default names the
# capability rather than the vendor. Override with --display-name.
display_name="SSO"
force=0
skip_remote_validation=0
skip_doctor=0

usage() {
  cat <<'EOF'
Configure WorkOS for the Codefly local-dogfood environment.

Usage:
  scripts/setup/workos.sh [options]

Credential inputs, in precedence order:
  1. --env-file containing WORKOS_CLIENT_ID and WORKOS_API_KEY
  2. --client-id plus --api-key-file
  3. Existing WORKOS_CLIENT_ID and WORKOS_API_KEY environment variables
  4. Interactive prompts

Options:
  --env-file PATH         Read the dashboard's WORKOS_* .env values safely.
  --client-id ID          WorkOS application client ID (not secret).
  --api-key-file PATH     Read the API key from a raw one-line file or a
                          WORKOS_API_KEY=... dotenv file. The key is never
                          accepted as a command-line argument.
  --display-name NAME     Label shown on the sign-in button, rendered as
                          "Continue with NAME". Defaults to "SSO" — end users
                          should never see the identity vendor's name.
  --workspace PATH        Target SaaS starter checkout.
  --force                 Replace differing resolved configuration files.
  --skip-remote-validation
                          Do not make read-only WorkOS API and JWKS checks.
  --skip-doctor           Do not run Codefly workspace doctor after writing.
  -h, --help              Show this help.

Examples:
  scripts/setup/workos.sh --env-file ../workos.env

  scripts/setup/workos.sh \
    --client-id client_01EXAMPLE \
    --api-key-file ../workos

The script writes:
  configurations/local-dogfood/identity.env
  configurations/local-dogfood/identity.secret.env

Both files must already be covered by the repository's Git ignore rules.
EOF
}

fail() {
  printf 'workos setup: %s\n' "$*" >&2
  exit 1
}

require_value() {
  local option="$1"
  local value="${2:-}"
  if [[ -z "${value}" ]]; then
    fail "${option} requires a value"
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      require_value "$1" "${2:-}"
      env_file="$2"
      shift 2
      ;;
    --client-id)
      require_value "$1" "${2:-}"
      client_id="$2"
      shift 2
      ;;
    --display-name)
      require_value "$1" "${2:-}"
      display_name="$2"
      shift 2
      ;;
    --api-key-file)
      require_value "$1" "${2:-}"
      api_key_file="$2"
      shift 2
      ;;
    --workspace)
      require_value "$1" "${2:-}"
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
      fail "unknown option: $1 (use --help)"
      ;;
  esac
done

read_dotenv_value() {
  local name="$1"
  local path="$2"
  local value

  value="$(
    awk -v wanted="${name}" '
      /^[[:space:]]*#/ { next }
      {
        line = $0
        sub(/^[[:space:]]*export[[:space:]]+/, "", line)
        equals = index(line, "=")
        if (equals == 0) {
          next
        }
        key = substr(line, 1, equals - 1)
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", key)
        if (key == wanted) {
          print substr(line, equals + 1)
          exit
        }
      }
    ' "${path}"
  )"

  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  if [[ "${value}" == \"*\" && "${value}" == *\" ]]; then
    value="${value:1:${#value}-2}"
  elif [[ "${value}" == \'*\' && "${value}" == *\' ]]; then
    value="${value:1:${#value}-2}"
  fi
  printf '%s' "${value}"
}

read_api_key_file() {
  local path="$1"
  local value

  value="$(read_dotenv_value "WORKOS_API_KEY" "${path}")"
  if [[ -z "${value}" ]]; then
    value="$(
      awk '
        /^[[:space:]]*#/ { next }
        /^[[:space:]]*$/ { next }
        {
          line = $0
          gsub(/^[[:space:]]+|[[:space:]]+$/, "", line)
          print line
          exit
        }
      ' "${path}"
    )"
  fi
  printf '%s' "${value}"
}

if [[ ! -d "${workspace}" ]]; then
  fail "workspace does not exist: ${workspace}"
fi
workspace="$(CDPATH= cd -- "${workspace}" && pwd)"

if ! command -v codefly >/dev/null 2>&1; then
  fail "codefly is required to resolve the injected frontend endpoint"
fi
callback_origin="$(
  cd "${workspace}"
  codefly endpoint frontend --type http
)"
callback_origin="${callback_origin%/}"
callback_uri="${callback_origin}/auth/callback"

if [[ -n "${env_file}" ]]; then
  if [[ ! -f "${env_file}" ]]; then
    fail "environment file does not exist: ${env_file}"
  fi
  env_client_id="$(read_dotenv_value "WORKOS_CLIENT_ID" "${env_file}")"
  env_api_key="$(read_dotenv_value "WORKOS_API_KEY" "${env_file}")"
  if [[ -n "${env_client_id}" ]]; then
    client_id="${env_client_id}"
  fi
  if [[ -n "${env_api_key}" ]]; then
    api_key="${env_api_key}"
  fi
fi

if [[ -n "${api_key_file}" ]]; then
  if [[ ! -f "${api_key_file}" ]]; then
    fail "API key file does not exist: ${api_key_file}"
  fi
  api_key="$(read_api_key_file "${api_key_file}")"
fi

if [[ -z "${client_id}" ]]; then
  if [[ ! -t 0 ]]; then
    fail "WORKOS_CLIENT_ID is missing; use --env-file or --client-id"
  fi
  printf 'WorkOS Client ID: ' >&2
  IFS= read -r client_id
fi

if [[ -z "${api_key}" ]]; then
  if [[ ! -t 0 ]]; then
    fail "WORKOS_API_KEY is missing; use --env-file or --api-key-file"
  fi
  printf 'WorkOS API key (input hidden): ' >&2
  IFS= read -r -s api_key
  printf '\n' >&2
fi

if [[ ! "${client_id}" =~ ^client_[A-Za-z0-9_]+$ ]]; then
  fail "client ID must start with client_ and contain only letters, digits, or underscores"
fi
if [[ ! "${api_key}" =~ ^sk_[A-Za-z0-9_]+$ ]]; then
  fail "API key must start with sk_ and contain only letters, digits, or underscores"
fi
if [[ ! "${callback_uri}" =~ ^https?://[^[:space:]]+/auth/callback$ ]]; then
  fail "Codefly returned an invalid auth-sidecar REST endpoint: ${callback_origin}"
fi

configuration_dir="${workspace}/configurations/local-dogfood"
public_config="${configuration_dir}/identity.env"
secret_config="${configuration_dir}/identity.secret.env"

if [[ ! -d "${configuration_dir}" ]]; then
  fail "Codefly dogfood configuration directory is missing: ${configuration_dir}"
fi
setup_materialize_dogfood_defaults

if command -v git >/dev/null 2>&1 &&
  git -C "${workspace}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  for config_path in "${public_config}" "${secret_config}"; do
    relative_path="${config_path#"${workspace}/"}"
    if ! git -C "${workspace}" check-ignore --quiet -- "${relative_path}"; then
      fail "${relative_path} is not Git-ignored; refusing to write credentials"
    fi
  done
fi

umask 077
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/saas-workos-setup.XXXXXX")"
cleanup() {
  rm -rf -- "${temporary_dir}"
}
trap cleanup EXIT HUP INT TERM

remote_validation_complete=0
if [[ "${skip_remote_validation}" -eq 0 ]]; then
  if ! command -v curl >/dev/null 2>&1; then
    fail "curl is required for remote validation (or use --skip-remote-validation)"
  fi

  api_status="$(
    curl \
      --silent \
      --show-error \
      --max-time 15 \
      --output /dev/null \
      --write-out '%{http_code}' \
      --config - \
      https://api.workos.com/organizations <<EOF
header = "Authorization: Bearer ${api_key}"
EOF
  )"
  if [[ "${api_status}" != "200" ]]; then
    fail "WorkOS rejected the API key (HTTP ${api_status})"
  fi

  jwks_status="$(
    curl \
      --silent \
      --show-error \
      --max-time 15 \
      --output /dev/null \
      --write-out '%{http_code}' \
      "https://api.workos.com/sso/jwks/${client_id}"
  )"
  if [[ "${jwks_status}" != "200" ]]; then
    fail "WorkOS JWKS is unavailable for the client ID (HTTP ${jwks_status})"
  fi

  authorize_page="${temporary_dir}/authorize.html"
  authorize_status="$(
    curl \
      --silent \
      --show-error \
      --location \
      --max-time 20 \
      --output "${authorize_page}" \
      --write-out '%{http_code}' \
      --get \
      "https://api.workos.com/user_management/authorize" \
      --data-urlencode "response_type=code" \
      --data-urlencode "client_id=${client_id}" \
      --data-urlencode "redirect_uri=${callback_uri}" \
      --data-urlencode "state=codefly-setup-validation" \
      --data-urlencode "provider=authkit" \
      --data-urlencode "code_challenge_method=S256" \
      --data-urlencode "code_challenge=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
  )"
  if [[ "${authorize_status}" != "200" ]]; then
    fail "WorkOS authorization page is unavailable (HTTP ${authorize_status})"
  fi
  if grep -q 'data-hak-page="redirect-uri-invalid"' "${authorize_page}" ||
    grep -q '<title>Invalid Redirect URI</title>' "${authorize_page}"; then
    fail "callback URI is not registered in the WorkOS application: ${callback_uri}"
  fi
  remote_validation_complete=1
fi

temporary_public="${temporary_dir}/identity.env"
temporary_secret="${temporary_dir}/identity.secret.env"

# The token endpoint and key set are backend-only facts: accounts fetches
# ${issuer}/.well-known/openid-configuration at startup and takes token_endpoint
# and jwks_uri from it, so they are never pinned here — a pinned value that
# drifts from the provider is how a login fails after a successful code exchange.
#
# The authorize endpoint is different: the browser redirects the user there to
# begin the flow, and the browser never performs OIDC discovery — it only sees
# the non-secret IDENTITY_* values the Next.js agent exposes as
# NEXT_PUBLIC_IDENTITY_*. So the authorize URL must be written for the frontend;
# without it readIdentityProvider() returns null and the sign-in button vanishes.
printf '%s\n' \
  "IDENTITY_PROVIDER=workos" \
  "IDENTITY_DISPLAY_NAME=${display_name}" \
  "IDENTITY_CLIENT_ID=${client_id}" \
  "IDENTITY_ISSUER=https://api.workos.com/user_management/${client_id}" \
  "IDENTITY_AUTHORIZE_URL=https://api.workos.com/user_management/authorize" \
  "IDENTITY_AUTHORIZE_SELECTOR=authkit" \
  "IDENTITY_CLIENT_ID_CLAIM=client_id" \
  "IDENTITY_ORG_CLAIM=org_id" \
  "IDENTITY_EMAIL_FROM_TOKEN_RESPONSE=true" \
  >"${temporary_public}"

printf '%s\n' \
  "IDENTITY_CLIENT_SECRET=${api_key}" \
  "IDENTITY_MANAGEMENT_API_KEY=${api_key}" \
  >"${temporary_secret}"

for candidate in "${public_config}:${temporary_public}" "${secret_config}:${temporary_secret}"; do
  target="${candidate%%:*}"
  replacement="${candidate#*:}"
  if [[ -f "${target}" ]] && ! cmp -s "${target}" "${replacement}" &&
    [[ "${force}" -ne 1 ]]; then
    fail "${target#"${workspace}/"} already exists with different values; inspect it or rerun with --force"
  fi
done

install -m 600 "${temporary_public}" "${public_config}"
install -m 600 "${temporary_secret}" "${secret_config}"

if [[ "${skip_doctor}" -eq 0 ]]; then
  (
    cd "${workspace}"
    codefly doctor workspace --env local-dogfood
  )
fi

printf '\nWorkOS dogfood configuration is ready.\n'
if [[ "${remote_validation_complete}" -eq 1 ]]; then
  printf 'Validated WorkOS callback:\n  %s\n' "${callback_uri}"
else
  printf 'Callback to verify in the WorkOS Dashboard:\n  %s\n' "${callback_uri}"
fi
printf 'Start the product:\n  codefly run service --env local-dogfood\n'
printf 'Resolve the product URL:\n  codefly endpoint frontend --type http --require-up\n'
