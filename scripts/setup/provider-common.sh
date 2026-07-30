#!/usr/bin/env bash

# Shared, source-only helpers for external-service setup scripts.

setup_fail() {
  printf '%s setup: %s\n' "${SETUP_PROVIDER:-provider}" "$*" >&2
  exit 1
}

setup_require_value() {
  if [[ -z "${2:-}" ]]; then
    setup_fail "$1 requires a value"
  fi
}

setup_trim() {
  local value="${1:-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

setup_read_dotenv_value() {
  local name="$1"
  local file="$2"
  local value
  value="$(
    awk -v wanted="${name}" '
      /^[[:space:]]*#/ { next }
      {
        line = $0
        sub(/^[[:space:]]*export[[:space:]]+/, "", line)
        equals = index(line, "=")
        if (equals == 0) next
        key = substr(line, 1, equals - 1)
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", key)
        if (key == wanted) {
          print substr(line, equals + 1)
          exit
        }
      }
    ' "${file}"
  )"
  value="$(setup_trim "${value}")"
  if [[ "${value}" == \"*\" && "${value}" == *\" ]]; then
    value="${value:1:${#value}-2}"
  elif [[ "${value}" == \'*\' && "${value}" == *\' ]]; then
    value="${value:1:${#value}-2}"
  fi
  printf '%s' "${value}"
}

setup_read_secret_file() {
  local name="$1"
  local file="$2"
  local value
  [[ -f "${file}" ]] || setup_fail "secret file does not exist: ${file}"
  value="$(setup_read_dotenv_value "${name}" "${file}")"
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
      ' "${file}"
    )"
  fi
  printf '%s' "${value}"
}

setup_prepare_workspace() {
  [[ -d "${workspace}" ]] || setup_fail "workspace does not exist: ${workspace}"
  workspace="$(CDPATH= cd -- "${workspace}" && pwd)"
  configuration_dir="${workspace}/configurations/local-dogfood"
  [[ -d "${configuration_dir}" ]] ||
    setup_fail "Codefly dogfood configuration directory is missing: ${configuration_dir}"
  command -v codefly >/dev/null 2>&1 || setup_fail "codefly is required"
  setup_materialize_dogfood_defaults
}

setup_materialize_dogfood_defaults() {
  local name
  local source
  local target
  # A provider can be configured independently. Materialize fail-closed
  # defaults for the other optional capabilities so running one provider's
  # doctor never requires credentials for every vendor at once.
  for name in billing email product-analytics error-tracking observability abuse-protection; do
    source="${workspace}/configurations/local/${name}.env"
    target="${configuration_dir}/${name}.env"
    if [[ ! -f "${target}" ]]; then
      [[ -f "${source}" ]] ||
        setup_fail "default configuration is missing: configurations/local/${name}.env"
      install -m 600 "${source}" "${target}"
    fi
  done
}

setup_public_origin() {
  local origin
  origin="$(
    cd "${workspace}"
    codefly endpoint frontend --type http
  )"
  origin="${origin%/}"
  [[ "${origin}" =~ ^https?://[^[:space:]]+$ ]] ||
    setup_fail "Codefly returned an invalid public endpoint"
  printf '%s' "${origin}"
}

setup_validate_webhook_origin() {
  local origin="${1%/}"
  [[ "${origin}" =~ ^https://[^[:space:]/]+$ ||
     "${origin}" =~ ^http://(localhost|127\.0\.0\.1)(:[0-9]+)?$ ]] ||
    setup_fail "webhook origin must be a public HTTPS origin or Codefly loopback origin"
}

setup_require_remote_webhook_origin() {
  local origin="${1%/}"
  [[ "${origin}" =~ ^https://[^[:space:]/]+$ ]] ||
    setup_fail "remote webhook provisioning cannot target Codefly's loopback endpoint; expose the Codefly ingress and pass its public HTTPS origin with --webhook-origin"
}

setup_prepare_temporary_directory() {
  umask 077
  SETUP_TEMPORARY_DIR="$(mktemp -d "${TMPDIR:-/tmp}/saas-${SETUP_PROVIDER}-setup.XXXXXX")"
  trap 'rm -rf -- "${SETUP_TEMPORARY_DIR}"' EXIT HUP INT TERM
}

setup_assert_ignored() {
  local target="$1"
  local relative
  if command -v git >/dev/null 2>&1 &&
    git -C "${workspace}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    relative="${target#"${workspace}/"}"
    git -C "${workspace}" check-ignore --quiet -- "${relative}" ||
      setup_fail "${relative} is not Git-ignored; refusing to write credentials"
  fi
}

setup_install_pair() {
  local public_name="$1"
  local public_replacement="$2"
  local secret_name="$3"
  local secret_replacement="$4"
  local public_target="${configuration_dir}/${public_name}.env"
  local secret_target="${configuration_dir}/${secret_name}.secret.env"
  local candidate
  local default
  local target
  local replacement

  setup_assert_ignored "${public_target}"
  setup_assert_ignored "${secret_target}"
  for candidate in "${public_target}:${public_replacement}" "${secret_target}:${secret_replacement}"; do
    target="${candidate%%:*}"
    replacement="${candidate#*:}"
    if [[ -f "${target}" ]] && ! cmp -s "${target}" "${replacement}" &&
      [[ "${force:-0}" -ne 1 ]]; then
      default="${workspace}/configurations/local/${public_name}.env"
      if [[ "${target}" != "${public_target}" || ! -f "${default}" ]] ||
        ! cmp -s "${target}" "${default}"; then
        setup_fail "${target#"${workspace}/"} already exists with different values; inspect it or rerun with --force"
      fi
    fi
  done
  install -m 600 "${public_replacement}" "${public_target}"
  install -m 600 "${secret_replacement}" "${secret_target}"
}

setup_doctor() {
  if [[ "${skip_doctor:-0}" -eq 0 ]]; then
    (
      cd "${workspace}"
      codefly doctor workspace --env local-dogfood
    )
  fi
}

setup_require_remote_tools() {
  command -v curl >/dev/null 2>&1 ||
    setup_fail "curl is required for remote validation (or use --skip-remote-validation)"
  command -v jq >/dev/null 2>&1 ||
    setup_fail "jq is required for provider response validation (or use --skip-remote-validation)"
}
